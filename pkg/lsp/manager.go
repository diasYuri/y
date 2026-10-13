//go:build feature_lsp

package lsp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxDocumentBytes = 8 << 20

// Manager reuses language-server processes for each project root/language pair.
type Manager struct {
	root      string
	configs   map[string]ServerConfig
	authorize StartAuthorizer
	mu        sync.Mutex
	servers   map[string]*managedServer
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
}

// Available reports whether this build includes the language-server client.
func Available() bool { return true }

type managedServer struct {
	config      ServerConfig
	root        string
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	client      *Client
	stderr      *limitedBuffer
	processDone chan struct{}
	waitErr     error
	docs        map[string]documentState
	mu          sync.Mutex
}

type documentState struct {
	version int
	hash    [32]byte
}

// NewManager creates a persistent language-server manager for a workspace.
func NewManager(root string, configs []ServerConfig, authorize StartAuthorizer) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{root: root, authorize: authorize, servers: make(map[string]*managedServer), ctx: ctx, cancel: cancel, closeDone: make(chan struct{})}
	m.configs = make(map[string]ServerConfig, len(configs))
	for _, config := range configs {
		config.Language = normalizeLanguage(config.Language)
		config.Args = append([]string(nil), config.Args...)
		m.configs[config.Language] = config
	}
	return m
}

// OpenDocument starts or reuses a server and synchronizes the current disk
// contents before returning the connection.
func (m *Manager) OpenDocument(ctx context.Context, language, path, projectRoot string) (Document, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	language = normalizeLanguage(language)
	if language == "" {
		return Document{}, errors.New("LSP language is not supported for this file")
	}
	if projectRoot == "" {
		projectRoot = m.root
	}
	server, cacheHit, err := m.getServer(ctx, language, projectRoot)
	if err != nil {
		return Document{}, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Document{}, err
	}
	file, err := os.Open(absolute)
	if err != nil {
		return Document{}, fmt.Errorf("read source document for LSP: %w", err)
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return Document{}, fmt.Errorf("read source document for LSP: %w", readErr)
	}
	if closeErr != nil {
		return Document{}, fmt.Errorf("close source document after LSP read: %w", closeErr)
	}
	if len(contents) > maxDocumentBytes {
		return Document{}, fmt.Errorf("source document exceeds the %d-byte LSP synchronization limit; use outline or targeted reads", maxDocumentBytes)
	}
	uri := fileURI(absolute)
	if err := server.syncDocument(uri, absolute, string(contents)); err != nil {
		return Document{}, err
	}
	return Document{Client: server.client, URI: uri, Text: string(contents), Language: language, Server: serverName(server.config), CacheHit: cacheHit}, nil
}

// GetClient starts or reuses a server without opening a document. It is useful
// for workspace-wide requests such as workspace/symbol.
func (m *Manager) GetClient(ctx context.Context, language, projectRoot string) (*Client, string, bool, error) {
	language = normalizeLanguage(language)
	if projectRoot == "" {
		projectRoot = m.root
	}
	server, cacheHit, err := m.getServer(ctx, language, projectRoot)
	if err != nil {
		return nil, "", false, err
	}
	return server.client, serverName(server.config), cacheHit, nil
}

// Running reports whether a server for language/projectRoot is currently alive.
func (m *Manager) Running(language, projectRoot string) bool {
	key := serverKey(projectRoot, normalizeLanguage(language))
	m.mu.Lock()
	server := m.servers[key]
	m.mu.Unlock()
	if server == nil {
		return false
	}
	select {
	case <-server.processDone:
		return false
	case <-server.client.Done():
		return false
	default:
		return true
	}
}

func (m *Manager) getServer(ctx context.Context, language, root string) (*managedServer, bool, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, false, err
	}
	key := serverKey(root, language)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, false, errors.New("LSP manager is closed")
	}
	if server := m.servers[key]; server != nil {
		select {
		case <-server.processDone:
			delete(m.servers, key)
			if server.waitErr != nil {
				// A crashed process is restarted below; the next successful
				// request can make progress without retaining stale state.
			}
		default:
			select {
			case <-server.client.Done():
				delete(m.servers, key)
				_ = killManagedProcess(server.cmd)
			default:
				return server, true, nil
			}
		}
	}
	config, ok := m.configs[language]
	if !ok || strings.TrimSpace(config.Command) == "" {
		return nil, false, fmt.Errorf("no language server is configured for %s", language)
	}
	if m.authorize != nil {
		if err := m.authorize(ctx, config, root); err != nil {
			return nil, false, err
		}
	}
	server, err := startManagedServer(m.ctx, config, root)
	if err != nil {
		return nil, false, fmt.Errorf("start %s language server: %w", serverName(config), err)
	}
	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rootURI := fileURI(root)
	if _, err := server.client.Initialize(initCtx, rootURI); err != nil {
		_ = server.stop(context.Background())
		return nil, false, fmt.Errorf("initialize %s language server: %w%s", serverName(config), err, server.stderrMessage())
	}
	server.client.SetServerRequestHandler(func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == "workspace/configuration" {
			var request struct {
				Items []json.RawMessage `json:"items"`
			}
			_ = json.Unmarshal(params, &request)
			return make([]any, len(request.Items)), nil
		}
		return nil, nil
	})
	m.servers[key] = server
	return server, false, nil
}

func startManagedServer(parent context.Context, config ServerConfig, root string) (*managedServer, error) {
	cmd := exec.CommandContext(parent, config.Command, config.Args...)
	configureManagedProcess(cmd)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &limitedBuffer{max: 32 * 1024}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	server := &managedServer{config: config, root: root, cmd: cmd, stdin: stdin, docs: make(map[string]documentState), processDone: make(chan struct{})}
	server.client = NewClient(stdin, stdout)
	go func() {
		server.waitErr = cmd.Wait()
		close(server.processDone)
	}()
	server.stderr = stderr
	return server, nil
}

func (s *managedServer) syncDocument(uri, path, text string) error {
	versionHash := sha256.Sum256([]byte(text))
	s.mu.Lock()
	defer s.mu.Unlock()
	state, exists := s.docs[uri]
	if exists && state.hash == versionHash {
		return nil
	}
	languageID := languageID(s.config.Language)
	if !exists {
		params, _ := json.Marshal(map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": languageID, "version": 1, "text": text}})
		if err := s.client.Notify("textDocument/didOpen", params); err != nil {
			return err
		}
		state.version = 1
	} else {
		state.version++
		params, _ := json.Marshal(map[string]any{"textDocument": map[string]any{"uri": uri, "version": state.version}, "contentChanges": []any{map[string]string{"text": text}}})
		if err := s.client.Notify("textDocument/didChange", params); err != nil {
			return err
		}
	}
	state.hash = versionHash
	s.docs[uri] = state
	_ = path
	return nil
}

func (s *managedServer) stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	shutdownErr := s.client.Shutdown(shutdownCtx)
	cancel()
	_ = s.stdin.Close()
	select {
	case <-s.processDone:
		return nil
	case <-time.After(2 * time.Second):
		if s.cmd.Process != nil {
			_ = killManagedProcess(s.cmd)
		}
	}
	select {
	case <-s.processDone:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return shutdownErr
	case <-time.After(2 * time.Second):
		return errors.New("timed out waiting for language server process to exit")
	case <-ctx.Done():
		if s.cmd.Process != nil {
			_ = killManagedProcess(s.cmd)
		}
		return ctx.Err()
	}
}

// Close gracefully shuts down every server, then terminates any process that
// did not exit in the grace period. It is safe to call more than once.
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		servers := make([]*managedServer, 0, len(m.servers))
		for _, server := range m.servers {
			servers = append(servers, server)
		}
		m.mu.Unlock()
		go func() {
			defer close(m.closeDone)
			var closeErr error
			for _, server := range servers {
				if err := server.stop(ctx); err != nil && closeErr == nil {
					closeErr = err
				}
			}
			m.cancel()
			m.mu.Lock()
			m.closeErr = closeErr
			m.mu.Unlock()
		}()
	})
	select {
	case <-m.closeDone:
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.closeErr
	case <-ctx.Done():
		m.cancel()
		m.mu.Lock()
		for _, server := range m.servers {
			if server.cmd.Process != nil {
				_ = killManagedProcess(server.cmd)
			}
		}
		m.mu.Unlock()
		return ctx.Err()
	}
}

func serverKey(root, language string) string {
	return filepath.Clean(root) + "\x00" + normalizeLanguage(language)
}

func normalizeLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "js", "javascript", "jsx":
		return "javascript"
	case "ts", "typescript", "tsx":
		return "typescript"
	case "py", "python":
		return "python"
	case "c", "h", "cpp", "c++", "cxx", "hpp":
		return "cpp"
	case "cs", "csharp", "c#":
		return "csharp"
	case "rs", "rust":
		return "rust"
	case "go", "golang":
		return "go"
	case "java":
		return "java"
	default:
		return ""
	}
}

func languageID(language string) string {
	if language == "cpp" {
		return "cpp"
	}
	if language == "csharp" {
		return "csharp"
	}
	return normalizeLanguage(language)
}

func serverName(config ServerConfig) string {
	if config.Name != "" {
		return config.Name
	}
	return config.Command
}

func fileURI(path string) string {
	abs, _ := filepath.Abs(path)
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
}

type limitedBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(b.buf) < b.max {
		remain := b.max - len(b.buf)
		if remain > len(p) {
			remain = len(p)
		}
		b.buf = append(b.buf, p[:remain]...)
	}
	return n, nil
}

func (s *managedServer) stderrMessage() string {
	buffer, ok := s.cmd.Stderr.(*limitedBuffer)
	if !ok {
		return ""
	}
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	message := strings.TrimSpace(string(buffer.buf))
	if message == "" {
		return ""
	}
	return ": " + message
}

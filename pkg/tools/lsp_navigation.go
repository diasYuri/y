package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/diasYuri/y/pkg/lsp"
)

// LSPOptions configures semantic navigation tools and language servers.
type LSPOptions struct {
	WorkspaceRoot string
	Policy        Policy
	Limits        ToolLimits
	Servers       []lsp.ServerConfig
}

// RegisterLSP registers semantic language-server navigation tools.
func RegisterLSP(registry *Registry, opts LSPOptions) error {
	if registry == nil {
		return fmt.Errorf("tool registry is nil")
	}
	root, err := filepath.Abs(opts.WorkspaceRoot)
	if err != nil || strings.TrimSpace(opts.WorkspaceRoot) == "" {
		return fmt.Errorf("LSP workspace root is required")
	}
	t := &lspTool{workspaceRoot: root, policy: opts.Policy, limits: commandLimits(opts.Limits), registry: registry}
	t.servers = defaultLSPServers()
	for _, server := range opts.Servers {
		language := normalizeToolLanguage(server.Language)
		if language == "" || strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("LSP server requires a supported language and command")
		}
		server.Language = language
		t.servers[language] = server
	}
	configs := make([]lsp.ServerConfig, 0, len(t.servers))
	for _, config := range t.servers {
		configs = append(configs, config)
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Language < configs[j].Language })
	t.manager = lsp.NewManager(root, configs, func(ctx context.Context, config lsp.ServerConfig, projectRoot string) error {
		resolvedCommand := config.Command
		if found, err := exec.LookPath(config.Command); err == nil {
			resolvedCommand = found
		}
		return registry.authorizeOperation(ctx, PolicyRequest{
			ToolName:             "lsp_server_start",
			Capability:           string(CapabilityProcessExec),
			WorkspaceRoot:        root,
			Path:                 config.Command,
			ResolvedPath:         resolvedCommand,
			Sensitive:            true,
			Arguments:            marshalToolJSON(map[string]any{"language": config.Language, "server": config.Name, "project_root": projectRoot}),
			RequiredCapabilities: []string{string(CapabilityProcessExec)},
		})
	})
	if err := registry.AddCloser(t.manager.Close); err != nil {
		return err
	}
	defs := []struct {
		desc    ToolDescriptor
		handler ToolHandler
	}{
		{t.positionDescriptor("definition", "Find the exact semantic definition of the symbol at the specified source position using the language server. Prefer this over text search once you have identified the relevant symbol occurrence."), ToolHandlerFunc(t.definition)},
		{t.referencesDescriptor(), ToolHandlerFunc(t.references)},
		{t.positionDescriptor("implementations", "Find concrete implementations of an interface, abstract method, trait, or other implementable symbol."), ToolHandlerFunc(t.implementations)},
		{t.positionDescriptor("hover", "Return semantic information such as the resolved type, signature, documentation, and symbol information at the selected position. Use this when type resolution can avoid unnecessary source-code reading."), ToolHandlerFunc(t.hover)},
		{t.documentSymbolsDescriptor(), ToolHandlerFunc(t.documentSymbols)},
		{t.positionDescriptor("callers", "Find functions or methods that call the selected symbol. Use this to move upward through an execution path."), ToolHandlerFunc(t.callers)},
		{t.positionDescriptor("callees", "Find functions or methods called by the selected symbol. Use this to move downward through an execution path."), ToolHandlerFunc(t.callees)},
		{t.positionDescriptor("type_definition", "Find the type definition of the symbol at the specified source position."), ToolHandlerFunc(t.typeDefinition)},
		{t.workspaceSymbolsDescriptor(), ToolHandlerFunc(t.workspaceSymbols)},
	}
	for _, def := range defs {
		if err := registry.Add(def.desc, def.handler); err != nil {
			return err
		}
	}
	return nil
}

type lspTool struct {
	workspaceRoot string
	policy        Policy
	limits        ToolLimits
	registry      *Registry
	manager       *lsp.Manager
	servers       map[string]lsp.ServerConfig
	mu            sync.Mutex
	active        map[string]string
}

func (t *lspTool) positionDescriptor(name, description string) ToolDescriptor {
	return ToolDescriptor{Name: name, Description: description, InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"line":{"type":"integer","minimum":1},"column":{"type":"integer","minimum":1}},"required":["path","line","column"],"additionalProperties":false}`), Capabilities: []Capability{CapabilityFilesystemRead}, Limits: t.limits}
}

func (t *lspTool) referencesDescriptor() ToolDescriptor {
	return ToolDescriptor{Name: "references", Description: "Find semantic references to the exact symbol at the specified position. Use this instead of searching the symbol name with text search when you need actual usages rather than textual matches.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"line":{"type":"integer","minimum":1},"column":{"type":"integer","minimum":1},"include_declaration":{"type":"boolean"}},"required":["path","line","column"],"additionalProperties":false}`), Capabilities: []Capability{CapabilityFilesystemRead}, Limits: t.limits}
}

func (t *lspTool) documentSymbolsDescriptor() ToolDescriptor {
	return ToolDescriptor{Name: "document_symbols", Description: "Return semantic symbols declared in a source document, including nested members and source ranges.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`), Capabilities: []Capability{CapabilityFilesystemRead}, Limits: t.limits}
}

func (t *lspTool) workspaceSymbolsDescriptor() ToolDescriptor {
	return ToolDescriptor{Name: "workspace_symbols", Description: "Search symbols indexed by the language server. Provide a language when the workspace contains multiple languages.", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"language":{"type":"string"},"path":{"type":"string"}},"required":["query","language"],"additionalProperties":false}`), Capabilities: []Capability{CapabilityFilesystemRead}, Limits: t.limits}
}

type lspPositionInput struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

type lspReferencesInput struct {
	Path               string `json:"path"`
	Line               int    `json:"line"`
	Column             int    `json:"column"`
	IncludeDeclaration bool   `json:"include_declaration,omitempty"`
}

type lspDocumentInput struct {
	Path string `json:"path"`
}

type lspWorkspaceSymbolsInput struct {
	Query    string `json:"query"`
	Language string `json:"language"`
	Path     string `json:"path,omitempty"`
}

type lspInvocation struct {
	doc       lsp.Document
	root      string
	line      int
	character int
	language  string
	startedAt time.Time
}

func (t *lspTool) definition(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	ctx, cancel := t.toolContext(ctx)
	defer cancel()
	var in lspPositionInput
	inv, err := t.position(ctx, req, &in)
	if err != nil {
		return ToolResponse{}, err
	}
	raw, err := inv.doc.Client.Definition(ctx, inv.doc.URI, inv.line, inv.character)
	return t.lspResponse("definition", inv, raw, err)
}

func (t *lspTool) references(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	ctx, cancel := t.toolContext(ctx)
	defer cancel()
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var in lspReferencesInput
	if err := json.Unmarshal(req.Arguments, &in); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "references arguments must be valid JSON", err)
	}
	base := lspPositionInput{Path: in.Path, Line: in.Line, Column: in.Column}
	inv, err := t.openPosition(ctx, req, base)
	if err != nil {
		return ToolResponse{}, err
	}
	raw, err := inv.doc.Client.References(ctx, inv.doc.URI, inv.line, inv.character, in.IncludeDeclaration)
	return t.lspResponse("references", inv, raw, err)
}

func (t *lspTool) implementations(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	ctx, cancel := t.toolContext(ctx)
	defer cancel()
	var in lspPositionInput
	inv, err := t.position(ctx, req, &in)
	if err != nil {
		return ToolResponse{}, err
	}
	raw, err := inv.doc.Client.Implementations(ctx, inv.doc.URI, inv.line, inv.character)
	return t.lspResponse("implementations", inv, raw, err)
}

func (t *lspTool) hover(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	ctx, cancel := t.toolContext(ctx)
	defer cancel()
	var in lspPositionInput
	inv, err := t.position(ctx, req, &in)
	if err != nil {
		return ToolResponse{}, err
	}
	raw, err := inv.doc.Client.Hover(ctx, inv.doc.URI, inv.line, inv.character)
	return t.lspResponse("hover", inv, raw, err)
}

func (t *lspTool) typeDefinition(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	ctx, cancel := t.toolContext(ctx)
	defer cancel()
	var in lspPositionInput
	inv, err := t.position(ctx, req, &in)
	if err != nil {
		return ToolResponse{}, err
	}
	raw, err := inv.doc.Client.TypeDefinition(ctx, inv.doc.URI, inv.line, inv.character)
	return t.lspResponse("type_definition", inv, raw, err)
}

func (t *lspTool) callers(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	ctx, cancel := t.toolContext(ctx)
	defer cancel()
	var in lspPositionInput
	inv, err := t.position(ctx, req, &in)
	if err != nil {
		return ToolResponse{}, err
	}
	return t.callHierarchy(ctx, "callers", inv, true)
}

func (t *lspTool) callees(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	ctx, cancel := t.toolContext(ctx)
	defer cancel()
	var in lspPositionInput
	inv, err := t.position(ctx, req, &in)
	if err != nil {
		return ToolResponse{}, err
	}
	return t.callHierarchy(ctx, "callees", inv, false)
}

func (t *lspTool) callHierarchy(ctx context.Context, name string, inv lspInvocation, incoming bool) (ToolResponse, error) {
	prepared, err := inv.doc.Client.CallHierarchy(ctx, inv.doc.URI, inv.line, inv.character)
	if err != nil {
		return ToolResponse{}, lspToolError(name, err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(prepared, &items); err != nil {
		return ToolResponse{}, toolError("lsp_invalid_response", "language server returned an invalid call hierarchy", err)
	}
	var calls []json.RawMessage
	for _, item := range items {
		var raw json.RawMessage
		if incoming {
			raw, err = inv.doc.Client.IncomingCalls(ctx, item)
		} else {
			raw, err = inv.doc.Client.OutgoingCalls(ctx, item)
		}
		if err != nil {
			return ToolResponse{}, lspToolError(name, err)
		}
		var part []json.RawMessage
		if json.Unmarshal(raw, &part) == nil {
			calls = append(calls, part...)
		}
		if len(calls) >= t.limits.MaxMatches {
			calls = calls[:t.limits.MaxMatches]
			break
		}
	}
	result, _ := json.Marshal(map[string]any{"symbol": items, "calls": calls})
	return t.lspResponse(name, inv, result, nil)
}

func (t *lspTool) documentSymbols(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	ctx, cancel := t.toolContext(ctx)
	defer cancel()
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var in lspDocumentInput
	if err := json.Unmarshal(req.Arguments, &in); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "document_symbols arguments must be valid JSON", err)
	}
	inv, err := t.openDocument(ctx, req, in.Path)
	if err != nil {
		return ToolResponse{}, err
	}
	raw, err := inv.doc.Client.DocumentSymbols(ctx, inv.doc.URI)
	return t.lspResponse("document_symbols", inv, raw, err)
}

func (t *lspTool) workspaceSymbols(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	ctx, cancel := t.toolContext(ctx)
	defer cancel()
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var in lspWorkspaceSymbolsInput
	if err := json.Unmarshal(req.Arguments, &in); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "workspace_symbols arguments must be valid JSON", err)
	}
	language := normalizeToolLanguage(in.Language)
	if language == "" {
		return ToolResponse{}, toolError("invalid_arguments", "workspace_symbols language is required", ErrInvalidTool)
	}
	if strings.TrimSpace(in.Query) == "" {
		return ToolResponse{}, toolError("invalid_arguments", "workspace_symbols query is required", ErrInvalidTool)
	}
	if !lsp.Available() {
		return ToolResponse{}, toolError("lsp_unavailable", "LSP integration is not compiled in this build; continue with search_ast, search_text, and targeted reads", lsp.ErrUnavailable)
	}
	projectRoot := t.projectRoot(language, firstNonEmpty(in.Path, t.workspaceRoot), t.workspaceRoot)
	if err := t.authorizeLSPRead(ctx, req, projectRoot, in.Path, "workspace_symbols"); err != nil {
		return ToolResponse{}, err
	}
	config, err := t.serverConfig(language)
	if err != nil {
		return ToolResponse{}, err
	}
	if _, err := exec.LookPath(config.Command); err != nil {
		return ToolResponse{}, serverMissingError(language, config, err)
	}
	startedAt := time.Now()
	client, serverName, cacheHit, err := t.manager.GetClient(ctx, language, projectRoot)
	if err != nil {
		return ToolResponse{}, lspToolError("workspace_symbols", err)
	}
	raw, err := client.WorkspaceSymbols(ctx, in.Query)
	inv := lspInvocation{root: projectRoot, language: language, startedAt: startedAt}
	response, err := t.lspResponse("workspace_symbols", inv, raw, err)
	if err == nil {
		response.Metadata = marshalToolJSON(map[string]any{"language": language, "server": serverName, "cache_hit": cacheHit, "duration_ms": time.Since(startedAt).Milliseconds(), "result_count": countTopLevel(raw), "truncated": false})
	}
	return response, err
}

func (t *lspTool) position(ctx context.Context, req ToolRequest, input *lspPositionInput) (lspInvocation, error) {
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return lspInvocation{}, err
	}
	if err := json.Unmarshal(req.Arguments, input); err != nil {
		return lspInvocation{}, toolError("invalid_arguments", req.Name+" arguments must be valid JSON", err)
	}
	return t.openPosition(ctx, req, *input)
}

func (t *lspTool) openPosition(ctx context.Context, req ToolRequest, input lspPositionInput) (lspInvocation, error) {
	if input.Line < 1 || input.Column < 1 {
		return lspInvocation{}, toolError("invalid_arguments", "line and column are 1-based and must be positive", ErrInvalidTool)
	}
	inv, err := t.openDocument(ctx, req, input.Path)
	if err != nil {
		return lspInvocation{}, err
	}
	lineText, ok := sourceLine(inv.doc.Text, input.Line)
	if !ok {
		return lspInvocation{}, toolError("invalid_arguments", "line is outside the source document", ErrInvalidTool)
	}
	character, err := runeColumnToUTF16(lineText, input.Column)
	if err != nil {
		return lspInvocation{}, toolError("invalid_arguments", "column is outside the source line", err)
	}
	inv.line, inv.character = input.Line-1, character
	return inv, nil
}

func runeColumnToUTF16(line string, oneBasedColumn int) (int, error) {
	if oneBasedColumn < 1 {
		return 0, ErrInvalidTool
	}
	runes := []rune(line)
	if oneBasedColumn > len(runes)+1 {
		return 0, ErrInvalidTool
	}
	units := 0
	for _, r := range runes[:oneBasedColumn-1] {
		units += utf16.RuneLen(r)
	}
	return units, nil
}

func (t *lspTool) openDocument(ctx context.Context, req ToolRequest, inputPath string) (lspInvocation, error) {
	if strings.TrimSpace(inputPath) == "" {
		return lspInvocation{}, toolError("invalid_arguments", "source path is required", ErrInvalidTool)
	}
	path, err := resolveForRead(firstNonEmpty(req.WorkspaceRoot, t.workspaceRoot), inputPath)
	if err != nil {
		return lspInvocation{}, err
	}
	if err := authorize(ctx, t.policy, PolicyRequest{ToolName: req.Name, Capability: string(CapabilityFilesystemRead), WorkspaceRoot: path.WorkspaceRoot, Path: inputPath, ResolvedPath: path.Absolute, EscapesWorkspace: path.EscapesWorkspace}); err != nil {
		return lspInvocation{}, err
	}
	language := detectToolLanguage(path.Absolute)
	if language == "" {
		return lspInvocation{}, toolError("language_unsupported", "LSP does not recognize this file type; use search_ast, search_text, and targeted reads", ErrInvalidTool)
	}
	projectRoot := t.projectRoot(language, path.Absolute, path.WorkspaceRoot)
	config, err := t.serverConfig(language)
	if err != nil {
		return lspInvocation{}, err
	}
	if !lsp.Available() {
		return lspInvocation{}, toolError("lsp_unavailable", "LSP integration is not compiled in this build; continue with search_ast, search_text, and targeted reads", lsp.ErrUnavailable)
	}
	if _, err := exec.LookPath(config.Command); err != nil {
		return lspInvocation{}, serverMissingError(language, config, err)
	}
	startedAt := time.Now()
	doc, err := t.manager.OpenDocument(ctx, language, path.Absolute, projectRoot)
	if err != nil {
		if errors.Is(err, lsp.ErrUnavailable) {
			return lspInvocation{}, toolError("lsp_unavailable", "LSP integration is not compiled in this build; continue with search_ast, search_text, and targeted reads", err)
		}
		return lspInvocation{}, lspToolError(req.Name, err)
	}
	t.mu.Lock()
	if t.active == nil {
		t.active = make(map[string]string)
	}
	t.active[language] = projectRoot
	t.mu.Unlock()
	return lspInvocation{doc: doc, root: path.WorkspaceRoot, language: language, startedAt: startedAt}, nil
}

func (t *lspTool) lspResponse(name string, inv lspInvocation, raw json.RawMessage, callErr error) (ToolResponse, error) {
	if callErr != nil {
		return ToolResponse{}, lspToolError(name, callErr)
	}
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	normalized, err := normalizeLSPResult(raw, inv.root, inv.doc.Text, t.limits.MaxMatches)
	if err != nil {
		return ToolResponse{}, toolError("lsp_invalid_response", "language server returned invalid result data", err)
	}
	if int64(len(normalized)) > t.limits.MaxOutputBytes {
		return ToolResponse{}, toolError("output_limit", "language-server result exceeds the output limit; narrow the query", ErrLimitExceeded)
	}
	result := countTopLevel(normalized)
	metadata := marshalToolJSON(map[string]any{"language": inv.language, "server": inv.doc.Server, "cache_hit": inv.doc.CacheHit, "duration_ms": time.Since(inv.startedAt).Milliseconds(), "result_count": result, "truncated": result < countTopLevel(raw)})
	return ToolResponse{Content: []ContentBlock{{Type: ContentText, Text: string(normalized)}}, Details: normalized, Metadata: metadata}, nil
}

func normalizeLSPResult(raw json.RawMessage, workspaceRoot, fallbackText string, maxItems int) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	cache := make(map[string][]string)
	value = normalizeLSPValue(value, workspaceRoot, "", fallbackText, cache, maxItems)
	return json.Marshal(value)
}

func normalizeLSPValue(value any, root, currentPath, fallbackText string, cache map[string][]string, maxItems int) any {
	switch typed := value.(type) {
	case []any:
		if maxItems > 0 && len(typed) > maxItems {
			typed = typed[:maxItems]
		}
		for i := range typed {
			typed[i] = normalizeLSPValue(typed[i], root, currentPath, fallbackText, cache, maxItems)
		}
		return typed
	case map[string]any:
		uri := ""
		for _, key := range []string{"uri", "targetUri"} {
			if rawURI, ok := typed[key].(string); ok && rawURI != "" {
				uri = rawURI
				break
			}
		}
		if uri != "" {
			currentPath = lspURIPath(uri)
			if currentPath != "" {
				resolved, err := resolveForRead(root, currentPath)
				if err == nil && !resolved.EscapesWorkspace {
					if rel, relErr := filepath.Rel(root, resolved.Absolute); relErr == nil {
						typed["path"] = filepath.ToSlash(rel)
						currentPath = resolved.Absolute
					}
				} else {
					currentPath = ""
				}
			}
			delete(typed, "uri")
			delete(typed, "targetUri")
		}
		if line, lineOK := typed["line"].(float64); lineOK {
			if character, charOK := typed["character"].(float64); charOK {
				lines := sourceLinesForPath(currentPath, fallbackText, cache)
				lineIndex := int(line)
				column := int(character)
				if lineIndex >= 0 && lineIndex < len(lines) {
					column = utf16ToRuneColumn(lines[lineIndex], column)
				} else {
					column++
				}
				typed["line"] = int(line) + 1
				typed["character"] = column
			}
		}
		for key, child := range typed {
			typed[key] = normalizeLSPValue(child, root, currentPath, fallbackText, cache, maxItems)
		}
		return typed
	default:
		return value
	}
}

func sourceLinesForPath(path, fallback string, cache map[string][]string) []string {
	if path == "" {
		return strings.Split(fallback, "\n")
	}
	if lines, ok := cache[path]; ok {
		return lines
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 8<<20 {
		cache[path] = nil
		return nil
	}
	lines := strings.Split(string(data), "\n")
	cache[path] = lines
	return lines
}

func utf16ToRuneColumn(line string, units int) int {
	if units < 0 {
		units = 0
	}
	used, runes := 0, 0
	for _, r := range line {
		if used >= units {
			break
		}
		used += utf16.RuneLen(r)
		runes++
	}
	return runes + 1
}

func sourceLine(text string, line int) (string, bool) {
	lines := strings.Split(text, "\n")
	if line < 1 || line > len(lines) {
		return "", false
	}
	return strings.TrimSuffix(lines[line-1], "\r"), true
}

func (t *lspTool) serverConfig(language string) (lsp.ServerConfig, error) {
	config, ok := t.servers[language]
	if !ok {
		return lsp.ServerConfig{}, toolError("language_unsupported", "no language-server integration is registered for "+language, ErrInvalidTool)
	}
	return config, nil
}

func (t *lspTool) projectRoot(language, path, workspaceRoot string) string {
	root := workspaceRoot
	if root == "" {
		root = t.workspaceRoot
	}
	if absolute, err := filepath.Abs(path); err == nil {
		if info, err := os.Stat(absolute); err == nil && !info.IsDir() {
			absolute = filepath.Dir(absolute)
		}
		for dir := absolute; ; dir = filepath.Dir(dir) {
			if projectMarker(dir, language) {
				return dir
			}
			if filepath.Clean(dir) == filepath.Clean(root) || filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return root
}

func (t *lspTool) authorizeLSPRead(ctx context.Context, req ToolRequest, root, path, name string) error {
	resolved, err := resolveForRead(firstNonEmpty(req.WorkspaceRoot, t.workspaceRoot), firstNonEmpty(path, "."))
	if err != nil {
		return err
	}
	_ = root
	return authorize(ctx, t.policy, PolicyRequest{ToolName: name, Capability: string(CapabilityFilesystemRead), WorkspaceRoot: resolved.WorkspaceRoot, Path: path, ResolvedPath: resolved.Absolute, EscapesWorkspace: resolved.EscapesWorkspace})
}

func (t *lspTool) toolContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, time.Duration(t.limits.CommandTimeoutSeconds)*time.Second)
}

func serverMissingError(language string, config lsp.ServerConfig, err error) error {
	return toolError("server_unavailable", fmt.Sprintf("%s is supported, but language server %q is not installed or not on PATH; install it or configure its command and arguments", language, config.Command), err)
}

func lspToolError(name string, err error) error {
	if errors.Is(err, lsp.ErrUnavailable) {
		return toolError("lsp_unavailable", "LSP integration is not compiled in this build; continue with search_ast, search_text, and targeted reads", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return toolError("timeout", name+" timed out while waiting for the language server", err)
	}
	return toolError("lsp_failed", name+" failed; verify the language server and use search_ast or search_text as fallback", err)
}

func defaultLSPServers() map[string]lsp.ServerConfig {
	defaults := map[string]lsp.ServerConfig{
		"go":         {Language: "go", Name: "gopls", Command: "gopls"},
		"rust":       {Language: "rust", Name: "rust-analyzer", Command: "rust-analyzer"},
		"typescript": {Language: "typescript", Name: "typescript-language-server", Command: "typescript-language-server", Args: []string{"--stdio"}},
		"javascript": {Language: "javascript", Name: "typescript-language-server", Command: "typescript-language-server", Args: []string{"--stdio"}},
		"python":     {Language: "python", Name: "basedpyright", Command: "basedpyright-langserver", Args: []string{"--stdio"}},
		"cpp":        {Language: "cpp", Name: "clangd", Command: "clangd"},
		"csharp":     {Language: "csharp", Name: "Roslyn-based LSP", Command: "csharp-ls"},
		"java":       {Language: "java", Name: "jdtls", Command: "jdtls"},
	}
	if _, err := exec.LookPath(defaults["python"].Command); err != nil {
		defaults["python"] = lsp.ServerConfig{Language: "python", Name: "pyright", Command: "pyright-langserver", Args: []string{"--stdio"}}
	}
	if _, err := exec.LookPath(defaults["csharp"].Command); err != nil {
		defaults["csharp"] = lsp.ServerConfig{Language: "csharp", Name: "Roslyn-based LSP", Command: "Microsoft.CodeAnalysis.LanguageServer"}
	}
	return defaults
}

func normalizeToolLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "go", "golang":
		return "go"
	case "rs", "rust":
		return "rust"
	case "ts", "typescript", "tsx":
		return "typescript"
	case "js", "javascript", "jsx":
		return "javascript"
	case "py", "python":
		return "python"
	case "c", "h", "cc", "cpp", "c++", "cxx", "hpp", "hh", "hxx":
		return "cpp"
	case "cs", "csharp", "c#":
		return "csharp"
	case "java":
		return "java"
	default:
		return ""
	}
}

func detectToolLanguage(path string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	return normalizeToolLanguage(ext)
}

func projectMarker(dir, language string) bool {
	markers := map[string][]string{
		"go": {"go.mod", "go.work"}, "rust": {"Cargo.toml"},
		"typescript": {"package.json", "tsconfig.json"}, "javascript": {"package.json"},
		"python": {"pyproject.toml", "setup.py", "setup.cfg"},
		"cpp":    {"compile_commands.json", "CMakeLists.txt"},
		"csharp": {".sln", ".csproj"}, "java": {"pom.xml", "build.gradle", "settings.gradle", "gradlew"},
	}
	for _, marker := range markers[language] {
		if strings.HasPrefix(marker, ".") {
			matches, _ := filepath.Glob(filepath.Join(dir, "*"+marker))
			if len(matches) > 0 {
				return true
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return true
	}
	return false
}

func lspURIPath(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "file" {
		return ""
	}
	return filepath.FromSlash(parsed.Path)
}

func countTopLevel(raw []byte) int {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return 0
	}
	switch result := value.(type) {
	case []any:
		return len(result)
	case map[string]any:
		if calls, ok := result["calls"].([]any); ok {
			return len(calls)
		}
		return 1
	case nil:
		return 0
	default:
		return 1
	}
}

func marshalToolJSON(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

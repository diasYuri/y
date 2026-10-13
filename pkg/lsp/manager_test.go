//go:build feature_lsp

package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLSPHelperServer(t *testing.T) {
	if flagValue("test.run") != "^TestLSPHelperServer$" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		message, err := readHelperMessage(reader)
		if err != nil {
			return
		}
		logPath := os.Getenv("Y_LSP_HELPER_LOG")
		if logPath != "" {
			file, openErr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if openErr == nil {
				_, _ = fmt.Fprintln(file, message.Method)
				_ = file.Close()
			}
		}
		if message.ID == nil {
			if message.Method == "exit" {
				return
			}
			continue
		}
		result := json.RawMessage(`[]`)
		if message.Method == "initialize" {
			result = json.RawMessage(`{"capabilities":{}}`)
		}
		if message.Method == "slow" {
			time.Sleep(150 * time.Millisecond)
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *message.ID, "result": result})
		_, _ = fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n", len(body))
		_, _ = os.Stdout.Write(body)
	}
}

func TestManager_ReusesSynchronizesAndClosesServer(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "requests.log")
	t.Setenv("Y_LSP_HELPER_LOG", logPath)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var starts int
	manager := NewManager(root, []ServerConfig{{Language: "go", Name: "fixture", Command: executable, Args: []string{"-test.run=^TestLSPHelperServer$"}}}, func(context.Context, ServerConfig, string) error {
		starts++
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := manager.OpenDocument(ctx, "go", path, root)
	if err != nil {
		t.Fatalf("first OpenDocument: %v", err)
	}
	second, err := manager.OpenDocument(ctx, "go", path, root)
	if err != nil {
		t.Fatalf("second OpenDocument: %v", err)
	}
	if first.CacheHit || !second.CacheHit || starts != 1 {
		t.Fatalf("reuse = first cache %t, second cache %t, starts %d", first.CacheHit, second.CacheHit, starts)
	}
	if _, err := first.Client.Definition(ctx, first.URI, 0, 0); err != nil {
		t.Fatalf("definition request: %v", err)
	}
	if err := os.WriteFile(path, []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenDocument(ctx, "go", path, root); err != nil {
		t.Fatalf("OpenDocument after edit: %v", err)
	}
	waitForMethods(t, logPath, map[string]int{"initialize": 1, "textDocument/didOpen": 1, "textDocument/didChange": 1})
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("manager Close: %v", err)
	}
	if manager.Running("go", root) {
		t.Fatal("server still marked running after shutdown")
	}
	shutdownMethods, _ := os.ReadFile(logPath)
	if countMethod(string(shutdownMethods), "shutdown") != 1 || countMethod(string(shutdownMethods), "exit") != 1 {
		t.Fatalf("server did not receive lifecycle shutdown: %q", shutdownMethods)
	}
}

func TestManager_RestartsCrashedServerAndRequestCancellation(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "requests.log")
	t.Setenv("Y_LSP_HELPER_LOG", logPath)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root, []ServerConfig{{Language: "go", Command: executable, Args: []string{"-test.run=^TestLSPHelperServer$"}}}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, _, hit, err := manager.GetClient(ctx, "go", root)
	if err != nil || hit {
		t.Fatalf("initial GetClient = %v, cache %t", err, hit)
	}
	slowCtx, slowCancel := context.WithTimeout(ctx, 10*time.Millisecond)
	_, err = client.Request(slowCtx, "slow", nil)
	slowCancel()
	if err != context.DeadlineExceeded {
		t.Fatalf("slow request error = %v, want deadline exceeded", err)
	}
	key := serverKey(root, "go")
	manager.mu.Lock()
	server := manager.servers[key]
	manager.mu.Unlock()
	if server == nil {
		t.Fatal("expected running server")
	}
	_ = server.cmd.Process.Kill()
	select {
	case <-server.processDone:
	case <-time.After(2 * time.Second):
		t.Fatal("crashed server did not exit")
	}
	_, name, hit, err := manager.GetClient(ctx, "go", root)
	if err != nil || hit || name != executable {
		t.Fatalf("restarted GetClient = (%q, cache %t), err %v", name, hit, err)
	}
	methods, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if countMethod(string(methods), "initialize") != 2 {
		t.Fatalf("expected initial and restarted initialization, got %q", methods)
	}
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("manager Close: %v", err)
	}
}

func readHelperMessage(reader *bufio.Reader) (Message, error) {
	length := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return Message{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return Message{}, err
			}
		}
	}
	if length < 0 || length > maxMessageBytes {
		return Message{}, fmt.Errorf("invalid helper message length %d", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return Message{}, err
	}
	var message Message
	err := json.Unmarshal(body, &message)
	return message, err
}

func flagValue(name string) string {
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-"+name+"=") {
			return strings.TrimPrefix(arg, "-"+name+"=")
		}
	}
	return ""
}

func countMethod(contents, method string) int {
	count := 0
	for _, line := range strings.Split(contents, "\n") {
		if line == method {
			count++
		}
	}
	return count
}

func waitForMethods(t *testing.T, path string, expected map[string]int) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil {
			ready := true
			for method, count := range expected {
				if countMethod(string(contents), method) < count {
					ready = false
					break
				}
			}
			if ready {
				return string(contents)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	contents, _ := os.ReadFile(path)
	t.Fatalf("server did not log expected requests %v; got %q", expected, contents)
	return ""
}

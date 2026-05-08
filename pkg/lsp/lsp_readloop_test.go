//go:build feature_lsp

package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"
)

func writeLSPMessage(w io.Writer, msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

func TestReadLoopReceivesResponse(t *testing.T) {
	// serverReads <- clientWrites  (client request to server)
	// clientReads <- serverWrites  (server response to client)
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	defer clientWrites.Close()
	defer serverReads.Close()
	defer serverWrites.Close()
	defer clientReads.Close()

	client := NewClient(clientWrites, clientReads)

	go func() {
		// Drain the request so the pipe doesn't block.
		go io.Copy(io.Discard, serverReads)
		// Small delay so request is registered first.
		time.Sleep(50 * time.Millisecond)

		resp := Message{
			JSONRPC: "2.0",
			ID:      intPtr(1),
			Result:  json.RawMessage(`{"capabilities":{}}`),
		}
		_ = writeLSPMessage(serverWrites, resp)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := client.Initialize(ctx, "file:///test")
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if string(result) != `{"capabilities":{}}` {
		t.Fatalf("result = %q, want capabilities", string(result))
	}
}

func TestReadLoopReceivesErrorResponse(t *testing.T) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	defer clientWrites.Close()
	defer serverReads.Close()
	defer serverWrites.Close()
	defer clientReads.Close()

	client := NewClient(clientWrites, clientReads)

	go func() {
		go io.Copy(io.Discard, serverReads)
		time.Sleep(50 * time.Millisecond)

		resp := Message{
			JSONRPC: "2.0",
			ID:      intPtr(1),
			Error:   &ResponseError{Code: -32600, Message: "invalid request"},
		}
		_ = writeLSPMessage(serverWrites, resp)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := client.Initialize(ctx, "file:///test")
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "lsp error -32600: invalid request" {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestReadLoopIgnoresNotifications(t *testing.T) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	defer clientWrites.Close()
	defer serverReads.Close()
	defer serverWrites.Close()
	defer clientReads.Close()

	client := NewClient(clientWrites, clientReads)

	go func() {
		go io.Copy(io.Discard, serverReads)
		time.Sleep(50 * time.Millisecond)

		notif := Message{
			JSONRPC: "2.0",
			Method:  "textDocument/publishDiagnostics",
			Params:  json.RawMessage(`{}`),
		}
		_ = writeLSPMessage(serverWrites, notif)

		resp := Message{
			JSONRPC: "2.0",
			ID:      intPtr(1),
			Result:  json.RawMessage(`{}`),
		}
		_ = writeLSPMessage(serverWrites, resp)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := client.Initialize(ctx, "file:///test")
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
}

func TestReadLoopHandlesMultipleResponses(t *testing.T) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	defer clientWrites.Close()
	defer serverReads.Close()
	defer serverWrites.Close()
	defer clientReads.Close()

	client := NewClient(clientWrites, clientReads)

	// Server waits for a signal before sending each response.
	trigger := make(chan int, 3)
	go func() {
		go io.Copy(io.Discard, serverReads)
		for i := 1; i <= 3; i++ {
			<-trigger
			resp := Message{
				JSONRPC: "2.0",
				ID:      intPtr(i),
				Result:  json.RawMessage(fmt.Sprintf(`{"id":%d}`, i)),
			}
			_ = writeLSPMessage(serverWrites, resp)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for i := 1; i <= 3; i++ {
		trigger <- i
		result, err := client.Initialize(ctx, fmt.Sprintf("file:///test%d", i))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		want := fmt.Sprintf(`{"id":%d}`, i)
		if string(result) != want {
			t.Fatalf("request %d result = %q, want %q", i, string(result), want)
		}
	}
}

func TestReadLoopHandlesExtraHeaders(t *testing.T) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	defer clientWrites.Close()
	defer serverReads.Close()
	defer serverWrites.Close()
	defer clientReads.Close()

	client := NewClient(clientWrites, clientReads)

	go func() {
		go io.Copy(io.Discard, serverReads)
		time.Sleep(50 * time.Millisecond)

		body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
		fmt.Fprint(serverWrites, "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n")
		fmt.Fprintf(serverWrites, "Content-Length: %d\r\n\r\n%s", len(body), body)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := client.Initialize(ctx, "file:///test")
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if !bytes.Contains(result, []byte("ok")) {
		t.Fatalf("result = %q, want ok", string(result))
	}
}

func TestReadLoopNilStdout(t *testing.T) {
	client := NewClient(nil, nil)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func intPtr(n int) *int {
	return &n
}

func TestNewClientWithPipe(t *testing.T) {
	_, clientWrites := io.Pipe()
	clientReads, _ := io.Pipe()
	defer clientWrites.Close()
	defer clientReads.Close()

	client := NewClient(clientWrites, clientReads)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.stdin != clientWrites {
		t.Fatal("expected stdin to be clientWrites")
	}
	if client.stdout != clientReads {
		t.Fatal("expected stdout to be clientReads")
	}
}

func TestRequestContextCancel(t *testing.T) {
	serverReads, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	defer clientWrites.Close()
	defer serverReads.Close()
	defer serverWrites.Close()
	defer clientReads.Close()

	// Drain writes so the request doesn't block on pipe buffer.
	go io.Copy(io.Discard, serverReads)

	client := NewClient(clientWrites, clientReads)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Initialize(ctx, "file:///test")
	if err == nil {
		t.Fatal("expected context canceled error")
	}
}

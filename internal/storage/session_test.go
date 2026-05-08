package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuri/y/pkg/ai"
)

func TestDefaultPathsRespectAgentDirOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv(agentDirEnv, filepath.Join(root, "custom"))

	if got, want := DefaultAgentDir(), filepath.Join(root, "custom"); got != want {
		t.Fatalf("DefaultAgentDir() = %q, want %q", got, want)
	}
	if got, want := DefaultConfigPath(), filepath.Join(root, "custom", "config.toml"); got != want {
		t.Fatalf("DefaultConfigPath() = %q, want %q", got, want)
	}
	if got, want := DefaultAuthPath(), filepath.Join(root, "custom", "auth.json"); got != want {
		t.Fatalf("DefaultAuthPath() = %q, want %q", got, want)
	}
	if got := DefaultSessionDir("/workspace/project"); !strings.Contains(got, "--workspace-project--") {
		t.Fatalf("DefaultSessionDir() = %q, want encoded cwd", got)
	}
}

func TestEncodeWorkdir(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/a/b/c", "--a-b-c--"},
		{".", "--.--"},
		{"", "--.--"},
		{"/very/long/path/to/project", "--very-long-path-to-project--"},
	}
	for _, tc := range tests {
		got := encodeWorkdir(tc.input)
		if got != tc.want {
			t.Fatalf("encodeWorkdir(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSessionStoreSaveListAndResolve(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(root)
	cwd := "/workspace/project"
	messages := []ai.Message{
		{
			Role:      ai.RoleUser,
			Timestamp: time.Unix(1, 0).UTC(),
			Content:   []ai.ContentBlock{{Type: ai.ContentText, Text: "hello"}},
		},
		{
			Role:      ai.RoleAssistant,
			Timestamp: time.Unix(2, 0).UTC(),
			Content:   []ai.ContentBlock{{Type: ai.ContentText, Text: "world"}},
		},
	}

	summary, err := store.SaveTranscript(context.Background(), cwd, messages, 1<<20)
	if err != nil {
		t.Fatalf("SaveTranscript returned error: %v", err)
	}
	if summary.ID == "" || summary.Path == "" {
		t.Fatalf("summary = %#v, want populated path and ID", summary)
	}
	if summary.MessageCount != 2 {
		t.Fatalf("summary.MessageCount = %d, want 2", summary.MessageCount)
	}

	summaries, err := store.List(context.Background(), cwd)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("List returned %d sessions, want 1", len(summaries))
	}
	if summaries[0].Path != summary.Path {
		t.Fatalf("List returned path %q, want %q", summaries[0].Path, summary.Path)
	}

	resolved, err := store.Resolve(context.Background(), cwd, summary.ID[:6])
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved != summary.Path {
		t.Fatalf("Resolve returned %q, want %q", resolved, summary.Path)
	}
}

func TestSessionStoreTruncatesOldEntriesToFitLimit(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(root)
	cwd := "/workspace/project"
	messages := make([]ai.Message, 0, 4)
	for i := 0; i < 4; i++ {
		messages = append(messages, ai.Message{
			Role:      ai.RoleUser,
			Timestamp: time.Unix(int64(i+1), 0).UTC(),
			Content:   []ai.ContentBlock{{Type: ai.ContentText, Text: strings.Repeat("x", 256)}},
		})
	}

	summary, err := store.SaveTranscript(context.Background(), cwd, messages, 700)
	if err != nil {
		t.Fatalf("SaveTranscript returned error: %v", err)
	}
	if !summary.Truncated {
		t.Fatalf("summary.Truncated = false, want true")
	}
	if summary.ByteSize > 700 {
		t.Fatalf("summary.ByteSize = %d, want <= 700", summary.ByteSize)
	}

	data, err := os.ReadFile(summary.Path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !bytes.Contains(data, []byte(`"type":"truncation"`)) {
		t.Fatalf("session file missing truncation marker:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`"role":"user"`)) {
		t.Fatalf("session file missing retained transcript messages:\n%s", data)
	}

	var header SessionHeader
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if err := json.Unmarshal(lines[0], &header); err != nil {
		t.Fatalf("header JSON invalid: %v", err)
	}
	if header.Type != "session" || header.ID == "" {
		t.Fatalf("header = %#v, want valid session header", header)
	}
}

func TestSessionStoreListEmptyDir(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(root)
	summaries, err := store.List(context.Background(), "/nonexistent/cwd")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if summaries != nil {
		t.Fatalf("List returned %v, want nil", summaries)
	}
}

func TestSessionStoreLatest(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(root)
	cwd := "/workspace/project"
	msg := []ai.Message{{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "x"}}}}

	// Save first session.
	_, err := store.SaveTranscript(context.Background(), cwd, msg, 1<<20)
	if err != nil {
		t.Fatalf("SaveTranscript: %v", err)
	}

	// Save second session after a small delay.
	time.Sleep(10 * time.Millisecond)
	s2, err := store.SaveTranscript(context.Background(), cwd, msg, 1<<20)
	if err != nil {
		t.Fatalf("SaveTranscript: %v", err)
	}

	latest, err := store.Latest(context.Background(), cwd)
	if err != nil {
		t.Fatalf("Latest returned error: %v", err)
	}
	if latest == nil {
		t.Fatal("Latest returned nil")
	}
	if latest.Path != s2.Path {
		t.Fatalf("Latest.Path = %q, want %q", latest.Path, s2.Path)
	}

	// Latest on empty dir should return nil.
	emptyLatest, err := store.Latest(context.Background(), "/other")
	if err != nil {
		t.Fatalf("Latest returned error: %v", err)
	}
	if emptyLatest != nil {
		t.Fatalf("Latest on empty dir = %v, want nil", emptyLatest)
	}
}

func TestSessionStoreResolveEmptyTarget(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(root)
	cwd := "/workspace/project"
	msg := []ai.Message{{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "x"}}}}

	summary, err := store.SaveTranscript(context.Background(), cwd, msg, 1<<20)
	if err != nil {
		t.Fatalf("SaveTranscript: %v", err)
	}

	resolved, err := store.Resolve(context.Background(), cwd, "")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved != summary.Path {
		t.Fatalf("Resolve(empty) = %q, want %q", resolved, summary.Path)
	}
}

func TestSessionStoreResolveAbsolutePath(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(root)
	cwd := "/workspace/project"
	msg := []ai.Message{{Role: ai.RoleUser, Content: []ai.ContentBlock{{Type: ai.ContentText, Text: "x"}}}}

	summary, err := store.SaveTranscript(context.Background(), cwd, msg, 1<<20)
	if err != nil {
		t.Fatalf("SaveTranscript: %v", err)
	}

	resolved, err := store.Resolve(context.Background(), cwd, summary.Path)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved != summary.Path {
		t.Fatalf("Resolve(abs) = %q, want %q", resolved, summary.Path)
	}
}

func TestSessionStoreResolveNotFound(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(root)
	_, err := store.Resolve(context.Background(), "/workspace", "nonexistent")
	if err == nil {
		t.Fatal("Resolve returned nil error for missing target")
	}
	if !os.IsNotExist(err) {
		t.Fatalf("error = %v, want os.ErrNotExist", err)
	}
}

func TestSessionStoreSaveWithToolCallsAndResult(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(root)
	cwd := "/workspace/project"
	messages := []ai.Message{
		{
			Role: ai.RoleAssistant,
			ToolCalls: []ai.ToolCall{
				{ID: "call_1", Name: "read_file", Arguments: json.RawMessage(`{"path":"/tmp/x"}`)},
			},
		},
		{
			Role: ai.RoleToolResult,
			ToolResult: &ai.ToolResult{
				ToolCallID: "call_1",
				ToolName:   "read_file",
				Content:    []ai.ContentBlock{{Type: ai.ContentText, Text: "content"}},
				IsError:    true,
			},
		},
	}

	summary, err := store.SaveTranscript(context.Background(), cwd, messages, 1<<20)
	if err != nil {
		t.Fatalf("SaveTranscript returned error: %v", err)
	}
	if summary.MessageCount != 2 {
		t.Fatalf("MessageCount = %d, want 2", summary.MessageCount)
	}

	data, err := os.ReadFile(summary.Path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Contains(data, []byte(`"tool_calls"`)) {
		t.Fatal("session file missing tool_calls")
	}
	if !bytes.Contains(data, []byte(`"tool_result"`)) {
		t.Fatal("session file missing tool_result")
	}
	if !bytes.Contains(data, []byte(`"is_error":true`)) {
		t.Fatal("session file missing is_error")
	}
}

func TestSessionStoreSaveEmptyMessages(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(root)
	cwd := "/workspace/project"

	summary, err := store.SaveTranscript(context.Background(), cwd, nil, 1<<20)
	if err != nil {
		t.Fatalf("SaveTranscript returned error: %v", err)
	}
	if summary.MessageCount != 0 {
		t.Fatalf("MessageCount = %d, want 0", summary.MessageCount)
	}
	if summary.Truncated {
		t.Fatal("Truncated = true for empty messages")
	}
}

func TestSessionStoreContextCancel(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.SaveTranscript(ctx, "/workspace", nil, 1<<20)
	if err == nil {
		t.Fatal("SaveTranscript returned nil error for cancelled context")
	}

	_, err = store.List(ctx, "/workspace")
	if err == nil {
		t.Fatal("List returned nil error for cancelled context")
	}
}

func TestSessionStoreSessionDir(t *testing.T) {
	root := t.TempDir()
	store := NewSessionStore(root)
	dir := store.SessionDir("/a/b")
	if !strings.Contains(dir, root) {
		t.Fatalf("SessionDir = %q, want containing root %q", dir, root)
	}
	if !strings.Contains(dir, "sessions") {
		t.Fatalf("SessionDir = %q, want containing 'sessions'", dir)
	}
}

func TestSessionStoreSessionDirNil(t *testing.T) {
	var store *SessionStore
	dir := store.SessionDir("/a/b")
	want := DefaultSessionDir("/a/b")
	if dir != want {
		t.Fatalf("SessionDir(nil) = %q, want %q", dir, want)
	}
}

func TestNewSessionIDUnique(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := newSessionID()
		if ids[id] {
			t.Fatalf("duplicate session ID: %q", id)
		}
		ids[id] = true
	}
}

func TestSessionFileName(t *testing.T) {
	ts := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	name := sessionFileName(ts, "abc123")
	want := "20260115T103000Z_abc123.jsonl"
	if name != want {
		t.Fatalf("sessionFileName = %q, want %q", name, want)
	}
}

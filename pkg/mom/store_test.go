package mom

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewChannelStoreRequiresDir(t *testing.T) {
	if _, err := NewChannelStore(StoreConfig{}); err == nil {
		t.Fatal("expected error when working dir is empty")
	}
}

func TestChannelStoreLogMessageDeduplicates(t *testing.T) {
	dir := t.TempDir()
	clk := &FakeClock{Current: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
	store, err := NewChannelStore(StoreConfig{WorkingDir: dir, Clock: clk, DedupeWindow: time.Minute})
	if err != nil {
		t.Fatalf("NewChannelStore: %v", err)
	}

	msg := LoggedMessage{TS: "1.0", User: "U1", Text: "hi"}
	ok, err := store.LogMessage("C1", msg)
	if err != nil {
		t.Fatalf("LogMessage: %v", err)
	}
	if !ok {
		t.Fatal("expected first log to succeed")
	}

	dup, err := store.LogMessage("C1", msg)
	if err != nil {
		t.Fatalf("LogMessage dup: %v", err)
	}
	if dup {
		t.Fatal("expected duplicate within window to be skipped")
	}

	clk.Advance(2 * time.Minute)
	ok, err = store.LogMessage("C1", msg)
	if err != nil {
		t.Fatalf("LogMessage after window: %v", err)
	}
	if !ok {
		t.Fatal("expected message to be logged after dedupe window expires")
	}

	logPath := filepath.Join(dir, "C1", "log.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	count := 0
	for scanner.Scan() {
		var m LoggedMessage
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if m.Date == "" {
			t.Fatal("expected Date to be populated")
		}
		count++
	}
	if count != 2 {
		t.Fatalf("expected 2 logged lines, got %d", count)
	}
}

func TestChannelStoreLastTimestamp(t *testing.T) {
	dir := t.TempDir()
	store, err := NewChannelStore(StoreConfig{WorkingDir: dir, Clock: &FakeClock{}})
	if err != nil {
		t.Fatalf("NewChannelStore: %v", err)
	}
	if got := store.LastTimestamp("C1"); got != "" {
		t.Fatalf("LastTimestamp empty channel = %q, want empty", got)
	}
	for i, ts := range []string{"1.000000", "2.000000", "3.000000"} {
		if _, err := store.LogMessage("C1", LoggedMessage{TS: ts, User: "U1", Text: "hi"}); err != nil {
			t.Fatalf("LogMessage[%d]: %v", i, err)
		}
	}
	if got := store.LastTimestamp("C1"); got != "3.000000" {
		t.Fatalf("LastTimestamp = %q", got)
	}
}

func TestChannelStoreProcessAttachmentsDownloads(t *testing.T) {
	dir := t.TempDir()
	dl := &FakeDownloader{Files: map[string][]byte{"https://example/file.png": []byte("png-bytes")}}
	store, err := NewChannelStore(StoreConfig{WorkingDir: dir, Downloader: dl, Clock: &FakeClock{}})
	if err != nil {
		t.Fatalf("NewChannelStore: %v", err)
	}
	att := store.ProcessAttachments("C1", []SlackFile{{Name: "file.png", URLDownload: "https://example/file.png"}}, "1.500000")
	if len(att) != 1 {
		t.Fatalf("ProcessAttachments returned %d items", len(att))
	}
	if att[0].Original != "file.png" {
		t.Fatalf("attachment original = %q", att[0].Original)
	}
	if got, want := att[0].Local, "C1/attachments/1500_file.png"; got != want {
		t.Fatalf("attachment local = %q, want %q", got, want)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := store.AwaitDownloads(ctx); err != nil {
		t.Fatalf("AwaitDownloads: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "C1", "attachments", "1500_file.png"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(body) != "png-bytes" {
		t.Fatalf("downloaded contents = %q", body)
	}
	if len(dl.Calls) != 1 {
		t.Fatalf("expected 1 download call, got %d", len(dl.Calls))
	}
}

func TestLocalAttachmentNameDeterministic(t *testing.T) {
	got := LocalAttachmentName("file name.txt", "1700000000.000000")
	want := "1700000000000_file_name.txt"
	if got != want {
		t.Fatalf("LocalAttachmentName = %q, want %q", got, want)
	}
}

func TestLogBotResponseAddsBotEntry(t *testing.T) {
	dir := t.TempDir()
	clk := &FakeClock{Current: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}
	store, err := NewChannelStore(StoreConfig{WorkingDir: dir, Clock: clk})
	if err != nil {
		t.Fatalf("NewChannelStore: %v", err)
	}
	if err := store.LogBotResponse("C1", "ok", "10"); err != nil {
		t.Fatalf("LogBotResponse: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "C1", "log.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var msg LoggedMessage
	if err := json.Unmarshal(bytes.TrimRight(data, "\n"), &msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.User != "bot" || !msg.IsBot || msg.Text != "ok" {
		t.Fatalf("unexpected entry: %#v", msg)
	}
	if msg.TS != "10" {
		t.Fatalf("TS = %q", msg.TS)
	}
}

func TestLooksLikeStop(t *testing.T) {
	cases := map[string]bool{
		"stop":        true,
		" STOP ":      true,
		"stop please": false,
		"":            false,
		"STOPS":       false,
	}
	for input, expected := range cases {
		if got := LooksLikeStop(input); got != expected {
			t.Errorf("LooksLikeStop(%q) = %v, want %v", input, got, expected)
		}
	}
}

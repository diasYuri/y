package storage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuri/y/pkg/ai"
)

const sessionFormatVersion = 1

// SessionHeader is the first JSONL line in every session file.
type SessionHeader struct {
	Type      string    `json:"type"`
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	CWD       string    `json:"cwd"`
	Timestamp time.Time `json:"timestamp"`
}

// SessionMessageEntry stores one transcript message.
type SessionMessageEntry struct {
	Type      string         `json:"type"`
	ID        string         `json:"id"`
	Timestamp time.Time      `json:"timestamp"`
	Message   SessionMessage `json:"message"`
}

// SessionTruncationEntry records when old transcript entries were dropped.
type SessionTruncationEntry struct {
	Type           string `json:"type"`
	DroppedEntries int    `json:"dropped_entries"`
	DroppedBytes   int64  `json:"dropped_bytes"`
	MaxBytes       int64  `json:"max_bytes"`
	FirstKeptID    string `json:"first_kept_id,omitempty"`
}

// SessionMessage is the JSONL payload used for transcript messages.
type SessionMessage struct {
	Role       string                `json:"role"`
	Content    []SessionContentBlock `json:"content,omitempty"`
	ToolCalls  []SessionToolCall     `json:"tool_calls,omitempty"`
	ToolResult *SessionToolResult    `json:"tool_result,omitempty"`
	Timestamp  time.Time             `json:"timestamp"`
}

// SessionContentBlock is a JSONL payload for normalized content blocks.
type SessionContentBlock struct {
	Type             string          `json:"type"`
	Text             string          `json:"text,omitempty"`
	Thinking         string          `json:"thinking,omitempty"`
	ThinkingRedacted bool            `json:"thinking_redacted,omitempty"`
	Signature        string          `json:"signature,omitempty"`
	ImageData        []byte          `json:"image_data,omitempty"`
	ImageMIMEType    string          `json:"image_mime_type,omitempty"`
	ProviderMetadata json.RawMessage `json:"provider_metadata,omitempty"`
}

// SessionToolCall is the JSONL payload for a tool call.
type SessionToolCall struct {
	ID               string          `json:"id,omitempty"`
	Name             string          `json:"name"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
}

// SessionToolResult is the JSONL payload for a tool result.
type SessionToolResult struct {
	ToolCallID string                `json:"tool_call_id,omitempty"`
	ToolName   string                `json:"tool_name,omitempty"`
	Content    []SessionContentBlock `json:"content,omitempty"`
	IsError    bool                  `json:"is_error,omitempty"`
	Details    json.RawMessage       `json:"details,omitempty"`
}

// SessionSummary describes one stored transcript.
type SessionSummary struct {
	Path         string
	ID           string
	CWD          string
	Created      time.Time
	Modified     time.Time
	MessageCount int
	ByteSize     int64
	Truncated    bool
}

// SessionStore reads and writes session JSONL files.
type SessionStore struct {
	agentDir string
	now      func() time.Time
}

// NewSessionStore creates a store rooted at agentDir.
func NewSessionStore(agentDir string) *SessionStore {
	if agentDir == "" {
		agentDir = DefaultAgentDir()
	}
	return &SessionStore{
		agentDir: agentDir,
		now:      time.Now,
	}
}

// SessionDir returns the cwd-specific directory for session files.
func (s *SessionStore) SessionDir(cwd string) string {
	if s == nil || s.agentDir == "" {
		return DefaultSessionDir(cwd)
	}
	return filepath.Join(s.agentDir, "sessions", encodeWorkdir(cwd))
}

// List returns summaries for all valid session files in the cwd-specific dir.
func (s *SessionStore) List(ctx context.Context, cwd string) ([]SessionSummary, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	dir := s.SessionDir(cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	summaries := make([]SessionSummary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		summary, err := s.readSummary(path, entry)
		if err != nil {
			continue
		}
		summaries = append(summaries, summary)
	}

	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Modified.Equal(summaries[j].Modified) {
			return summaries[i].Path > summaries[j].Path
		}
		return summaries[i].Modified.After(summaries[j].Modified)
	})
	return summaries, nil
}

// Latest returns the most recently modified session in the cwd-specific dir.
func (s *SessionStore) Latest(ctx context.Context, cwd string) (*SessionSummary, error) {
	summaries, err := s.List(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if len(summaries) == 0 {
		return nil, nil
	}
	return &summaries[0], nil
}

// Resolve looks up a session file by exact path, basename, or ID prefix.
func (s *SessionStore) Resolve(ctx context.Context, cwd, target string) (string, error) {
	if target == "" {
		latest, err := s.Latest(ctx, cwd)
		if err != nil {
			return "", err
		}
		if latest == nil {
			return "", os.ErrNotExist
		}
		return latest.Path, nil
	}

	if filepath.IsAbs(target) {
		if _, statErr := os.Stat(target); statErr == nil {
			return target, nil
		}
		return "", os.ErrNotExist
	}

	if strings.ContainsRune(target, os.PathSeparator) {
		candidate := filepath.Clean(target)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
		return "", os.ErrNotExist
	}

	summaries, err := s.List(ctx, cwd)
	if err != nil {
		return "", err
	}
	var matched []SessionSummary
	for _, summary := range summaries {
		base := filepath.Base(summary.Path)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		switch {
		case summary.ID == target,
			strings.HasPrefix(summary.ID, target),
			name == target,
			strings.HasPrefix(name, target):
			matched = append(matched, summary)
		}
	}
	if len(matched) == 0 {
		return "", os.ErrNotExist
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].Modified.Equal(matched[j].Modified) {
			return matched[i].Path > matched[j].Path
		}
		return matched[i].Modified.After(matched[j].Modified)
	})
	return matched[0].Path, nil
}

// SaveTranscript writes a transcript as JSONL, truncating the oldest entries if needed.
func (s *SessionStore) SaveTranscript(ctx context.Context, cwd string, messages []ai.Message, maxBytes int64) (SessionSummary, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return SessionSummary{}, err
		}
	}

	dir := s.SessionDir(cwd)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return SessionSummary{}, err
	}

	now := s.now
	if now == nil {
		now = time.Now
	}
	sessionTime := now().UTC()
	header := SessionHeader{
		Type:      "session",
		Version:   sessionFormatVersion,
		ID:        newSessionID(),
		CWD:       cwd,
		Timestamp: sessionTime,
	}

	headerLine, err := encodeJSONLine(header)
	if err != nil {
		return SessionSummary{}, err
	}

	encodedMessages := make([][]byte, len(messages))
	messageIDs := make([]string, len(messages))
	for i, message := range messages {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return SessionSummary{}, err
			}
		}
		messageEntry := SessionMessageEntry{
			Type:      "message",
			ID:        newSessionID(),
			Timestamp: message.Timestamp,
			Message:   toSessionMessage(message),
		}
		if messageEntry.Timestamp.IsZero() {
			messageEntry.Timestamp = sessionTime
		}
		messageIDs[i] = messageEntry.ID
		encoded, err := encodeJSONLine(messageEntry)
		if err != nil {
			return SessionSummary{}, err
		}
		encodedMessages[i] = encoded
	}

	truncation := SessionTruncationEntry{Type: "truncation", MaxBytes: maxBytes}
	selectedStart := 0
	willTruncate := false
	if maxBytes > 0 {
		totalBytes := len(headerLine)
		for _, line := range encodedMessages {
			totalBytes += len(line)
		}
		if int64(totalBytes) > maxBytes {
			willTruncate = true
			foundFit := false
			for start := 0; start < len(encodedMessages); start++ {
				truncation.DroppedEntries = start
				truncation.FirstKeptID = ""
				if start < len(messageIDs) {
					truncation.FirstKeptID = messageIDs[start]
				}
				droppedBytes := 0
				for i := 0; i < start; i++ {
					droppedBytes += len(encodedMessages[i])
				}
				truncation.DroppedBytes = int64(droppedBytes)
				truncationLine, err := encodeJSONLine(truncation)
				if err != nil {
					return SessionSummary{}, err
				}
				size := len(headerLine) + len(truncationLine)
				for i := start; i < len(encodedMessages); i++ {
					size += len(encodedMessages[i])
				}
				if int64(size) <= maxBytes {
					selectedStart = start
					foundFit = true
					break
				}
			}
			if !foundFit {
				selectedStart = len(encodedMessages)
				truncation.DroppedEntries = len(encodedMessages)
				truncation.DroppedBytes = int64(totalBytes - len(headerLine))
				truncation.FirstKeptID = ""
				truncationLine, err := encodeJSONLine(truncation)
				if err != nil {
					return SessionSummary{}, err
				}
				if int64(len(headerLine)+len(truncationLine)) > maxBytes {
					return SessionSummary{}, fmt.Errorf("session header exceeds max_session_bytes")
				}
			}
		}
	}

	finalBytes := make([]byte, 0, len(headerLine)+1)
	finalBytes = append(finalBytes, headerLine...)
	finalBytes = append(finalBytes, '\n')

	messageCount := len(encodedMessages[selectedStart:])
	if willTruncate {
		truncation.DroppedEntries = selectedStart
		droppedBytes := 0
		for i := 0; i < selectedStart; i++ {
			droppedBytes += len(encodedMessages[i])
		}
		truncation.DroppedBytes = int64(droppedBytes)
		if selectedStart < len(messageIDs) {
			truncation.FirstKeptID = messageIDs[selectedStart]
		}
		truncationLine, err := encodeJSONLine(truncation)
		if err != nil {
			return SessionSummary{}, err
		}
		finalBytes = append(finalBytes, truncationLine...)
		finalBytes = append(finalBytes, '\n')
	}
	for i := selectedStart; i < len(encodedMessages); i++ {
		finalBytes = append(finalBytes, encodedMessages[i]...)
		finalBytes = append(finalBytes, '\n')
	}

	if err := atomicWriteFile(filepath.Join(dir, sessionFileName(sessionTime, header.ID)), finalBytes, 0o600); err != nil {
		return SessionSummary{}, err
	}

	summary := SessionSummary{
		Path:         filepath.Join(dir, sessionFileName(sessionTime, header.ID)),
		ID:           header.ID,
		CWD:          cwd,
		Created:      sessionTime,
		Modified:     sessionTime,
		MessageCount: messageCount,
		ByteSize:     int64(len(finalBytes)),
		Truncated:    willTruncate,
	}
	return summary, nil
}

func (s *SessionStore) readSummary(path string, entry os.DirEntry) (SessionSummary, error) {
	file, err := os.Open(path)
	if err != nil {
		return SessionSummary{}, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	headerLine, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return SessionSummary{}, err
	}
	headerLine = bytes.TrimSpace(headerLine)
	if len(headerLine) == 0 {
		return SessionSummary{}, errors.New("empty session file")
	}

	var header SessionHeader
	if err := json.Unmarshal(headerLine, &header); err != nil {
		return SessionSummary{}, err
	}
	if header.Type != "session" || header.ID == "" {
		return SessionSummary{}, errors.New("invalid session header")
	}

	summary := SessionSummary{
		Path:     path,
		ID:       header.ID,
		CWD:      header.CWD,
		Created:  header.Timestamp,
		Modified: time.Time{},
	}
	if info, err := entry.Info(); err == nil {
		summary.Modified = info.ModTime()
		summary.ByteSize = info.Size()
	}

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSpace(line)
			if len(line) > 0 {
				var meta struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(line, &meta) == nil {
					switch meta.Type {
					case "message":
						summary.MessageCount++
					case "truncation":
						summary.Truncated = true
					}
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return SessionSummary{}, err
		}
	}

	return summary, nil
}

func encodeJSONLine(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr == nil {
			if retryErr := os.Rename(tmpPath, path); retryErr == nil {
				return nil
			}
		}
		return err
	}
	return nil
}

func newSessionID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("%d", time.Now().UTC().UnixNano())
}

func sessionFileName(ts time.Time, id string) string {
	return ts.UTC().Format("20060102T150405Z") + "_" + id + ".jsonl"
}

func toSessionMessage(message ai.Message) SessionMessage {
	out := SessionMessage{
		Role:      string(message.Role),
		Timestamp: message.Timestamp,
	}
	if len(message.Content) > 0 {
		out.Content = make([]SessionContentBlock, len(message.Content))
		for i, block := range message.Content {
			out.Content[i] = SessionContentBlock{
				Type:             string(block.Type),
				Text:             block.Text,
				Thinking:         block.Thinking,
				ThinkingRedacted: block.ThinkingRedacted,
				Signature:        block.Signature,
				ImageData:        append([]byte(nil), block.ImageData...),
				ImageMIMEType:    block.ImageMIMEType,
				ProviderMetadata: append(json.RawMessage(nil), block.ProviderMetadata...),
			}
		}
	}
	if len(message.ToolCalls) > 0 {
		out.ToolCalls = make([]SessionToolCall, len(message.ToolCalls))
		for i, call := range message.ToolCalls {
			out.ToolCalls[i] = SessionToolCall{
				ID:               call.ID,
				Name:             call.Name,
				Arguments:        append(json.RawMessage(nil), call.Arguments...),
				ThoughtSignature: call.ThoughtSignature,
			}
		}
	}
	if message.ToolResult != nil {
		result := SessionToolResult{
			ToolCallID: message.ToolResult.ToolCallID,
			ToolName:   message.ToolResult.ToolName,
			IsError:    message.ToolResult.IsError,
			Details:    append(json.RawMessage(nil), message.ToolResult.Details...),
		}
		if len(message.ToolResult.Content) > 0 {
			result.Content = make([]SessionContentBlock, len(message.ToolResult.Content))
			for i, block := range message.ToolResult.Content {
				result.Content[i] = SessionContentBlock{
					Type:             string(block.Type),
					Text:             block.Text,
					Thinking:         block.Thinking,
					ThinkingRedacted: block.ThinkingRedacted,
					Signature:        block.Signature,
					ImageData:        append([]byte(nil), block.ImageData...),
					ImageMIMEType:    block.ImageMIMEType,
					ProviderMetadata: append(json.RawMessage(nil), block.ProviderMetadata...),
				}
			}
		}
		out.ToolResult = &result
	}
	return out
}

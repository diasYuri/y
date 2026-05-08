// Package coding provides the coding agent framework for AI-assisted code editing.
// It is the Go equivalent of pi-mono's coding-agent package.
package coding

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yuri/y/pkg/ai"
)

// Mode identifies a coding agent strategy (e.g., refactor, explain, test).
type Mode string

const (
	ModeRefactor  Mode = "refactor"
	ModeExplain   Mode = "explain"
	ModeTest      Mode = "test"
	ModeReview    Mode = "review"
	ModeDocument  Mode = "document"
	ModeFix       Mode = "fix"
	ModeImplement Mode = "implement"
)

// Session represents a single coding agent interaction.
type Session struct {
	ID        string
	Mode      Mode
	Prompt    string
	Context   Context
	StartedAt time.Time
	EndedAt   *time.Time
	Result    *Result
}

// Context carries the file and repository context for a coding session.
type Context struct {
	WorkspaceRoot string
	Files         []FileContext
	Selection     *Selection
}

// FileContext describes a file relevant to the coding session.
type FileContext struct {
	Path     string
	Content  string
	Language string
	Offset   int
	Limit    int
}

// Selection describes a selected region within a file.
type Selection struct {
	Path  string
	Start Position
	End   Position
	Text  string
}

// Position is a line/column pair.
type Position struct {
	Line   int
	Column int
}

// Result is the output of a coding session.
type Result struct {
	Edits    []Edit
	Summary  string
	Messages []ai.Message
	Usage    ai.Usage
}

// Edit is a proposed code change.
type Edit struct {
	Path    string
	OldText string
	NewText string
	Reason  string
}

// Runner executes coding sessions.
type Runner interface {
	Run(ctx context.Context, session Session) (Result, error)
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(context.Context, Session) (Result, error)

// Run calls f.
func (f RunnerFunc) Run(ctx context.Context, session Session) (Result, error) {
	return f(ctx, session)
}

// ErrNoEdits is returned when a coding session produces no changes.
var ErrNoEdits = errors.New("coding session produced no edits")

// ErrInvalidMode is returned when an unsupported mode is requested.
var ErrInvalidMode = errors.New("unsupported coding mode")

// NewSession creates a coding session with the given parameters.
func NewSession(mode Mode, prompt string, ctx Context) (Session, error) {
	if mode == "" {
		return Session{}, errors.New("mode is required")
	}
	if prompt == "" {
		return Session{}, errors.New("prompt is required")
	}
	return Session{
		ID:        generateID(),
		Mode:      mode,
		Prompt:    prompt,
		Context:   ctx,
		StartedAt: time.Now().UTC(),
	}, nil
}

// ValidateMode reports whether mode is supported.
func ValidateMode(mode Mode) bool {
	switch mode {
	case ModeRefactor, ModeExplain, ModeTest, ModeReview, ModeDocument, ModeFix, ModeImplement:
		return true
	}
	return false
}

// ApplyEdit applies a single edit to content, returning the updated content.
func ApplyEdit(content string, edit Edit) (string, error) {
	if edit.OldText == "" && content != "" {
		return "", errors.New("old_text must not be empty for non-empty content")
	}
	if edit.OldText == "" {
		return edit.NewText, nil
	}
	idx := 0
	count := 0
	for {
		pos := findAt(content, edit.OldText, idx)
		if pos < 0 {
			break
		}
		count++
		idx = pos + len(edit.OldText)
	}
	if count == 0 {
		return "", fmt.Errorf("old_text not found in %s", edit.Path)
	}
	if count > 1 {
		return "", fmt.Errorf("old_text is not unique in %s (%d occurrences)", edit.Path, count)
	}
	return replaceOnce(content, edit.OldText, edit.NewText), nil
}

func findAt(s, substr string, start int) int {
	if start >= len(s) {
		return -1
	}
	idx := 0
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			idx = i
			return idx
		}
	}
	return -1
}

func replaceOnce(s, old, new string) string {
	for i := 0; i <= len(s)-len(old); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}

var idCounter uint64

func generateID() string {
	idCounter++
	return fmt.Sprintf("coding-%d-%d", time.Now().Unix(), idCounter)
}

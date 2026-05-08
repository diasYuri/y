package coding

import (
	"context"
	"testing"
)

func TestValidateMode(t *testing.T) {
	valid := []Mode{ModeRefactor, ModeExplain, ModeTest, ModeReview, ModeDocument, ModeFix, ModeImplement}
	for _, mode := range valid {
		if !ValidateMode(mode) {
			t.Fatalf("expected %q to be valid", mode)
		}
	}
	if ValidateMode("unknown") {
		t.Fatal("expected 'unknown' to be invalid")
	}
	if ValidateMode("") {
		t.Fatal("expected empty mode to be invalid")
	}
}

func TestNewSession(t *testing.T) {
	ctx := Context{WorkspaceRoot: "/tmp"}
	s, err := NewSession(ModeRefactor, "rename variable", ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Mode != ModeRefactor {
		t.Fatalf("mode = %q, want refactor", s.Mode)
	}
	if s.Prompt != "rename variable" {
		t.Fatalf("prompt = %q, want 'rename variable'", s.Prompt)
	}
	if s.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if s.StartedAt.IsZero() {
		t.Fatal("expected non-zero started at")
	}
}

func TestNewSessionMissingMode(t *testing.T) {
	_, err := NewSession("", "prompt", Context{})
	if err == nil {
		t.Fatal("expected error for missing mode")
	}
}

func TestNewSessionMissingPrompt(t *testing.T) {
	_, err := NewSession(ModeFix, "", Context{})
	if err == nil {
		t.Fatal("expected error for missing prompt")
	}
}

func TestApplyEdit(t *testing.T) {
	edit := Edit{Path: "test.go", OldText: "foo", NewText: "bar"}
	got, err := ApplyEdit("hello foo world", edit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello bar world" {
		t.Fatalf("got %q, want 'hello bar world'", got)
	}
}

func TestApplyEditNotFound(t *testing.T) {
	edit := Edit{Path: "test.go", OldText: "missing", NewText: "x"}
	_, err := ApplyEdit("hello world", edit)
	if err == nil {
		t.Fatal("expected error for missing old text")
	}
}

func TestApplyEditNotUnique(t *testing.T) {
	edit := Edit{Path: "test.go", OldText: "foo", NewText: "bar"}
	_, err := ApplyEdit("foo foo foo", edit)
	if err == nil {
		t.Fatal("expected error for non-unique old text")
	}
}

func TestApplyEditEmptyContent(t *testing.T) {
	edit := Edit{Path: "test.go", OldText: "", NewText: "hello"}
	got, err := ApplyEdit("", edit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello" {
		t.Fatalf("got %q, want 'hello'", got)
	}
}

func TestApplyEditEmptyOldTextNonEmptyContent(t *testing.T) {
	edit := Edit{Path: "test.go", OldText: "", NewText: "hello"}
	_, err := ApplyEdit("existing", edit)
	if err == nil {
		t.Fatal("expected error for empty old text with non-empty content")
	}
}

func TestRunnerFunc(t *testing.T) {
	called := false
	var r Runner = RunnerFunc(func(ctx context.Context, s Session) (Result, error) {
		called = true
		return Result{Summary: "done"}, nil
	})
	result, err := r.Run(context.Background(), Session{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected runner to be called")
	}
	if result.Summary != "done" {
		t.Fatalf("summary = %q, want 'done'", result.Summary)
	}
}

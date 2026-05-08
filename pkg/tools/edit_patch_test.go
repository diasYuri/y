package tools

import (
	"testing"
)

func TestApplyTextEditsBasic(t *testing.T) {
	original := "hello world\nfoo bar\n"
	edits := []textEdit{{OldText: "world", NewText: "universe"}}
	got, err := applyTextEdits("test.txt", original, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "hello universe\nfoo bar\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestApplyTextEditsMultiple(t *testing.T) {
	original := "a\nb\nc\n"
	edits := []textEdit{
		{OldText: "a", NewText: "x"},
		{OldText: "b", NewText: "y"},
	}
	got, err := applyTextEdits("test.txt", original, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "x\ny\nc\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestApplyTextEditsNotFound(t *testing.T) {
	original := "hello world\n"
	edits := []textEdit{{OldText: "missing", NewText: "x"}}
	_, err := applyTextEdits("test.txt", original, edits)
	if err == nil {
		t.Fatal("expected error for missing old_text")
	}
}

func TestApplyTextEditsEmptyFile(t *testing.T) {
	original := ""
	edits := []textEdit{{OldText: "", NewText: "hello"}}
	got, err := applyTextEdits("test.txt", original, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "hello"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestApplyTextEditsMultiline(t *testing.T) {
	original := "line1\nline2\nline3\n"
	edits := []textEdit{{OldText: "line1\nline2", NewText: "replaced"}}
	got, err := applyTextEdits("test.txt", original, edits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "replaced\nline3\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNormalizeTextEdit(t *testing.T) {
	e := normalizeTextEdit(textEdit{OldTextCamel: "old", NewTextCamel: "new"})
	if e.OldText != "old" {
		t.Fatalf("OldText = %q, want old", e.OldText)
	}
	if e.NewText != "new" {
		t.Fatalf("NewText = %q, want new", e.NewText)
	}
}

func TestSplitLinesKeepNewline(t *testing.T) {
	lines := splitLinesKeepNewline("a\nb\nc")
	if len(lines) != 3 {
		t.Fatalf("len = %d, want 3", len(lines))
	}
	if lines[0] != "a\n" {
		t.Fatalf("line0 = %q, want a\\n", lines[0])
	}
	if lines[2] != "c" {
		t.Fatalf("line2 = %q, want c", lines[2])
	}
}

func TestSplitLinesKeepNewlineEmpty(t *testing.T) {
	lines := splitLinesKeepNewline("")
	if len(lines) != 1 {
		t.Fatalf("len = %d, want 1", len(lines))
	}
	if lines[0] != "" {
		t.Fatalf("line0 = %q, want empty", lines[0])
	}
}

func TestSplitLinesKeepNewlineTrailing(t *testing.T) {
	lines := splitLinesKeepNewline("a\n")
	if len(lines) != 2 {
		t.Fatalf("len = %d, want 2", len(lines))
	}
	if lines[0] != "a\n" {
		t.Fatalf("line0 = %q, want a\\n", lines[0])
	}
	if lines[1] != "" {
		t.Fatalf("line1 = %q, want empty", lines[1])
	}
}

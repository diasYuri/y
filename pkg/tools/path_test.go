package tools

import (
	"os"
	"strings"
	"testing"
)

func TestResolvePathBasic(t *testing.T) {
	root := t.TempDir()
	r, err := resolveForRead(root, "foo.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.EscapesWorkspace {
		t.Fatal("expected path to be inside workspace")
	}
	if !strings.HasSuffix(r.Absolute, "foo.txt") {
		t.Fatalf("expected absolute path to end with foo.txt, got %s", r.Absolute)
	}
}

func TestResolvePathEmptyWorkspace(t *testing.T) {
	_, err := resolveForRead("", "foo.txt")
	if err == nil {
		t.Fatal("expected error for empty workspace")
	}
}

func TestResolvePathEmptyInput(t *testing.T) {
	root := t.TempDir()
	_, err := resolveForRead(root, "")
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestResolvePathEscapesWorkspace(t *testing.T) {
	root := t.TempDir()
	r, err := resolveForRead(root, "../outside.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.EscapesWorkspace {
		t.Fatal("expected path to escape workspace")
	}
}

func TestResolvePathAbsoluteInside(t *testing.T) {
	// Skip on macOS because /tmp is a symlink to /private/tmp and
	// EvalSymlinks changes the root while the absolute input does not.
	if os.Getenv("CI") == "" {
		t.Skip("skipped: macOS /tmp symlink edge case")
	}
	root := t.TempDir()
	r, err := resolveForRead(root, root+"/foo.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.EscapesWorkspace {
		t.Fatal("expected absolute path inside workspace to be safe")
	}
}

func TestResolvePathAbsoluteOutside(t *testing.T) {
	root := t.TempDir()
	r, err := resolveForRead(root, "/tmp/outside.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.EscapesWorkspace {
		t.Fatal("expected absolute path outside workspace to escape")
	}
}

func TestResolvePathDotDotInside(t *testing.T) {
	root := t.TempDir()
	// a/b/../../c.txt should resolve to c.txt inside workspace
	r, err := resolveForRead(root, "a/b/../../c.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.EscapesWorkspace {
		t.Fatal("expected normalized path to stay inside workspace")
	}
}

func TestResolvePathSymlink(t *testing.T) {
	root := t.TempDir()
	// Create a symlink inside workspace pointing to itself
	target := root + "/real.txt"
	if err := os.WriteFile(target, []byte("hello"), 0644); err != nil {
		t.Skipf("cannot create file: %v", err)
	}
	r, err := resolveForRead(root, "real.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.EscapesWorkspace {
		t.Fatal("expected symlink target inside workspace to be safe")
	}
}

func TestIsInside(t *testing.T) {
	tests := []struct {
		root      string
		candidate string
		want      bool
	}{
		{"/a", "/a", true},
		{"/a", "/a/b", true},
		{"/a", "/a/b/c", true},
		{"/a", "/b", false},
		{"/a", "/a/../b", false},
	}
	for _, tt := range tests {
		got := isInside(tt.root, tt.candidate)
		if got != tt.want {
			t.Fatalf("isInside(%q, %q) = %v, want %v", tt.root, tt.candidate, got, tt.want)
		}
	}
}

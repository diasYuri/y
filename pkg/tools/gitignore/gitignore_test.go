package gitignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePatternSimple(t *testing.T) {
	p, err := parsePattern("*.go")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.negate {
		t.Fatal("expected no negate")
	}
	if p.dirOnly {
		t.Fatal("expected no dirOnly")
	}
	if p.anchored {
		t.Fatal("expected no anchored")
	}
	if !p.match("foo.go", false) {
		t.Fatal("expected *.go to match foo.go")
	}
	if p.match("foo.txt", false) {
		t.Fatal("expected *.go to not match foo.txt")
	}
}

func TestParsePatternNegate(t *testing.T) {
	p, err := parsePattern("!keep.go")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !p.negate {
		t.Fatal("expected negate")
	}
	if !p.match("keep.go", false) {
		t.Fatal("expected !keep.go to match keep.go")
	}
}

func TestParsePatternDirOnly(t *testing.T) {
	p, err := parsePattern("build/")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !p.dirOnly {
		t.Fatal("expected dirOnly")
	}
	if !p.match("build", true) {
		t.Fatal("expected build/ to match build dir")
	}
	if p.match("build", false) {
		t.Fatal("expected build/ to not match build file")
	}
}

func TestParsePatternAnchored(t *testing.T) {
	p, err := parsePattern("/foo.txt")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !p.anchored {
		t.Fatal("expected anchored")
	}
	if !p.match("foo.txt", false) {
		t.Fatal("expected /foo.txt to match foo.txt at root")
	}
	if p.match("bar/foo.txt", false) {
		t.Fatal("expected /foo.txt to not match bar/foo.txt")
	}
}

func TestGlobStar(t *testing.T) {
	p, err := parsePattern("**/node_modules")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !p.globStar {
		t.Fatal("expected globStar")
	}
	if !p.match("node_modules", true) {
		t.Fatal("expected **/node_modules to match node_modules")
	}
	if !p.match("a/b/node_modules", true) {
		t.Fatal("expected **/node_modules to match a/b/node_modules")
	}
}

func TestMatcherStack(t *testing.T) {
	m := &Matcher{
		patterns: []pattern{
			func() pattern { p, _ := parsePattern("*.log"); return p }(),
			func() pattern { p, _ := parsePattern("!important.log"); return p }(),
		},
	}
	if !m.Match("debug.log", false) {
		t.Fatal("expected debug.log to be ignored")
	}
	if m.Match("important.log", false) {
		t.Fatal("expected important.log to NOT be ignored (negated)")
	}
}

func TestCompileRealFile(t *testing.T) {
	tmpDir := t.TempDir()
	content := `# comment
*.log
build/
!important.log
/vendor
`
	path := filepath.Join(tmpDir, ".gitignore")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	m, err := Compile(path)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if m.Empty() {
		t.Fatal("expected non-empty matcher")
	}
	if !m.Match("app.log", false) {
		t.Fatal("expected *.log to match")
	}
	if !m.Match("build", true) {
		t.Fatal("expected build/ to match dir")
	}
	if m.Match("important.log", false) {
		t.Fatal("expected !important.log to negate")
	}
}

func TestWalkIgnore(t *testing.T) {
	tmpDir := t.TempDir()

	root := filepath.Join(tmpDir, "root")
	sub := filepath.Join(root, "pkg")
	os.MkdirAll(sub, 0o755)

	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\nbuild/\n"), 0o644)
	os.WriteFile(filepath.Join(sub, ".gitignore"), []byte("*_test.go\n"), 0o644)

	w := NewWalkIgnore()
	if err := w.AddDir(root); err != nil {
		t.Fatalf("add root: %v", err)
	}
	if err := w.AddDir(sub); err != nil {
		t.Fatalf("add sub: %v", err)
	}

	if !w.Match(filepath.Join(root, "debug.log"), false) {
		t.Fatal("expected debug.log to be ignored by root .gitignore")
	}
	if w.Match(filepath.Join(root, "main.go"), false) {
		t.Fatal("expected main.go to NOT be ignored")
	}
	if !w.Match(filepath.Join(sub, "foo_test.go"), false) {
		t.Fatal("expected foo_test.go to be ignored by sub .gitignore")
	}
}

func TestMatchSegmentGlob(t *testing.T) {
	tests := []struct {
		pat, seg string
		want     bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "main.txt", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"test_*.go", "test_foo.go", true},
		{"[ab].go", "a.go", true},
		{"[ab].go", "c.go", false},
	}
	for _, tt := range tests {
		got := matchSegment(tt.pat, tt.seg)
		if got != tt.want {
			t.Errorf("matchSegment(%q, %q) = %v, want %v", tt.pat, tt.seg, got, tt.want)
		}
	}
}

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSearchTextStructuredResultsAndArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses a POSIX script")
	}
	root := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	fixture := writeSearchFixture(t, `#!/bin/sh
printf '%s\0' "$@" > "$SEARCH_ARGS_CAPTURE"
cat <<'JSON'
{"type":"context","data":{"path":{"text":"sample.go"},"lines":{"text":"prior\n"},"line_number":1}}
{"type":"match","data":{"path":{"text":"sample.go"},"lines":{"text":"😀needle here\n"},"line_number":2,"submatches":[{"start":4}]}}
{"type":"context","data":{"path":{"text":"sample.go"},"lines":{"text":"later\n"},"line_number":3}}
{"type":"match","data":{"path":{"text":"sample.go"},"lines":{"text":"needle again\n"},"line_number":5,"submatches":[{"start":0}]}}
JSON
`)
	t.Setenv("SEARCH_ARGS_CAPTURE", argsFile)
	reg := NewRegistry()
	if err := RegisterCodeSearch(reg, CodeSearchOptions{WorkspaceRoot: root, RipgrepPath: fixture, AstGrepPath: filepath.Join(root, "missing-sg")}); err != nil {
		t.Fatal(err)
	}
	resp, err := reg.Handle(context.Background(), ToolRequest{Name: "search_text", WorkspaceRoot: root, Arguments: mustJSON(t, searchTextInput{
		Query: "$(echo safe)", Glob: "*.go", Language: "go", CaseSensitive: boolPointer(false), WholeWord: true, FixedString: true, MaxResults: 1, ContextLines: 1,
	})})
	if err != nil {
		t.Fatalf("search_text: %v", err)
	}
	var got searchTextResult
	if err := json.Unmarshal(resp.Details, &got); err != nil {
		t.Fatalf("search_text details: %v", err)
	}
	if len(got.Matches) != 1 || !got.Truncated {
		t.Fatalf("search_text result = %#v, want one truncated match", got)
	}
	match := got.Matches[0]
	if match.Path != "sample.go" || match.Line != 2 || match.Column != 2 || match.Text != "😀needle here" {
		t.Fatalf("match = %#v", match)
	}
	if len(match.Before) != 1 || match.Before[0] != "prior" || len(match.After) != 1 || match.After[0] != "later" {
		t.Fatalf("context = %#v", match)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(string(args), "\x00")
	for _, want := range []string{"--ignore-case", "--word-regexp", "--fixed-strings", "--glob", "*.go", "--type", "go", "--context", "1", "$(echo safe)"} {
		if !containsString(argv, want) {
			t.Errorf("ripgrep argv %q missing %q", argv, want)
		}
	}
}

func TestSearchTextInvalidRegexAndMissingBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses a POSIX script")
	}
	root := t.TempDir()
	badPattern := writeSearchFixture(t, "#!/bin/sh\necho 'regex parse error: unclosed group' >&2\nexit 2\n")
	reg := NewRegistry()
	if err := RegisterCodeSearch(reg, CodeSearchOptions{WorkspaceRoot: root, RipgrepPath: badPattern}); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Handle(context.Background(), ToolRequest{Name: "search_text", WorkspaceRoot: root, Arguments: mustJSON(t, searchTextInput{Query: "("})})
	if err == nil || !strings.Contains(err.Error(), "not a valid regular expression") {
		t.Fatalf("invalid regex error = %v", err)
	}
	otherSearchFailure := writeSearchFixture(t, "#!/bin/sh\necho 'unrecognized file type: custom' >&2\nexit 2\n")
	reg = NewRegistry()
	if err := RegisterCodeSearch(reg, CodeSearchOptions{WorkspaceRoot: root, RipgrepPath: otherSearchFailure}); err != nil {
		t.Fatal(err)
	}
	_, err = reg.Handle(context.Background(), ToolRequest{Name: "search_text", WorkspaceRoot: root, Arguments: mustJSON(t, searchTextInput{Query: "valid query"})})
	if err == nil || strings.Contains(err.Error(), "not a valid regular expression") {
		t.Fatalf("non-regex ripgrep failure = %v", err)
	}

	reg = NewRegistry()
	if err := RegisterCodeSearch(reg, CodeSearchOptions{WorkspaceRoot: root, RipgrepPath: filepath.Join(root, "missing-rg")}); err != nil {
		t.Fatal(err)
	}
	_, err = reg.Handle(context.Background(), ToolRequest{Name: "search_text", WorkspaceRoot: root, Arguments: mustJSON(t, searchTextInput{Query: "needle"})})
	if err == nil || !strings.Contains(err.Error(), "requires ripgrep") {
		t.Fatalf("missing rg error = %v", err)
	}
}

func TestASTSearchAndOutlineAreStructured(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses a POSIX script")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte("package sample\n😀go(worker())\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := writeSearchFixture(t, `#!/bin/sh
case "$1" in
  run)
    cat <<'JSON'
{"text":"go(worker())","file":"sample.go","language":"Go","range":{"start":{"line":1,"column":4},"end":{"line":1,"column":16}}}
JSON
    ;;
  outline)
    cat <<'JSON'
[{"path":"sample.go","items":[{"name":"Service","symbolType":"class","signature":"class Service","isExported":true,"range":{"start":{"line":0,"column":0},"end":{"line":5,"column":1}},"members":[{"name":"Run","symbolType":"method","signature":"Run()","isExported":true,"range":{"start":{"line":1,"column":2},"end":{"line":2,"column":3}}}]},{"name":"hidden","symbolType":"function","isExported":false,"range":{"start":{"line":6,"column":0},"end":{"line":7,"column":0}}}]}]
JSON
    ;;
esac
`)
	reg := NewRegistry()
	if err := RegisterCodeSearch(reg, CodeSearchOptions{WorkspaceRoot: root, AstGrepPath: fixture}); err != nil {
		t.Fatal(err)
	}
	search, err := reg.Handle(context.Background(), ToolRequest{Name: "search_ast", WorkspaceRoot: root, Arguments: mustJSON(t, astSearchInput{Pattern: "go($X)", Language: "go", MaxResults: 2})})
	if err != nil {
		t.Fatalf("search_ast: %v", err)
	}
	var matches astSearchResult
	if err := json.Unmarshal(search.Details, &matches); err != nil {
		t.Fatalf("search_ast details: %v", err)
	}
	if len(matches.Matches) != 1 || matches.Matches[0].Path != "sample.go" || matches.Matches[0].StartLine != 2 || matches.Matches[0].StartColumn != 2 || matches.Matches[0].EndColumn != 14 {
		t.Fatalf("AST matches = %#v", matches)
	}
	outline, err := reg.Handle(context.Background(), ToolRequest{Name: "outline", WorkspaceRoot: root, Arguments: mustJSON(t, outlineInput{Path: "sample.go"})})
	if err != nil {
		t.Fatalf("outline: %v", err)
	}
	var symbols outlineResult
	if err := json.Unmarshal(outline.Details, &symbols); err != nil {
		t.Fatalf("outline details: %v", err)
	}
	if len(symbols.Symbols) != 1 || symbols.Symbols[0].Name != "Service" || len(symbols.Symbols[0].Members) != 1 || symbols.Symbols[0].Members[0].Name != "Run" {
		t.Fatalf("outline symbols = %#v", symbols.Symbols)
	}
	invalidPattern := writeSearchFixture(t, "#!/bin/sh\necho 'error: invalid pattern' >&2\nexit 2\n")
	reg = NewRegistry()
	if err := RegisterCodeSearch(reg, CodeSearchOptions{WorkspaceRoot: root, AstGrepPath: invalidPattern}); err != nil {
		t.Fatal(err)
	}
	_, err = reg.Handle(context.Background(), ToolRequest{Name: "search_ast", WorkspaceRoot: root, Arguments: mustJSON(t, astSearchInput{Pattern: "($X"})})
	if err == nil || !strings.Contains(err.Error(), "structural pattern") {
		t.Fatalf("invalid ast-grep pattern error = %v", err)
	}
}

func TestFindFilesFallsBackAndRespectsFilesystemKinds(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "a.go"), []byte("package nested\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if err := RegisterCodeSearch(reg, CodeSearchOptions{WorkspaceRoot: root, RipgrepPath: filepath.Join(root, "missing-rg")}); err != nil {
		t.Fatal(err)
	}
	resp, err := reg.Handle(context.Background(), ToolRequest{Name: "find_files", WorkspaceRoot: root, Arguments: mustJSON(t, findFilesInput{Type: "directory", IncludeIgnored: true})})
	if err != nil {
		t.Fatalf("find_files fallback: %v", err)
	}
	var found findFilesResult
	if err := json.Unmarshal(resp.Details, &found); err != nil {
		t.Fatalf("find_files details: %v", err)
	}
	if len(found.Paths) != 1 || found.Paths[0] != "nested" {
		t.Fatalf("directories = %#v", found.Paths)
	}
}

func writeSearchFixture(t *testing.T, contents string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses a POSIX script")
	}
	path := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func boolPointer(value bool) *bool { return &value }

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

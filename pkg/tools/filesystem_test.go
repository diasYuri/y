package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	policypkg "github.com/yuri/y/internal/policy"
)

func TestFilesystemToolsRegisterDescriptors(t *testing.T) {
	reg := NewRegistry()
	if err := RegisterFilesystem(reg, FilesystemOptions{WorkspaceRoot: t.TempDir()}); err != nil {
		t.Fatalf("RegisterFilesystem returned error: %v", err)
	}
	got := reg.List()
	names := make([]string, 0, len(got))
	for _, desc := range got {
		names = append(names, desc.Name)
		if len(desc.Capabilities) == 0 {
			t.Fatalf("tool %s has no capabilities", desc.Name)
		}
		if desc.Limits.MaxOutputBytes == 0 {
			t.Fatalf("tool %s has no output limit", desc.Name)
		}
		if desc.Name == "write_file" && !desc.Sensitive {
			t.Fatalf("tool %s is not marked sensitive", desc.Name)
		}
	}
	want := []string{"edit", "list_files", "patch", "read_file", "search", "write_file"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("registered names = %v, want %v", names, want)
	}
}

func TestReadFileRespectsByteLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte("0123456789abcdef"), 0o644); err != nil {
		t.Fatalf("WriteFile fixture: %v", err)
	}
	reg := registerTestFS(t, root, ToolLimits{MaxFileReadBytes: 8, MaxOutputBytes: 64})
	resp, err := reg.Handle(context.Background(), ToolRequest{Name: "read_file", Arguments: mustJSON(t, readFileInput{Path: "big.txt"})})
	if err != nil {
		t.Fatalf("read_file returned error: %v", err)
	}
	text := resp.Content[0].Text
	if !strings.Contains(text, "01234567") || strings.Contains(text, "89abcdef") {
		t.Fatalf("read_file text = %q, want truncated first 8 bytes", text)
	}
	var details readFileDetails
	if err := json.Unmarshal(resp.Details, &details); err != nil {
		t.Fatalf("details JSON: %v", err)
	}
	if !details.Truncated || details.BytesRead != 8 {
		t.Fatalf("details = %#v, want truncated 8 bytes", details)
	}
}

func TestWriteFileCreatesParentsAndChecksLimit(t *testing.T) {
	root := t.TempDir()
	reg := registerTestFS(t, root, ToolLimits{MaxFileWriteBytes: 5})
	_, err := reg.Handle(context.Background(), ToolRequest{Name: "write_file", Arguments: mustJSON(t, writeFileInput{
		Path:    "dir/file.txt",
		Content: "hello",
	}), Approval: &policypkg.ApprovalResolution{Mode: policypkg.ApprovalModeHeadless, State: policypkg.ApprovalApproved}})
	if err != nil {
		t.Fatalf("write_file returned error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "dir", "file.txt"))
	if err != nil {
		t.Fatalf("ReadFile result: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("written content = %q, want hello", got)
	}
	_, err = reg.Handle(context.Background(), ToolRequest{Name: "write_file", Arguments: mustJSON(t, writeFileInput{
		Path:    "dir/too-big.txt",
		Content: "toolarge",
	}), Approval: &policypkg.ApprovalResolution{Mode: policypkg.ApprovalModeHeadless, State: policypkg.ApprovalApproved}})
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("oversize write error = %v, want ErrLimitExceeded", err)
	}
}

func TestWriteFileRequiresApprovalByDefault(t *testing.T) {
	root := t.TempDir()
	reg := registerTestFS(t, root, ToolLimits{})
	_, err := reg.Handle(context.Background(), ToolRequest{Name: "write_file", Arguments: mustJSON(t, writeFileInput{
		Path:    "dir/file.txt",
		Content: "hello",
	})})
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("write_file error = %v, want ErrApprovalRequired", err)
	}
}

func TestListFilesSortedWithLimit(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "Zoo"))
	mustWrite(t, filepath.Join(root, "alpha.txt"), "a")
	mustWrite(t, filepath.Join(root, "Beta.txt"), "b")
	reg := registerTestFS(t, root, ToolLimits{MaxEntries: 2, MaxOutputBytes: 128})

	resp, err := reg.Handle(context.Background(), ToolRequest{Name: "list_files", Arguments: mustJSON(t, listFilesInput{Path: ".", Limit: 2})})
	if err != nil {
		t.Fatalf("list_files returned error: %v", err)
	}
	text := resp.Content[0].Text
	if !strings.HasPrefix(text, "alpha.txt\nBeta.txt") {
		t.Fatalf("list_files text = %q, want sorted limited entries", text)
	}
	if !strings.Contains(text, "entries limit reached") {
		t.Fatalf("list_files text = %q, want limit notice", text)
	}
}

func TestSearchFindsLiteralMatchesWithLimits(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "one.txt"), "needle one\nother\nneedle two\n")
	mustWrite(t, filepath.Join(root, "two.go"), "needle go\n")
	reg := registerTestFS(t, root, ToolLimits{MaxMatches: 2, MaxOutputBytes: 256})

	resp, err := reg.Handle(context.Background(), ToolRequest{Name: "search", Arguments: mustJSON(t, searchInput{
		Pattern: "needle",
		Literal: true,
		Glob:    "*.txt",
		Limit:   1,
	})})
	if err != nil {
		t.Fatalf("search returned error: %v", err)
	}
	text := resp.Content[0].Text
	if !strings.Contains(text, "one.txt:1: needle one") {
		t.Fatalf("search text = %q, want first txt match", text)
	}
	if strings.Contains(text, "two.go") || strings.Contains(text, "needle two") {
		t.Fatalf("search text = %q, want glob and match limit applied", text)
	}
	if !strings.Contains(text, "matches limit reached") {
		t.Fatalf("search text = %q, want match limit notice", text)
	}
}

func TestSearchStopsReadingLargeFilesAtLimit(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "big.txt"), "alpha\nneedle after limit\n")
	reg := registerTestFS(t, root, ToolLimits{MaxFileReadBytes: 6, MaxOutputBytes: 256})

	resp, err := reg.Handle(context.Background(), ToolRequest{Name: "search", Arguments: mustJSON(t, searchInput{
		Pattern: "needle",
		Literal: true,
	})})
	if err != nil {
		t.Fatalf("search returned error: %v", err)
	}
	if text := resp.Content[0].Text; strings.Contains(text, "needle after limit") {
		t.Fatalf("search text = %q, want content after per-file read limit skipped", text)
	}
	var details searchDetails
	if err := json.Unmarshal(resp.Details, &details); err != nil {
		t.Fatalf("details JSON: %v", err)
	}
	if !details.FilesLimited {
		t.Fatalf("details = %#v, want FilesLimited", details)
	}
}

func TestEditAppliesExactReplacementsWithAuditDiff(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "file.txt"), "one\ntwo\nthree\n")
	reg := registerTestFS(t, root, ToolLimits{MaxOutputBytes: 4096})

	resp, err := reg.Handle(context.Background(), ToolRequest{Name: "edit", Arguments: mustJSON(t, editInput{
		Path: "file.txt",
		Edits: []textEdit{
			{OldText: "two\n", NewText: "TWO\n"},
			{OldText: "three\n", NewText: "THREE\n"},
		},
	}), Approval: &policypkg.ApprovalResolution{Mode: policypkg.ApprovalModeHeadless, State: policypkg.ApprovalApproved}})
	if err != nil {
		t.Fatalf("edit returned error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "file.txt"))
	if err != nil {
		t.Fatalf("ReadFile result: %v", err)
	}
	if string(got) != "one\nTWO\nTHREE\n" {
		t.Fatalf("edited content = %q", got)
	}
	if text := resp.Content[0].Text; !strings.Contains(text, "--- a/file.txt") || !strings.Contains(text, "+TWO") {
		t.Fatalf("edit response = %q, want audit diff", text)
	}
}

func TestEditAcceptsLegacyCamelCaseFields(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "file.txt"), "before\n")
	reg := registerTestFS(t, root, ToolLimits{})

	_, err := reg.Handle(context.Background(), ToolRequest{Name: "edit", Arguments: json.RawMessage(`{"path":"file.txt","edits":[{"oldText":"before\n","newText":"after\n"}]}`), Approval: &policypkg.ApprovalResolution{Mode: policypkg.ApprovalModeHeadless, State: policypkg.ApprovalApproved}})
	if err != nil {
		t.Fatalf("edit returned error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "file.txt"))
	if err != nil {
		t.Fatalf("ReadFile result: %v", err)
	}
	if string(got) != "after\n" {
		t.Fatalf("edited content = %q", got)
	}
}

func TestEditInvalidReplacementDoesNotPartiallyWrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	mustWrite(t, path, "one\ntwo\nthree\n")
	reg := registerTestFS(t, root, ToolLimits{})

	_, err := reg.Handle(context.Background(), ToolRequest{Name: "edit", Arguments: mustJSON(t, editInput{
		Path: "file.txt",
		Edits: []textEdit{
			{OldText: "one\n", NewText: "ONE\n"},
			{OldText: "missing\n", NewText: "MISSING\n"},
		},
	}), Approval: &policypkg.ApprovalResolution{Mode: policypkg.ApprovalModeHeadless, State: policypkg.ApprovalApproved}})
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("edit error = %v, want ErrInvalidTool", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile result: %v", err)
	}
	if string(got) != "one\ntwo\nthree\n" {
		t.Fatalf("content changed after invalid edit: %q", got)
	}
}

func TestPatchAppliesUnifiedDiffAfterValidation(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "file.txt"), "one\ntwo\nthree\n")
	reg := registerTestFS(t, root, ToolLimits{MaxOutputBytes: 4096})
	diff := "--- a/file.txt\n+++ b/file.txt\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n"

	_, err := reg.Handle(context.Background(), ToolRequest{Name: "patch", Arguments: mustJSON(t, patchInput{
		Patch: diff,
	}), Approval: &policypkg.ApprovalResolution{Mode: policypkg.ApprovalModeHeadless, State: policypkg.ApprovalApproved}})
	if err != nil {
		t.Fatalf("patch returned error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "file.txt"))
	if err != nil {
		t.Fatalf("ReadFile result: %v", err)
	}
	if string(got) != "one\nTWO\nthree\n" {
		t.Fatalf("patched content = %q", got)
	}
}

func TestPatchInvalidHunkDoesNotPartiallyWrite(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.txt")
	second := filepath.Join(root, "second.txt")
	mustWrite(t, first, "one\ntwo\n")
	mustWrite(t, second, "alpha\nbeta\n")
	reg := registerTestFS(t, root, ToolLimits{})
	diff := strings.Join([]string{
		"--- a/first.txt",
		"+++ b/first.txt",
		"@@ -1,2 +1,2 @@",
		" one",
		"-two",
		"+TWO",
		"--- a/second.txt",
		"+++ b/second.txt",
		"@@ -1,2 +1,2 @@",
		" alpha",
		"-missing",
		"+MISSING",
		"",
	}, "\n")

	_, err := reg.Handle(context.Background(), ToolRequest{Name: "patch", Arguments: mustJSON(t, patchInput{
		Patch: diff,
	}), Approval: &policypkg.ApprovalResolution{Mode: policypkg.ApprovalModeHeadless, State: policypkg.ApprovalApproved}})
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("patch error = %v, want ErrInvalidTool", err)
	}
	gotFirst, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("ReadFile first: %v", err)
	}
	gotSecond, err := os.ReadFile(second)
	if err != nil {
		t.Fatalf("ReadFile second: %v", err)
	}
	if string(gotFirst) != "one\ntwo\n" || string(gotSecond) != "alpha\nbeta\n" {
		t.Fatalf("content changed after invalid patch: first=%q second=%q", gotFirst, gotSecond)
	}
}

func TestEscapedPathsAreAuthorizedByPolicy(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.txt"), "secret")
	var seen []PolicyRequest
	reg := registerTestFSWithPolicy(t, root, ToolLimits{}, PolicyFunc(func(ctx context.Context, req PolicyRequest) (PolicyDecision, error) {
		seen = append(seen, req)
		return PolicyDecision{Kind: DecisionDeny}, nil
	}))

	_, err := reg.Handle(context.Background(), ToolRequest{Name: "read_file", Arguments: mustJSON(t, readFileInput{
		Path: filepath.Join("..", filepath.Base(outside), "secret.txt"),
	})})
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("escaped read error = %v, want ErrPolicyDenied", err)
	}
	if len(seen) != 1 || !seen[0].EscapesWorkspace {
		t.Fatalf("policy requests = %#v, want one escaped workspace request", seen)
	}
}

func TestSymlinkEscapesWorkspaceThroughPolicy(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	mustWrite(t, target, "secret")
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	reg := registerTestFS(t, root, ToolLimits{})
	_, err := reg.Handle(context.Background(), ToolRequest{Name: "read_file", Arguments: mustJSON(t, readFileInput{Path: "link.txt"})})
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("symlink escape error = %v, want ErrPolicyDenied", err)
	}
}

func TestFilesystemToolsRespectCanceledContext(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "file.txt"), "content")
	reg := registerTestFS(t, root, ToolLimits{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := reg.Handle(ctx, ToolRequest{Name: "read_file", Arguments: mustJSON(t, readFileInput{Path: "file.txt"})})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read error = %v, want context.Canceled", err)
	}
}

func registerTestFS(t *testing.T, root string, limits ToolLimits) *Registry {
	t.Helper()
	return registerTestFSWithPolicy(t, root, limits, nil)
}

func registerTestFSWithPolicy(t *testing.T, root string, limits ToolLimits, policy Policy) *Registry {
	t.Helper()
	reg := NewRegistry()
	if err := RegisterFilesystem(reg, FilesystemOptions{WorkspaceRoot: root, Limits: limits, Policy: policy}); err != nil {
		t.Fatalf("RegisterFilesystem returned error: %v", err)
	}
	return reg
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal fixture: %v", err)
	}
	return raw
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", path, err)
	}
}

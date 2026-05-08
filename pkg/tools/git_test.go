package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestGitToolsStatusDiffAndCommit(t *testing.T) {
	root := t.TempDir()
	runGitCommand(t, root, "init")
	runGitCommand(t, root, "config", "user.name", "Y Test")
	runGitCommand(t, root, "config", "user.email", "y.test@example.com")

	writeGitFile(t, root, "notes.txt", "hello\n")
	runGitCommand(t, root, "add", "notes.txt")
	runGitCommand(t, root, "commit", "-m", "initial")

	writeGitFile(t, root, "notes.txt", strings.Repeat("hello\n", 300))

	reg := registerTestGit(t, root, ToolLimits{MaxOutputBytes: 128})

	statusResp, err := reg.Handle(context.Background(), ToolRequest{Name: "git_status", WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("git_status returned error: %v", err)
	}
	if statusResp.IsError {
		t.Fatalf("git_status marked error: %#v", statusResp)
	}
	if got := statusResp.Content[0].Text; !strings.Contains(got, "notes.txt") || !strings.Contains(got, "##") {
		t.Fatalf("git_status output = %q, want modified file and branch header", got)
	}
	var statusDetails gitDetails
	if err := json.Unmarshal(statusResp.Details, &statusDetails); err != nil {
		t.Fatalf("git_status details JSON: %v", err)
	}
	if statusDetails.ExitCode != 0 {
		t.Fatalf("git_status details = %#v, want exit code 0", statusDetails)
	}

	diffResp, err := reg.Handle(context.Background(), ToolRequest{
		Name:          "git_diff",
		WorkspaceRoot: root,
		Arguments:     mustJSON(t, gitDiffInput{Paths: []string{"notes.txt"}}),
	})
	if err != nil {
		t.Fatalf("git_diff returned error: %v", err)
	}
	if diffResp.IsError {
		t.Fatalf("git_diff marked error: %#v", diffResp)
	}
	if got := diffResp.Content[0].Text; !strings.Contains(got, "diff --git") || !strings.Contains(got, "@@") {
		t.Fatalf("git_diff output = %q, want unified diff", got)
	}
	if !strings.Contains(diffResp.Content[0].Text, "stdout truncated") {
		t.Fatalf("git_diff output missing truncation note:\n%s", diffResp.Content[0].Text)
	}

	_, err = reg.Handle(context.Background(), ToolRequest{
		Name:          "git_commit",
		WorkspaceRoot: root,
		Arguments:     mustJSON(t, gitCommitInput{Message: "update notes", Paths: []string{"notes.txt"}}),
	})
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("git_commit without approval error = %v, want ErrApprovalRequired", err)
	}

	commitResp, err := reg.Handle(context.Background(), ToolRequest{
		Name:          "git_commit",
		WorkspaceRoot: root,
		Arguments:     mustJSON(t, gitCommitInput{Message: "update notes", Paths: []string{"notes.txt"}}),
		Approval:      approvedResolution(),
	})
	if err != nil {
		t.Fatalf("git_commit returned error: %v", err)
	}
	if commitResp.IsError {
		t.Fatalf("git_commit marked error: %#v", commitResp)
	}
	if got := commitResp.Content[0].Text; !strings.Contains(got, "stdout:") {
		t.Fatalf("git_commit output = %q, want commit output", got)
	}

	out, err := exec.Command("git", "-C", root, "log", "-1", "--pretty=%s").CombinedOutput()
	if err != nil {
		t.Fatalf("git log verification failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "update notes" {
		t.Fatalf("git log subject = %q, want update notes", got)
	}
}

func registerTestGit(t *testing.T, root string, limits ToolLimits) *Registry {
	t.Helper()
	reg := NewRegistry()
	if err := RegisterGit(reg, GitOptions{WorkspaceRoot: root, Limits: limits}); err != nil {
		t.Fatalf("RegisterGit returned error: %v", err)
	}
	return reg
}

func TestGitStatusAndDiffDoNotNeedApproval(t *testing.T) {
	root := t.TempDir()
	runGitCommand(t, root, "init")
	runGitCommand(t, root, "config", "user.name", "Y Test")
	runGitCommand(t, root, "config", "user.email", "y.test@example.com")

	writeGitFile(t, root, "notes.txt", "hello\n")
	runGitCommand(t, root, "add", "notes.txt")
	runGitCommand(t, root, "commit", "-m", "initial")
	writeGitFile(t, root, "notes.txt", "hello\nworld\n")

	reg := registerTestGit(t, root, ToolLimits{})
	if _, err := reg.Handle(context.Background(), ToolRequest{Name: "git_status", WorkspaceRoot: root}); err != nil {
		t.Fatalf("git_status required approval or failed: %v", err)
	}
	if _, err := reg.Handle(context.Background(), ToolRequest{Name: "git_diff", WorkspaceRoot: root}); err != nil {
		t.Fatalf("git_diff required approval or failed: %v", err)
	}
}

func TestGitCommitRequiresExplicitPaths(t *testing.T) {
	root := t.TempDir()
	runGitCommand(t, root, "init")
	runGitCommand(t, root, "config", "user.name", "Y Test")
	runGitCommand(t, root, "config", "user.email", "y.test@example.com")

	writeGitFile(t, root, "a.txt", "a\n")
	runGitCommand(t, root, "add", "a.txt")
	runGitCommand(t, root, "commit", "-m", "initial")

	writeGitFile(t, root, "a.txt", "aa\n")

	reg := registerTestGit(t, root, ToolLimits{})

	_, err := reg.Handle(context.Background(), ToolRequest{
		Name:          "git_commit",
		WorkspaceRoot: root,
		Arguments:     mustJSON(t, gitCommitInput{Message: "no paths"}),
		Approval:      approvedResolution(),
	})
	if err == nil {
		t.Fatalf("git_commit without paths should error, got nil")
	}
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("git_commit without paths error = %v, want ErrInvalidTool", err)
	}
	if !strings.Contains(err.Error(), "broad staging") {
		t.Fatalf("git_commit error message = %q, want mention of broad staging", err.Error())
	}
}

func TestGitCommitOnlyStagesSpecifiedPaths(t *testing.T) {
	root := t.TempDir()
	runGitCommand(t, root, "init")
	runGitCommand(t, root, "config", "user.name", "Y Test")
	runGitCommand(t, root, "config", "user.email", "y.test@example.com")

	writeGitFile(t, root, "track.txt", "track\n")
	writeGitFile(t, root, "leave.txt", "leave\n")
	runGitCommand(t, root, "add", "track.txt", "leave.txt")
	runGitCommand(t, root, "commit", "-m", "initial")

	writeGitFile(t, root, "track.txt", "track modified\n")
	writeGitFile(t, root, "leave.txt", "leave modified\n")

	reg := registerTestGit(t, root, ToolLimits{})

	commitResp, err := reg.Handle(context.Background(), ToolRequest{
		Name:          "git_commit",
		WorkspaceRoot: root,
		Arguments:     mustJSON(t, gitCommitInput{Message: "only track", Paths: []string{"track.txt"}}),
		Approval:      approvedResolution(),
	})
	if err != nil {
		t.Fatalf("git_commit returned error: %v", err)
	}
	if commitResp.IsError {
		t.Fatalf("git_commit marked error: %#v", commitResp)
	}

	out, err := exec.Command("git", "-C", root, "status", "--short").CombinedOutput()
	if err != nil {
		t.Fatalf("git status verification failed: %v\n%s", err, out)
	}
	status := strings.TrimSpace(string(out))
	if !strings.Contains(status, "leave.txt") {
		t.Fatalf("git status = %q, want leave.txt still modified", status)
	}
	if strings.Contains(status, "track.txt") {
		t.Fatalf("git status = %q, track.txt should not appear (it was committed)", status)
	}

	logOut, err := exec.Command("git", "-C", root, "log", "-1", "--pretty=%s").CombinedOutput()
	if err != nil {
		t.Fatalf("git log verification failed: %v\n%s", err, logOut)
	}
	if got := strings.TrimSpace(string(logOut)); got != "only track" {
		t.Fatalf("git log subject = %q, want only track", got)
	}
}

func TestGitCommitInDirtyWorktreeLeavesUnrelatedChanges(t *testing.T) {
	root := t.TempDir()
	runGitCommand(t, root, "init")
	runGitCommand(t, root, "config", "user.name", "Y Test")
	runGitCommand(t, root, "config", "user.email", "y.test@example.com")

	writeGitFile(t, root, " committed.go", "package main\n")
	writeGitFile(t, root, "unrelated.md", "# readme\n")
	writeGitFile(t, root, "scratch.tmp", "temp\n")
	runGitCommand(t, root, "add", " committed.go", "unrelated.md")
	runGitCommand(t, root, "commit", "-m", "initial")

	writeGitFile(t, root, " committed.go", "package main\n\nfunc main() {}\n")
	writeGitFile(t, root, "unrelated.md", "# readme\n\nupdated\n")
	writeGitFile(t, root, "scratch.tmp", "temp modified\n")

	reg := registerTestGit(t, root, ToolLimits{})

	_, err := reg.Handle(context.Background(), ToolRequest{
		Name:          "git_commit",
		WorkspaceRoot: root,
		Arguments:     mustJSON(t, gitCommitInput{Message: "update only go", Paths: []string{" committed.go"}}),
		Approval:      approvedResolution(),
	})
	if err != nil {
		t.Fatalf("git_commit returned error: %v", err)
	}

	out, err := exec.Command("git", "-C", root, "status", "--short").CombinedOutput()
	if err != nil {
		t.Fatalf("git status verification failed: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var hasUnrelated, hasScratch bool
	for _, line := range lines {
		if strings.Contains(line, "unrelated.md") {
			hasUnrelated = true
		}
		if strings.Contains(line, "scratch.tmp") {
			hasScratch = true
		}
	}
	if !hasUnrelated {
		t.Fatalf("git status = %q, want unrelated.md still modified", string(out))
	}
	if !hasScratch {
		t.Fatalf("git status = %q, want scratch.tmp still untracked/modified", string(out))
	}
}

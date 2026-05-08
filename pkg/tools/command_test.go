package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	policypkg "github.com/yuri/y/internal/policy"
)

func TestRunCommandRequiresApproval(t *testing.T) {
	reg := registerTestShell(t, t.TempDir(), ToolLimits{})
	_, err := reg.Handle(context.Background(), ToolRequest{
		Name:      "run_command",
		Arguments: mustJSON(t, runCommandInput{Command: "git", Args: []string{"--version"}}),
	})
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("run_command error = %v, want ErrApprovalRequired", err)
	}
}

func TestRunCommandDirectArgsAndShellOutputLimits(t *testing.T) {
	root := t.TempDir()
	reg := registerTestShell(t, root, ToolLimits{MaxOutputBytes: 64})

	resp, err := reg.Handle(context.Background(), ToolRequest{
		Name: "run_command",
		Arguments: mustJSON(t, runCommandInput{
			Command: "git",
			Args:    []string{"--version"},
		}),
		Approval: approvedResolution(),
	})
	if err != nil {
		t.Fatalf("run_command returned error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("run_command response marked error: %#v", resp)
	}
	text := resp.Content[0].Text
	if !strings.Contains(text, "git version") {
		t.Fatalf("run_command output = %q, want git version", text)
	}
	var details commandDetails
	if err := json.Unmarshal(resp.Details, &details); err != nil {
		t.Fatalf("run_command details JSON: %v", err)
	}
	if details.Shell {
		t.Fatalf("run_command details shell = true, want false")
	}
	if details.ExitCode != 0 || details.StdoutTruncated || details.StderrTruncated {
		t.Fatalf("run_command details = %#v, want clean direct exec", details)
	}

	resp, err = reg.Handle(context.Background(), ToolRequest{
		Name: "run_command",
		Arguments: mustJSON(t, runCommandInput{
			Command: "yes abc | head -n 2000",
			Shell:   true,
		}),
		Approval: approvedResolution(),
	})
	if err != nil {
		t.Fatalf("shell run_command returned error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("shell run_command marked error: %#v", resp)
	}
	if !strings.Contains(resp.Content[0].Text, "stdout truncated") {
		t.Fatalf("shell run_command output missing truncation note:\n%s", resp.Content[0].Text)
	}
	if !strings.Contains(resp.Content[0].Text, "abc") {
		t.Fatalf("shell run_command output missing command data:\n%s", resp.Content[0].Text)
	}
	if err := json.Unmarshal(resp.Details, &details); err != nil {
		t.Fatalf("shell run_command details JSON: %v", err)
	}
	if !details.Shell || !details.StdoutTruncated || details.ExitCode != 0 {
		t.Fatalf("shell run_command details = %#v, want explicit shell with truncation", details)
	}
}

func registerTestShell(t *testing.T, root string, limits ToolLimits) *Registry {
	t.Helper()
	reg := NewRegistry()
	if err := RegisterShell(reg, ShellOptions{WorkspaceRoot: root, Limits: limits}); err != nil {
		t.Fatalf("RegisterShell returned error: %v", err)
	}
	return reg
}

func approvedResolution() *policypkg.ApprovalResolution {
	return &policypkg.ApprovalResolution{
		Mode:  policypkg.ApprovalModeHeadless,
		State: policypkg.ApprovalApproved,
	}
}

func runGitCommand(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func writeGitFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

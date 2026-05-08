package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ShellOptions configures the subprocess tool.
type ShellOptions struct {
	WorkspaceRoot string
	Policy        Policy
	Limits        ToolLimits
	ShellPath     string
}

// RegisterShell registers the run_command tool.
func RegisterShell(r *Registry, opts ShellOptions) error {
	if r == nil {
		return fmt.Errorf("tool registry is nil")
	}
	shell := newShellTool(opts)
	return r.Add(shell.descriptor(), ToolHandlerFunc(shell.runCommand))
}

type shellTool struct {
	workspaceRoot string
	policy        Policy
	limits        ToolLimits
	shellPath     string
}

func newShellTool(opts ShellOptions) *shellTool {
	return &shellTool{
		workspaceRoot: opts.WorkspaceRoot,
		policy:        opts.Policy,
		limits:        commandLimits(opts.Limits),
		shellPath:     opts.ShellPath,
	}
}

func (t *shellTool) descriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:         "run_command",
		Description:  "Execute a subprocess with explicit shell opt-in, timeout, and output caps.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"args":{"type":"array","items":{"type":"string"}},"cwd":{"type":"string"},"shell":{"type":"boolean"},"timeout_seconds":{"type":"integer","minimum":1},"max_output_bytes":{"type":"integer","minimum":1}},"required":["command"],"additionalProperties":false}`),
		Capabilities: []Capability{CapabilityProcessExec},
		Limits:       t.limits,
		Sensitive:    true,
	}
}

type runCommandInput struct {
	Command        string   `json:"command"`
	Args           []string `json:"args,omitempty"`
	CWD            string   `json:"cwd,omitempty"`
	Shell          bool     `json:"shell,omitempty"`
	TimeoutSecs    int64    `json:"timeout_seconds,omitempty"`
	MaxOutputBytes int64    `json:"max_output_bytes,omitempty"`
}

type commandDetails struct {
	Command         string   `json:"command"`
	Args            []string `json:"args,omitempty"`
	CWD             string   `json:"cwd,omitempty"`
	Shell           bool     `json:"shell,omitempty"`
	ExitCode        int      `json:"exit_code"`
	TimedOut        bool     `json:"timed_out,omitempty"`
	StdoutBytes     int64    `json:"stdout_bytes"`
	StderrBytes     int64    `json:"stderr_bytes"`
	StdoutTruncated bool     `json:"stdout_truncated,omitempty"`
	StderrTruncated bool     `json:"stderr_truncated,omitempty"`
}

func (t *shellTool) runCommand(ctx context.Context, req ToolRequest) (ToolResponse, error) {
	if err := enforceInputLimit(req.Arguments, t.limits); err != nil {
		return ToolResponse{}, err
	}
	var input runCommandInput
	if err := json.Unmarshal(req.Arguments, &input); err != nil {
		return ToolResponse{}, toolError("invalid_arguments", "run_command arguments must be valid JSON", err)
	}
	if strings.TrimSpace(input.Command) == "" {
		return ToolResponse{}, toolError("invalid_arguments", "run_command command is required", ErrInvalidTool)
	}
	if input.Shell && len(input.Args) > 0 {
		return ToolResponse{}, toolError("invalid_arguments", "run_command args cannot be provided when shell is true", ErrInvalidTool)
	}

	// LLM fallback: if the command contains shell-like characters but shell
	// is false and no args were provided, the LLM likely meant to run the
	// full string through a shell. Automatically opt-in to shell mode.
	if !input.Shell && len(input.Args) == 0 && looksLikeShellCommand(input.Command) {
		input.Shell = true
	}

	cwd, err := resolveCommandDir(t.workspaceRoot, req.WorkspaceRoot, input.CWD)
	if err != nil {
		return ToolResponse{}, err
	}
	if err := authorize(ctx, t.policy, PolicyRequest{
		ToolName:      "run_command",
		Capability:    string(CapabilityProcessExec),
		WorkspaceRoot: filepath.Clean(firstNonEmpty(req.WorkspaceRoot, t.workspaceRoot)),
		Path:          cwd,
		ResolvedPath:  cwd,
		Sensitive:     true,
		Approval:      req.Approval,
	}); err != nil {
		return ToolResponse{}, err
	}

	timeout := t.limits.CommandTimeoutSeconds
	if input.TimeoutSecs > 0 && input.TimeoutSecs < timeout {
		timeout = input.TimeoutSecs
	}
	maxOutput := t.limits.MaxOutputBytes
	if input.MaxOutputBytes > 0 && input.MaxOutputBytes < maxOutput {
		maxOutput = input.MaxOutputBytes
	}

	result, err := executeCommand(ctx, commandSpec{
		command:   input.Command,
		args:      input.Args,
		cwd:       cwd,
		shell:     input.Shell,
		timeout:   timeout,
		limit:     maxOutput,
		shellPath: t.shellPath,
	})
	if err != nil {
		return ToolResponse{}, err
	}

	return commandResultResponse(result, commandDetails{
		Command:         input.Command,
		Args:            append([]string(nil), input.Args...),
		CWD:             cwd,
		Shell:           input.Shell,
		ExitCode:        result.ExitCode,
		TimedOut:        result.TimedOut,
		StdoutBytes:     result.StdoutBytes,
		StderrBytes:     result.StderrBytes,
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
	})
}

type commandSpec struct {
	command   string
	args      []string
	cwd       string
	shell     bool
	timeout   int64
	limit     int64
	shellPath string
}

type commandResult struct {
	Stdout          string
	Stderr          string
	StdoutBytes     int64
	StderrBytes     int64
	StdoutTruncated bool
	StderrTruncated bool
	ExitCode        int
	TimedOut        bool
}

func executeCommand(ctx context.Context, spec commandSpec) (commandResult, error) {
	timeoutCtx := ctx
	cancel := func() {}
	if spec.timeout > 0 {
		timeoutCtx, cancel = context.WithTimeout(ctx, secondsToDuration(spec.timeout))
	}
	defer cancel()

	commandName := spec.command
	args := append([]string(nil), spec.args...)
	if spec.shell {
		commandName, args = resolveShellCommand(spec.shellPath, spec.command)
		args = append(args, spec.command)
	}

	cmd := exec.CommandContext(timeoutCtx, commandName, args...)
	cmd.Dir = spec.cwd
	cmd.Env = os.Environ()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return commandResult{}, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return commandResult{}, err
	}

	if err := cmd.Start(); err != nil {
		return commandResult{}, err
	}

	var wg sync.WaitGroup
	stdoutCapture := newCapture(spec.limit)
	stderrCapture := newCapture(spec.limit)

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = stdoutCapture.CopyFrom(stdoutPipe)
	}()
	go func() {
		defer wg.Done()
		_, _ = stderrCapture.CopyFrom(stderrPipe)
	}()

	waitErr := cmd.Wait()
	wg.Wait()

	result := commandResult{
		Stdout:          stdoutCapture.String(),
		Stderr:          stderrCapture.String(),
		StdoutBytes:     stdoutCapture.Bytes(),
		StderrBytes:     stderrCapture.Bytes(),
		StdoutTruncated: stdoutCapture.Truncated(),
		StderrTruncated: stderrCapture.Truncated(),
		ExitCode:        exitCodeFor(cmd, waitErr),
		TimedOut:        timeoutCtx.Err() == context.DeadlineExceeded,
	}
	return result, nil
}

func commandResultResponse(result commandResult, details any) (ToolResponse, error) {
	text := formatCommandOutput(result)
	resp := ToolResponse{
		Content: []ContentBlock{{Type: ContentText, Text: text}},
	}
	if details != nil {
		raw, err := json.Marshal(details)
		if err != nil {
			return ToolResponse{}, fmt.Errorf("marshal command details: %w", err)
		}
		resp.Details = raw
	}
	if result.ExitCode != 0 || result.TimedOut {
		resp.IsError = true
	}
	return resp, nil
}

func formatCommandOutput(result commandResult) string {
	estBytes := len(result.Stdout) + len(result.Stderr) + 64
	var b strings.Builder
	b.Grow(estBytes)
	wrote := false
	if result.Stdout != "" {
		b.WriteString("stdout:\n")
		b.WriteString(result.Stdout)
		wrote = true
	}
	if result.Stderr != "" {
		if wrote {
			b.WriteString("\n\n")
		}
		b.WriteString("stderr:\n")
		b.WriteString(result.Stderr)
		wrote = true
	}
	if !wrote {
		b.WriteString("(no output)")
	}
	hasNote := false
	appendNote := func(note string) {
		if hasNote {
			b.WriteString("; ")
			b.WriteString(note)
			return
		}
		b.WriteString("\n\n[")
		b.WriteString(note)
		hasNote = true
	}
	if result.StdoutTruncated {
		appendNote("stdout truncated to output limit")
	}
	if result.StderrTruncated {
		appendNote("stderr truncated to output limit")
	}
	if result.TimedOut {
		appendNote("command timed out")
	} else if result.ExitCode != 0 {
		appendNote(fmt.Sprintf("command exited with code %d", result.ExitCode))
	}
	if hasNote {
		b.WriteString("]")
	}
	return b.String()
}

type streamCapture struct {
	limit     int64
	buf       []byte
	count     int64
	truncated bool
}

func newCapture(limit int64) *streamCapture {
	if limit <= 0 {
		limit = DefaultMaxCommandOutputBytes
	}
	return &streamCapture{limit: limit}
}

func (c *streamCapture) CopyFrom(r io.Reader) (int64, error) {
	return ioCopy(c, r)
}

func (c *streamCapture) Write(p []byte) (int, error) {
	c.count += int64(len(p))
	if c.limit <= 0 {
		c.buf = append(c.buf, p...)
		return len(p), nil
	}
	if int64(len(c.buf)) >= c.limit {
		c.truncated = true
		return len(p), nil
	}
	remaining := int(c.limit - int64(len(c.buf)))
	if len(p) <= remaining {
		c.buf = append(c.buf, p...)
		return len(p), nil
	}
	c.buf = append(c.buf, p[:remaining]...)
	c.truncated = true
	return len(p), nil
}

func (c *streamCapture) String() string {
	return string(c.buf)
}

func (c *streamCapture) Bytes() int64 {
	return c.count
}

func (c *streamCapture) Truncated() bool {
	return c.truncated
}

func exitCodeFor(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	if err == nil {
		return 0
	}
	return 1
}

func resolveShellCommand(shellPath, command string) (string, []string) {
	if shellPath == "" {
		shellPath = defaultShellPath()
	}
	switch runtime.GOOS {
	case "windows":
		return shellPath, []string{"/C"}
	default:
		return shellPath, []string{"-lc"}
	}
}

func defaultShellPath() string {
	if runtime.GOOS == "windows" {
		if comspec := os.Getenv("COMSPEC"); comspec != "" {
			return comspec
		}
		return "cmd"
	}
	return "/bin/sh"
}

func resolveCommandDir(defaultRoot, requestRoot, cwd string) (string, error) {
	root := firstNonEmpty(requestRoot, defaultRoot)
	if strings.TrimSpace(cwd) == "" {
		if root != "" {
			resolved, err := resolveForRead(root, ".")
			if err != nil {
				return "", err
			}
			return resolved.Absolute, nil
		}
		return os.Getwd()
	}
	if root != "" {
		resolved, err := resolveForRead(root, cwd)
		if err != nil {
			return "", err
		}
		return resolved.Absolute, nil
	}
	return filepath.Abs(cwd)
}

// looksLikeShellCommand reports whether a command string appears to be a
// shell expression rather than a single executable name. This helps LLM-
// generated calls that pass "node hello.mjs" as the command field without
// setting shell=true.
func looksLikeShellCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	// Contains spaces (more than one word).
	if strings.ContainsAny(cmd, " \t") {
		return true
	}
	// Contains shell metacharacters even without spaces (e.g. "a&&b").
	const shellMeta = "|&;<>()$`\"'*?[]{}"
	if strings.ContainsAny(cmd, shellMeta) {
		return true
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func secondsToDuration(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// ioCopy is defined in a small wrapper so tests can replace it if needed.
var ioCopy = func(dst io.Writer, src io.Reader) (int64, error) {
	return io.Copy(dst, src)
}

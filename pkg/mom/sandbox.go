package mom

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ExecOptions tunes a sandbox execution.
type ExecOptions struct {
	Timeout      time.Duration
	MaxOutput    int64
	WorkingDir   string
	Env          []string
	Stdin        string
	Cancellation <-chan struct{}
}

// ExecResult is returned by Sandbox.Exec.
type ExecResult struct {
	Stdout          string
	Stderr          string
	StdoutBytes     int64
	StderrBytes     int64
	StdoutTruncated bool
	StderrTruncated bool
	ExitCode        int
	TimedOut        bool
}

// Sandbox executes shell commands either on the host or via a Docker
// container. The interface intentionally mirrors what y-mom needs from the
// sandbox so tests can swap in fakes.
type Sandbox interface {
	Kind() SandboxKind
	WorkspacePath(hostPath string) string
	Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error)
}

// HostSandbox runs commands directly on the host shell.
type HostSandbox struct {
	Shell      string
	MaxOutput  int64
	DefaultEnv []string
}

// NewHostSandbox creates a host sandbox with sensible defaults.
func NewHostSandbox() *HostSandbox {
	return &HostSandbox{
		Shell:     defaultMomShell(),
		MaxOutput: 10 * 1024 * 1024,
	}
}

// Kind reports SandboxHost.
func (h *HostSandbox) Kind() SandboxKind { return SandboxHost }

// WorkspacePath returns hostPath unchanged.
func (h *HostSandbox) WorkspacePath(hostPath string) string { return hostPath }

// Exec runs command using the configured shell.
func (h *HostSandbox) Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error) {
	if strings.TrimSpace(command) == "" {
		return ExecResult{}, errors.New("sandbox: empty command")
	}
	shell, args := shellInvocation(h.Shell)
	args = append(args, command)
	limit := opts.MaxOutput
	if limit <= 0 {
		limit = h.MaxOutput
	}
	return runShell(ctx, shell, args, opts, limit, h.DefaultEnv)
}

// DockerSandbox runs commands inside a long-running Docker container.
type DockerSandbox struct {
	Container string
	Shell     string
	MaxOutput int64
}

// NewDockerSandbox creates a docker-backed sandbox bound to container.
func NewDockerSandbox(container string) *DockerSandbox {
	return &DockerSandbox{
		Container: container,
		Shell:     "sh",
		MaxOutput: 10 * 1024 * 1024,
	}
}

// Kind reports SandboxDocker.
func (d *DockerSandbox) Kind() SandboxKind { return SandboxDocker }

// WorkspacePath always returns "/workspace" because the container exposes the
// host data directory under that mount.
func (d *DockerSandbox) WorkspacePath(_ string) string { return "/workspace" }

// Exec wraps the command in `docker exec <container> sh -c <command>`.
func (d *DockerSandbox) Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error) {
	if strings.TrimSpace(command) == "" {
		return ExecResult{}, errors.New("sandbox: empty command")
	}
	if strings.TrimSpace(d.Container) == "" {
		return ExecResult{}, errors.New("sandbox: docker container is required")
	}
	shell := d.Shell
	if shell == "" {
		shell = "sh"
	}
	args := []string{"exec", d.Container, shell, "-c", command}
	limit := opts.MaxOutput
	if limit <= 0 {
		limit = d.MaxOutput
	}
	return runShell(ctx, "docker", args, opts, limit, nil)
}

// FakeSandbox is a deterministic Sandbox for tests.
type FakeSandbox struct {
	Responses []FakeExecResponse
	Calls     []FakeExecCall
}

// FakeExecCall captures a call to FakeSandbox.Exec.
type FakeExecCall struct {
	Command string
	Options ExecOptions
}

// FakeExecResponse drives the next FakeSandbox.Exec invocation.
type FakeExecResponse struct {
	Result ExecResult
	Err    error
}

// Kind reports SandboxHost for fakes.
func (f *FakeSandbox) Kind() SandboxKind { return SandboxHost }

// WorkspacePath returns hostPath unchanged.
func (f *FakeSandbox) WorkspacePath(hostPath string) string { return hostPath }

// Exec consumes the next queued response.
func (f *FakeSandbox) Exec(_ context.Context, command string, opts ExecOptions) (ExecResult, error) {
	f.Calls = append(f.Calls, FakeExecCall{Command: command, Options: opts})
	if len(f.Responses) == 0 {
		return ExecResult{ExitCode: 0}, nil
	}
	resp := f.Responses[0]
	f.Responses = f.Responses[1:]
	return resp.Result, resp.Err
}

// ValidateSandbox performs a best-effort liveness check for the configuration.
// HostSandbox is always valid; DockerSandbox requires both `docker` on PATH and
// a running container.
func ValidateSandbox(ctx context.Context, cfg SandboxConfig) error {
	switch cfg.Kind {
	case SandboxHost, "":
		return nil
	case SandboxDocker:
		if strings.TrimSpace(cfg.Container) == "" {
			return errors.New("sandbox: docker requires a container name (e.g. docker:mom-sandbox)")
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", "--version").CombinedOutput()
		if err != nil {
			return fmt.Errorf("sandbox: docker not available: %s", strings.TrimSpace(string(out)))
		}
		out, err = exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", cfg.Container).CombinedOutput()
		if err != nil {
			return fmt.Errorf("sandbox: container %q not found: %s", cfg.Container, strings.TrimSpace(string(out)))
		}
		if strings.TrimSpace(string(out)) != "true" {
			return fmt.Errorf("sandbox: container %q is not running", cfg.Container)
		}
		return nil
	default:
		return fmt.Errorf("sandbox: unknown kind %q", cfg.Kind)
	}
}

// NewSandbox constructs a Sandbox for the configured kind.
func NewSandbox(cfg SandboxConfig) (Sandbox, error) {
	switch cfg.Kind {
	case SandboxHost, "":
		return NewHostSandbox(), nil
	case SandboxDocker:
		if cfg.Container == "" {
			return nil, errors.New("sandbox: docker requires a container name")
		}
		return NewDockerSandbox(cfg.Container), nil
	default:
		return nil, fmt.Errorf("sandbox: unknown kind %q", cfg.Kind)
	}
}

// ParseSandboxArg parses CLI flags such as `host` or `docker:my-container`.
func ParseSandboxArg(value string) (SandboxConfig, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == string(SandboxHost) {
		return SandboxConfig{Kind: SandboxHost}, nil
	}
	if strings.HasPrefix(value, "docker:") {
		container := strings.TrimSpace(strings.TrimPrefix(value, "docker:"))
		if container == "" {
			return SandboxConfig{}, errors.New("sandbox: docker requires a container name")
		}
		return SandboxConfig{Kind: SandboxDocker, Container: container}, nil
	}
	return SandboxConfig{}, fmt.Errorf("sandbox: invalid value %q (use host or docker:<container>)", value)
}

func runShell(ctx context.Context, command string, args []string, opts ExecOptions, limit int64, defaultEnv []string) (ExecResult, error) {
	cmdCtx := ctx
	cancel := func() {}
	if opts.Timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, command, args...)
	if opts.WorkingDir != "" {
		cmd.Dir = opts.WorkingDir
	}
	if len(opts.Env) > 0 {
		cmd.Env = opts.Env
	} else if len(defaultEnv) > 0 {
		cmd.Env = defaultEnv
	}
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return ExecResult{}, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return ExecResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return ExecResult{}, err
	}

	stdoutBuf := newBoundedBuffer(limit)
	stderrBuf := newBoundedBuffer(limit)
	doneStdout := make(chan struct{})
	doneStderr := make(chan struct{})
	go func() { _, _ = io.Copy(stdoutBuf, stdoutPipe); close(doneStdout) }()
	go func() { _, _ = io.Copy(stderrBuf, stderrPipe); close(doneStderr) }()

	cancelCh := opts.Cancellation
	if cancelCh != nil {
		go func() {
			select {
			case <-cancelCh:
				_ = cmd.Process.Kill()
			case <-cmdCtx.Done():
			}
		}()
	}

	waitErr := cmd.Wait()
	<-doneStdout
	<-doneStderr

	timedOut := cmdCtx.Err() == context.DeadlineExceeded
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	} else if waitErr != nil {
		exitCode = 1
	}

	return ExecResult{
		Stdout:          stdoutBuf.String(),
		Stderr:          stderrBuf.String(),
		StdoutBytes:     stdoutBuf.TotalBytes(),
		StderrBytes:     stderrBuf.TotalBytes(),
		StdoutTruncated: stdoutBuf.Truncated(),
		StderrTruncated: stderrBuf.Truncated(),
		ExitCode:        exitCode,
		TimedOut:        timedOut,
	}, nil
}

type boundedBuffer struct {
	limit     int64
	total     int64
	buf       bytes.Buffer
	truncated bool
}

func newBoundedBuffer(limit int64) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.total += int64(len(p))
	if b.limit <= 0 {
		return b.buf.Write(p)
	}
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if int64(len(p)) <= remaining {
		return b.buf.Write(p)
	}
	_, _ = b.buf.Write(p[:remaining])
	b.truncated = true
	return len(p), nil
}

func (b *boundedBuffer) String() string    { return b.buf.String() }
func (b *boundedBuffer) Truncated() bool   { return b.truncated }
func (b *boundedBuffer) TotalBytes() int64 { return b.total }

func defaultMomShell() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "/bin/sh"
}

func shellInvocation(shell string) (string, []string) {
	if shell == "" {
		shell = defaultMomShell()
	}
	if runtime.GOOS == "windows" {
		return shell, []string{"/C"}
	}
	return shell, []string{"-c"}
}

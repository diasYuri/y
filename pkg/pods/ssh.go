package pods

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SSHClient abstracts remote SSH operations for testability.
type SSHClient interface {
	Exec(ctx context.Context, sshCmd, command string) (SSHResult, error)
	ExecStream(ctx context.Context, sshCmd, command string, opts StreamOpts) (int, error)
	SCP(ctx context.Context, sshCmd, localPath, remotePath string) error
}

// StreamOpts controls streaming execution behavior.
type StreamOpts struct {
	Silent    bool
	ForceTTY  bool
	KeepAlive bool
}

// DefaultSSHClient is the production SSH client.
type DefaultSSHClient struct{}

func parseSSH(sshCmd string) (binary string, args []string) {
	parts := strings.Fields(sshCmd)
	if len(parts) == 0 {
		return "ssh", nil
	}
	return parts[0], parts[1:]
}

// Exec runs a remote command and captures output.
func (DefaultSSHClient) Exec(ctx context.Context, sshCmd, command string) (SSHResult, error) {
	binary, args := parseSSH(sshCmd)
	args = append(args, command)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	return SSHResult{Stdout: string(out), Stderr: "", ExitCode: exitCode}, nil
}

// ExecStream runs a remote command with inherited stdio.
func (DefaultSSHClient) ExecStream(ctx context.Context, sshCmd, command string, opts StreamOpts) (int, error) {
	binary, args := parseSSH(sshCmd)
	if opts.ForceTTY {
		hasT := false
		for _, a := range args {
			if a == "-t" {
				hasT = true
				break
			}
		}
		if !hasT {
			args = append([]string{"-t"}, args...)
		}
	}
	if opts.KeepAlive {
		args = append([]string{"-o", "ServerAliveInterval=30", "-o", "ServerAliveCountMax=120"}, args...)
	}
	args = append(args, command)
	cmd := exec.CommandContext(ctx, binary, args...)
	if opts.Silent {
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil
	} else {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 1, nil
	}
	return 0, nil
}

// SCP copies a local file to the remote host.
func (DefaultSSHClient) SCP(ctx context.Context, sshCmd, localPath, remotePath string) error {
	parts := strings.Fields(sshCmd)
	host := ""
	port := "22"
	for i := 1; i < len(parts); i++ {
		if parts[i] == "-p" && i+1 < len(parts) {
			port = parts[i+1]
			i++
		} else if !strings.HasPrefix(parts[i], "-") {
			host = parts[i]
			break
		}
	}
	if host == "" {
		return fmt.Errorf("could not parse host from SSH command")
	}
	cmd := exec.CommandContext(ctx, "scp", "-P", port, localPath, host+":"+remotePath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// FakeSSHClient records calls and returns canned responses for tests.
type FakeSSHClient struct {
	Calls        []FakeSSHCall
	ExecResults  map[string]SSHResult
	StreamResult int
	SCPErr       error

	// Optional overrides for dynamic behavior in tests.
	ExecFunc       func(ctx context.Context, sshCmd, command string) (SSHResult, error)
	ExecStreamFunc func(ctx context.Context, sshCmd, command string, opts StreamOpts) (int, error)
	SCPFunc        func(ctx context.Context, sshCmd, localPath, remotePath string) error
}

// FakeSSHCall records a single invocation.
type FakeSSHCall struct {
	Kind    string
	SSHCmd  string
	Command string
	Local   string
	Remote  string
}

// Exec implements SSHClient.
func (f *FakeSSHClient) Exec(ctx context.Context, sshCmd, command string) (SSHResult, error) {
	f.Calls = append(f.Calls, FakeSSHCall{Kind: "exec", SSHCmd: sshCmd, Command: command})
	if f.ExecFunc != nil {
		return f.ExecFunc(ctx, sshCmd, command)
	}
	key := sshCmd + "::" + command
	if r, ok := f.ExecResults[key]; ok {
		return r, nil
	}
	if r, ok := f.ExecResults[command]; ok {
		return r, nil
	}
	return SSHResult{Stdout: "", ExitCode: 0}, nil
}

// ExecStream implements SSHClient.
func (f *FakeSSHClient) ExecStream(ctx context.Context, sshCmd, command string, opts StreamOpts) (int, error) {
	f.Calls = append(f.Calls, FakeSSHCall{Kind: "stream", SSHCmd: sshCmd, Command: command})
	if f.ExecStreamFunc != nil {
		return f.ExecStreamFunc(ctx, sshCmd, command, opts)
	}
	return f.StreamResult, nil
}

// SCP implements SSHClient.
func (f *FakeSSHClient) SCP(ctx context.Context, sshCmd, localPath, remotePath string) error {
	f.Calls = append(f.Calls, FakeSSHCall{Kind: "scp", SSHCmd: sshCmd, Local: localPath, Remote: remotePath})
	if f.SCPFunc != nil {
		return f.SCPFunc(ctx, sshCmd, localPath, remotePath)
	}
	return f.SCPErr
}

// WaitForExec blocks until an exec call matching command appears (test helper).
func (f *FakeSSHClient) WaitForExec(command string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, c := range f.Calls {
			if c.Kind == "exec" && c.Command == command {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

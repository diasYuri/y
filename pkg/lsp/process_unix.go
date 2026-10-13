//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package lsp

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureManagedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killManagedProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

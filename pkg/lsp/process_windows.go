//go:build windows

package lsp

import "os/exec"

func configureManagedProcess(*exec.Cmd) {}

func killManagedProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

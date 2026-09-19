//go:build !windows

package memory

import (
	"os"
	"syscall"
)

func acquireProcessLock(file *os.File, write bool) error {
	flag := syscall.LOCK_SH
	if write {
		flag = syscall.LOCK_EX
	}
	return syscall.Flock(int(file.Fd()), flag)
}

func releaseProcessLock(file *os.File) { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }

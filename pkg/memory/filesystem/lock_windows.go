//go:build windows

package filesystem

import (
	"golang.org/x/sys/windows"
	"os"
)

func acquireProcessLock(file *os.File, write bool) error {
	var flags uint32
	if write {
		flags = windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	return windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &windows.Overlapped{})
}
func releaseProcessLock(file *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}

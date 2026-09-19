//go:build windows

package memory

import "os"

// Windows deployments should replace this with a durable backend or a native
// LockFileEx implementation. Atomic rename still protects single-process
// writes in the minimal cross-compiled binary.
func acquireProcessLock(_ *os.File, _ bool) error { return nil }
func releaseProcessLock(_ *os.File)               {}

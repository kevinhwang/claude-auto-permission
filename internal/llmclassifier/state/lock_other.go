//go:build !unix

package state

import "os"

// On non-Unix platforms (Windows) we don't take a real file lock. Concurrent same-session classification is rare enough
// on Windows that the worst case — a slightly inaccurate counter — is acceptable. A proper implementation would use
// windows.LockFileEx from golang.org/x/sys/windows.
func lockFile(_ *os.File) error { return nil }
func unlockFile(_ *os.File)     {}

//go:build unix

package state

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive POSIX advisory lock on f, blocking until acquired. Released by Close or unlockFile.
//
// flock(2) is whole-file and the kernel releases it on fd close, so a crashed process can't leave a stale lock.
// unix.Flock (vs syscall.Flock) handles EINTR retries internally.
func lockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}

func unlockFile(f *os.File) {
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

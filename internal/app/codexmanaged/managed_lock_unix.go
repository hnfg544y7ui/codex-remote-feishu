//go:build linux || darwin

package codexmanaged

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func tryManagedFileLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func unlockManagedFile(file *os.File) { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }

//go:build !windows

package sharedbackend

import (
	"fmt"
	"os"
	"syscall"
)

func securePath(path string, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}

func privatePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("host package permissions or file type are unsafe")
	}
	metadata, ok := info.Sys().(*syscall.Stat_t)
	if !ok || metadata.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("host package is owned by another user")
	}
	return nil
}

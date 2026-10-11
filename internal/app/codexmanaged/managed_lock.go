package codexmanaged

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// A kernel lock has no JSON ownership record. Creating an empty lock file is
// harmless, and a crashed installer releases its lock without stale lock theft.
type managedLock struct{ file *os.File }

func lockManaged(ctx context.Context, root string) (*managedLock, error) {
	return lockManagedFile(ctx, root, "managed.lock")
}

func lockManagedFile(ctx context.Context, root, name string) (*managedLock, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		ok, err := tryManagedFileLock(file)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("lock managed Codex: %w", err)
		}
		if ok {
			return &managedLock{file: file}, nil
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (lock *managedLock) release() {
	unlockManagedFile(lock.file)
	_ = lock.file.Close()
}

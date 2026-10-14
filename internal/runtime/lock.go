package relayruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

var ErrLockHeld = errors.New("lock held by another process")

type FileLock struct {
	path  string
	token string
}

type lockRecord struct {
	PID          int       `json:"pid"`
	Token        string    `json:"token"`
	CreatedAt    time.Time `json:"createdAt"`
	BootID       string    `json:"bootId,omitempty"`
	ProcessStart string    `json:"processStart,omitempty"`
}

func AcquireLock(ctx context.Context, path string, wait bool) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	record := lockRecord{
		PID:       os.Getpid(),
		Token:     token,
		CreatedAt: time.Now().UTC(),
	}
	record.BootID, record.ProcessStart = lockProcessIdentity(record.PID)
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}

	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, writeErr := file.Write(raw); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, writeErr
			}
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(path)
				return nil, closeErr
			}
			return &FileLock{path: path, token: token}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		cleared, clearErr := clearStaleLock(path)
		if clearErr != nil {
			return nil, clearErr
		}
		if cleared {
			continue
		}
		if !wait {
			return nil, ErrLockHeld
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func (l *FileLock) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	raw, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var record lockRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	if record.Token != l.token {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func clearStaleLock(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	var record lockRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return false, err
	}
	if record.PID > 0 && !lockOwnerReplaced(record) && processAlive(record.PID) {
		return false, nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return true, nil
}

func randomToken() (string, error) {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

//go:build linux

package relayruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockProcessStart(t *testing.T) {
	stat := "123 (worker (old) name) S " + strings.Repeat("0 ", 18) + "4567 0"
	if got := lockProcessStart(stat); got != "4567" {
		t.Fatalf("starttime = %q", got)
	}
	for _, stat := range []string{"", "123 (truncated) S 1", "123 missing comm"} {
		if got := lockProcessStart(stat); got != "" {
			t.Fatalf("malformed stat returned %q", got)
		}
	}
}

func TestAcquireLockLinuxProcessIdentity(t *testing.T) {
	bootID, processStart := lockProcessIdentity(os.Getpid())
	if bootID == "" || processStart == "" {
		t.Fatal("Linux kernel process identity unavailable")
	}
	base := lockRecord{PID: os.Getpid(), Token: "original-owner", CreatedAt: time.Now().UTC(), BootID: bootID, ProcessStart: processStart}
	tests := []struct {
		name   string
		record lockRecord
		stale  bool
	}{
		{"live owner", base, false},
		{"prior boot reused live pid", lockRecord{PID: base.PID, Token: base.Token, CreatedAt: base.CreatedAt, BootID: "previous-boot", ProcessStart: base.ProcessStart}, true},
		{"same boot reused pid", lockRecord{PID: base.PID, Token: base.Token, CreatedAt: base.CreatedAt, BootID: base.BootID, ProcessStart: "0"}, true},
		{"legacy prior boot live pid", lockRecord{PID: base.PID, Token: base.Token, CreatedAt: time.Unix(1, 0)}, true},
		{"legacy live owner", lockRecord{PID: base.PID, Token: base.Token, CreatedAt: base.CreatedAt}, false},
		{"legacy unknown creation time", lockRecord{PID: base.PID, Token: base.Token}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "relayd.lock")
			raw, err := json.Marshal(test.record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			lock, err := AcquireLock(context.Background(), path, false)
			if !test.stale {
				if !errors.Is(err, ErrLockHeld) {
					t.Fatalf("live owner: %v", err)
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != string(raw) {
					t.Fatal("live owner's lock changed")
				}
				return
			}
			if err != nil {
				t.Fatalf("reclaim reused PID: %v", err)
			}
			defer lock.Release()
			current, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var record lockRecord
			if err := json.Unmarshal(current, &record); err != nil {
				t.Fatal(err)
			}
			if record.BootID != bootID || record.ProcessStart != processStart || record.Token == test.record.Token {
				t.Fatalf("replacement identity = %#v", record)
			}
			if _, err := AcquireLock(context.Background(), path, false); !errors.Is(err, ErrLockHeld) {
				t.Fatalf("replacement lock is not exclusive: %v", err)
			}
		})
	}
}

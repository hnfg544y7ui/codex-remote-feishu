package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpgradeHelperLaunchGatePreventsJournalAccessUntilCommit(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "install-state.json")
	original := []byte("not-valid-journal-json")
	if err := os.WriteFile(statePath, original, 0600); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(dir, "receipt")
	for _, mode := range []string{"canceled", "deadline", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(upgradeHelperStartGateEnv, gate)
			_ = os.Remove(gate)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			if mode == "rejected" {
				_ = os.WriteFile(gate, []byte("cancel"), 0600)
			}
			err := RunUpgradeHelperWithStatePath(ctx, statePath)
			if err == nil || !strings.Contains(err.Error(), "ownership gate") {
				t.Fatalf("premature journal access error=%v", err)
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if mode == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(statePath)
			if string(data) != string(original) {
				t.Fatal("journal changed before ownership commit")
			}
		})
	}
	t.Setenv(upgradeHelperStartGateEnv, gate)
	if err := os.WriteFile(gate, []byte("committed"), 0600); err != nil {
		t.Fatal(err)
	}
	err := RunUpgradeHelperWithStatePath(context.Background(), statePath)
	if err == nil || strings.Contains(err.Error(), "ownership gate") {
		t.Fatalf("committed helper did not enter journal load: %v", err)
	}
	if os.Getenv(upgradeHelperStartGateEnv) != "" {
		t.Fatal("startup gate leaked into replacement daemon environment")
	}
}

func TestUpgradeHelperLaunchGateWaitsForDelayedCommit(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "receipt")
	t.Setenv(upgradeHelperStartGateEnv, gate)
	done := make(chan error, 1)
	go func() { done <- waitForUpgradeHelperLaunch(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("gate returned before commit: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	if err := os.WriteFile(gate, []byte("committed"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("committed gate did not release")
	}
}

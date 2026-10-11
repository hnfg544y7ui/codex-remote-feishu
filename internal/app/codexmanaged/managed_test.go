package codexmanaged

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func fakeManagedOperations(latest string) managedOperations {
	legacy := fakeOperations(latest)
	return managedOperations{
		lookup:      func(context.Context, string) (string, error) { return latest, nil },
		readVersion: legacy.readVersion,
		install: func(ctx context.Context, prefix, target string) error {
			if err := writeFakeInstall(prefix, target); err != nil {
				return err
			}
			binary, err := installedBinary(prefix)
			if err != nil {
				return err
			}
			for _, path := range componentPaths(binary, runtime.GOOS)[1:] {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte("official bundled helper"), 0o700); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func TestManagedBootstrapWithoutConfiguredCLIAndOfflineReuse(t *testing.T) {
	root := t.TempDir()
	ops := fakeManagedOperations("0.162.1")
	release, err := ensureManaged(context.Background(), root, Options{}, ops)
	if err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(root)
	if err != nil || state.Active == nil || state.Active.Version != "0.162.1" || state.Candidate != nil {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	// Existing CLI must remain usable even if npm is missing, registry offline,
	// the App was removed, and the registry's latest version changed.
	ops.lookup = func(context.Context, string) (string, error) {
		t.Fatal("offline reuse consulted registry")
		return "", errors.New("offline")
	}
	ops.install = func(context.Context, string, string) error {
		t.Fatal("offline reuse invoked npm")
		return errors.New("missing npm")
	}
	again, err := ensureManaged(context.Background(), root, Options{Version: "0.999.0"}, ops)
	if err != nil || again.Binary != release.Binary {
		t.Fatalf("offline reuse=%+v err=%v", again, err)
	}
	resolved, err := resolveManaged(context.Background(), root, ops)
	if err != nil || resolved.Binary != release.Binary {
		t.Fatalf("resolve=%+v err=%v", resolved, err)
	}
}

func TestManagedPrepareDoesNotSwitchAndRollbackRejectsBadRelease(t *testing.T) {
	root := t.TempDir()
	ops := fakeManagedOperations("0.161.0")
	first, err := ensureManaged(context.Background(), root, Options{}, ops)
	if err != nil {
		t.Fatal(err)
	}
	ops.lookup = func(context.Context, string) (string, error) { return "0.162.1", nil }
	second, err := prepareManaged(context.Background(), root, Options{}, ops)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := Inspect(root)
	if state.Active.Version != first.Version || state.Candidate.Version != second.Version {
		t.Fatalf("preparation changed active: %+v", state)
	}
	if _, err := Activate(root, second.Version); err != nil {
		t.Fatal(err)
	}
	state, _ = Inspect(root)
	if state.Previous.Version != first.Version || state.Active.Version != second.Version || state.Candidate != nil {
		t.Fatalf("activation=%+v", state)
	}
	restored, err := Rollback(root)
	if err != nil || restored.Version != first.Version {
		t.Fatalf("rollback=%+v err=%v", restored, err)
	}
	if _, err := prepareManaged(context.Background(), root, Options{}, ops); err == nil || !strings.Contains(err.Error(), "explicit retry") {
		t.Fatalf("rejected release selected again: %v", err)
	}
	if _, err := Activate(root, second.Version); err == nil {
		t.Fatal("rejected release activated")
	}
	resolved, err := resolveManaged(context.Background(), root, ops)
	if err != nil || resolved.Version != first.Version {
		t.Fatalf("rollback lost lower-version selection: %+v %v", resolved, err)
	}
	if _, err := prepareManaged(context.Background(), root, Options{RetryFailed: true}, ops); err != nil {
		t.Fatal(err)
	}
	if _, err := Activate(root, second.Version); err != nil {
		t.Fatal(err)
	}
}

func TestManagedPrepareOnlyPublishesCandidateAndRejectKeepsActive(t *testing.T) {
	root := t.TempDir()
	ops := fakeManagedOperations("0.162.1")
	release, err := prepareManaged(context.Background(), root, Options{}, ops)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveManaged(context.Background(), root, ops); err == nil {
		t.Fatal("unprobed candidate became active")
	}
	if err := Reject(root, release.Version, "app-server protocol mismatch"); err != nil {
		t.Fatal(err)
	}
	state, _ := Inspect(root)
	if state.Active != nil || state.Candidate != nil || state.Failed[release.Version].Reason == "" {
		t.Fatalf("rejection=%+v", state)
	}
	if _, err := Activate(root, release.Version); err == nil {
		t.Fatal("probe failure activated")
	}
}

func TestManagedConcurrentBootstrapInstallsOnlyOnce(t *testing.T) {
	root := t.TempDir()
	ops := fakeManagedOperations("0.162.1")
	install := ops.install
	var count atomic.Int32
	ops.install = func(ctx context.Context, prefix, target string) error {
		count.Add(1)
		return install(ctx, prefix, target)
	}
	var wg sync.WaitGroup
	results := make(chan error, 24)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ensureManaged(context.Background(), root, Options{}, ops)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count.Load() != 1 {
		t.Fatalf("installed %d times", count.Load())
	}
	if _, err := Inspect(root); err != nil {
		t.Fatal(err)
	}
}

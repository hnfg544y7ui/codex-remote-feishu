package codexmanaged

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureGuardWaitsBeforeReadingPartiallyPublishedPIDLock(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		want    error
	}{
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 50*time.Millisecond)
		}, context.DeadlineExceeded},
		{"canceled", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}, context.Canceled},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			guard, err := lockManagedFile(context.Background(), root, "private-update.guard")
			if err != nil {
				t.Fatal(err)
			}
			defer guard.release()
			// A competing installer has created the PID file but has not yet
			// published its JSON. No caller holding out on the guard may read it.
			pidPath := filepath.Join(root, "update.lock")
			if err := os.WriteFile(pidPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(pidPath)
			if err != nil {
				t.Fatal(err)
			}
			ops := fakeOperations("0.159.3")
			ops.lookup = func(context.Context) (string, error) { t.Fatal("registry used without lock ownership"); return "", nil }
			ops.install = func(context.Context, string, string) error {
				t.Fatal("installation used without lock ownership")
				return nil
			}
			ops.readVersion = func(context.Context, string) (string, error) {
				t.Fatal("binary executed without lock ownership")
				return "", nil
			}
			ctx, cancel := testCase.context()
			defer cancel()
			binary, err := ensure(ctx, "missing-configured-codex", root, ops)
			if binary != "" || !errors.Is(err, testCase.want) {
				t.Fatalf("binary=%q error=%v, expected guard wait %v", binary, err, testCase.want)
			}
			raw, err := os.ReadFile(pidPath)
			if err != nil || len(raw) != 0 {
				t.Fatalf("pending PID file changed: %q %v", raw, err)
			}
			after, err := os.Stat(pidPath)
			if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("pending PID file deleted or replaced: %v", err)
			}
		})
	}
}

func TestEnsureLegacyLockFailureReleasesKernelGuard(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "update.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ensure(context.Background(), "unused-configured-codex", root, fakeOperations("0.159.3"))
	if err == nil || !strings.Contains(err.Error(), "lock private Codex update") {
		t.Fatalf("legacy PID-lock behavior changed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	guard, err := lockManagedFile(ctx, root, "private-update.guard")
	if err != nil {
		t.Fatalf("failed legacy acquisition leaked kernel guard: %v", err)
	}
	guard.release()
}

func TestEnsureVerificationFailureReleasesBothLocks(t *testing.T) {
	root := t.TempDir()
	configured := configuredFile(t, "custom-version")
	_, err := ensure(context.Background(), configured, root, fakeOperations("0.159.3"))
	if err == nil {
		t.Fatal("invalid binary version accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "update.lock")); !os.IsNotExist(err) {
		t.Fatalf("legacy PID lock survived failed validation: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	guard, err := lockManagedFile(ctx, root, "private-update.guard")
	if err != nil {
		t.Fatalf("failed validation leaked kernel guard: %v", err)
	}
	guard.release()
}

package codexmanaged

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManagedLatestChannelsAndPinnedVersion(t *testing.T) {
	for _, tc := range []struct {
		name      string
		options   Options
		latest    string
		wantError bool
	}{
		{"stable", Options{}, "0.162.1", false},
		{"alpha", Options{Channel: "alpha"}, "0.163.0-alpha.5", false},
		{"stable rejects preview", Options{}, "0.163.0-alpha.5", true},
		{"alpha rejects stable", Options{Channel: "alpha"}, "0.162.1", true},
		{"invalid channel", Options{Channel: "beta"}, "0.162.1", true},
		{"invalid path pin", Options{Version: "../../outside"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := fakeManagedOperations(tc.latest)
			_, err := latestManaged(context.Background(), tc.options, ops)
			if (err != nil) != tc.wantError {
				t.Fatalf("err=%v", err)
			}
		})
	}
	ops := fakeManagedOperations("")
	ops.lookup = func(context.Context, string) (string, error) {
		t.Fatal("pin consulted registry")
		return "", errors.New("offline")
	}
	version, err := latestManaged(context.Background(), Options{Version: "0.163.0-alpha.5"}, ops)
	if err != nil || version != "0.163.0-alpha.5" {
		t.Fatalf("pin=%s err=%v", version, err)
	}
}

func TestManagedMissingComponentsAreNeverPublished(t *testing.T) {
	root := t.TempDir()
	ops := fakeManagedOperations("0.162.1")
	ops.install = func(_ context.Context, prefix, target string) error { return writeFakeInstall(prefix, target) }
	if _, err := prepareManaged(context.Background(), root, Options{}, ops); err == nil || !strings.Contains(err.Error(), "incomplete official") {
		t.Fatalf("missing helper accepted: %v", err)
	}
	state, _ := Inspect(root)
	if state.Active != nil || state.Candidate != nil {
		t.Fatalf("invalid install selected: %+v", state)
	}
	if _, err := os.Stat(filepath.Join(root, "releases", "0.162.1")); !os.IsNotExist(err) {
		t.Fatalf("invalid release published: %v", err)
	}
	if staging, _ := filepath.Glob(filepath.Join(root, ".managed-install-*")); len(staging) != 0 {
		t.Fatalf("staging leaked: %v", staging)
	}
}

func TestManagedActivationChecksImmutableComponents(t *testing.T) {
	root := t.TempDir()
	ops := fakeManagedOperations("0.162.1")
	release, err := prepareManaged(context.Background(), root, Options{}, ops)
	if err != nil {
		t.Fatal(err)
	}
	helper := componentPaths(release.Binary, runtime.GOOS)[1]
	if err := os.WriteFile(helper, []byte("altered helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Activate(root, release.Version); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("corrupt component activated: %v", err)
	}
	if _, err := prepareManaged(context.Background(), root, Options{}, ops); err == nil {
		t.Fatal("corrupt immutable release overwritten")
	}
	state, _ := Inspect(root)
	if state.Active != nil {
		t.Fatal("active changed after failed verification")
	}
}

func TestManagedResolveFailsWhenActiveComponentMissing(t *testing.T) {
	root := t.TempDir()
	ops := fakeManagedOperations("0.162.1")
	release, err := ensureManaged(context.Background(), root, Options{}, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(componentPaths(release.Binary, runtime.GOOS)[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveManaged(context.Background(), root, ops); err == nil {
		t.Fatal("incomplete active release selected")
	}
}

func TestManagedLockHandlesEmptyFileAndCancellation(t *testing.T) {
	root := t.TempDir()
	// No record is interpreted; an empty persistent lock file is a normal state.
	if err := os.WriteFile(filepath.Join(root, "managed.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := lockManaged(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if second, err := lockManaged(ctx, root); !errors.Is(err, context.DeadlineExceeded) {
		if second != nil {
			second.release()
		}
		t.Fatalf("contending lock=%v", err)
	}
	lock.release()
	third, err := lockManaged(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	third.release()
}

func TestManagedComponentsMatchOfficialPlatformBundles(t *testing.T) {
	for _, tc := range []struct {
		goos, binary string
		count        int
		required     string
	}{
		{"linux", filepath.Join("prefix", "bin", "codex"), 3, filepath.Join("prefix", "codex-resources", "bwrap")},
		{"windows", filepath.Join("prefix", "bin", "codex.exe"), 4, filepath.Join("prefix", "codex-resources", "codex-command-runner.exe")},
		{"darwin", filepath.Join("prefix", "bin", "codex"), 2, filepath.Join("prefix", "bin", "codex-code-mode-host")},
	} {
		paths := componentPaths(tc.binary, tc.goos)
		if len(paths) != tc.count {
			t.Fatalf("%s components=%v", tc.goos, paths)
		}
		found := false
		for _, path := range paths {
			if path == tc.required {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s missing %s: %v", tc.goos, tc.required, paths)
		}
	}
}

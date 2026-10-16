//go:build !windows

package sharedbackend

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHostExportUnixPrivateModesAndUnsafeExistingPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sources")
	m, err := Export(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{root: 0o700, m.Directory: 0o700, m.Entrypoint: 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("private mode mismatch for %s: info=%v error=%v", path, info, err)
		}
	}
	if err := os.Chmod(m.Entrypoint, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(context.Background(), root); err == nil {
		t.Fatal("public existing source permissions accepted")
	}
	info, err := os.Stat(m.Entrypoint)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatal("unsafe existing source was rewritten")
	}
}

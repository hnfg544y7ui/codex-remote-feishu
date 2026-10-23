package sharedbackend

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHostExportCompleteImmutablePackageAndReuse(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sources")
	first, err := Export(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"app_environment.py", "app_windows.py", "bootstrap.py", "host.py", "immediate_upgrade.py", "owner.py", "runtime_environment.py", "runtime_selection.py"}
	if first.SchemaVersion != 1 || len(first.Files) != len(want) || filepath.Base(first.Directory) != first.SHA256 {
		t.Fatalf("invalid manifest: %+v", first)
	}
	for _, name := range want {
		if first.Files[name] == "" {
			t.Errorf("missing embedded source: %s", name)
		}
		if err := privatePath(filepath.Join(first.Directory, name), false); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.Stat(first.Entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Export(context.Background(), root)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("reuse manifest=%+v error=%v", second, err)
	}
	after, err := os.Stat(first.Entrypoint)
	if err != nil || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("existing immutable package was rewritten: %v", err)
	}
}

func TestHostExportRefusesCorruptionWithoutRepair(t *testing.T) {
	for _, corruption := range []string{"contents", "extra", "missing"} {
		t.Run(corruption, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "sources")
			m, err := Export(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			path := m.Entrypoint
			switch corruption {
			case "contents":
				err = os.WriteFile(path, []byte("tampered"), 0o600)
			case "extra":
				err = os.WriteFile(filepath.Join(m.Directory, "unexpected.py"), []byte("extra"), 0o600)
			case "missing":
				err = os.Remove(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Export(context.Background(), root); err == nil {
				t.Fatal("corrupt package was accepted")
			}
			if corruption == "contents" {
				raw, _ := os.ReadFile(path)
				if string(raw) != "tampered" {
					t.Fatal("corrupt file was silently replaced")
				}
			}
		})
	}
}

func TestHostExportRejectsSymlinkPaths(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "actual")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Export(context.Background(), filepath.Join(link, "nested")); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
	root := filepath.Join(base, "valid")
	m, err := Export(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(m.Entrypoint); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(m.Bootstrap, m.Entrypoint); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(context.Background(), root); err == nil {
		t.Fatal("symlink source component accepted")
	}
}

func TestHostExportRequiresExplicitRootAndHonorsCancellation(t *testing.T) {
	if _, err := Export(context.Background(), ""); err == nil {
		t.Fatal("empty root accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Export(ctx, filepath.Join(t.TempDir(), "sources")); err == nil {
		t.Fatal("cancellation ignored")
	}
}

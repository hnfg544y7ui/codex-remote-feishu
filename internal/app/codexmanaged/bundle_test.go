package codexmanaged

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func fakeBundle(t *testing.T, goos, layout string) (string, bundleVersionReader) {
	t.Helper()
	root := t.TempDir()
	base := root
	if layout == "npm-vendor" {
		base = filepath.Join(root, "bin")
	}
	name := "codex"
	if goos == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(base, name)
	_, paths, err := bundleLayout(binary, layout, goos)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range paths {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("complete bundle "+relative), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return binary, func(context.Context, string) (string, error) { return "0.163.0", nil }
}

func TestBundleSnapshotsCoverPlatformLayoutsWithoutSelecting(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		for _, layout := range []string{"flat", "npm-vendor"} {
			t.Run(goos+"/"+layout, func(t *testing.T) {
				binary, reader := fakeBundle(t, goos, layout)
				snapshot, err := snapshotBundle(context.Background(), binary, "0.163.0", goos, reader)
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.Layout != layout || snapshot.SchemaVersion != 1 {
					t.Fatalf("snapshot=%+v", snapshot)
				}
				count := map[string]int{"windows": 4, "linux": 3, "darwin": 2}[goos]
				if len(snapshot.Components) != count {
					t.Fatalf("incomplete platform contract: %+v", snapshot.Components)
				}
				verified, err := verifyBundle(context.Background(), snapshot, goos, reader)
				if err != nil || !reflect.DeepEqual(snapshot, verified) {
					t.Fatalf("verify=%+v error=%v", verified, err)
				}
				for _, name := range []string{"managed.json", "selection.json", "remote-release.json"} {
					if _, err := os.Stat(filepath.Join(snapshot.BundleRoot, name)); !os.IsNotExist(err) {
						t.Fatalf("read-only operation wrote %s: %v", name, err)
					}
				}
			})
		}
	}
}

func TestBundleEveryComponentRequiredAndChecksummed(t *testing.T) {
	for _, goos := range []string{"windows", "linux"} {
		for _, layout := range []string{"flat", "npm-vendor"} {
			for _, mutation := range []string{"missing", "changed", "manifest missing", "empty"} {
				t.Run(goos+"/"+layout+"/"+mutation, func(t *testing.T) {
					binary, reader := fakeBundle(t, goos, layout)
					snapshot, err := snapshotBundle(context.Background(), binary, "0.163.0", goos, reader)
					if err != nil {
						t.Fatal(err)
					}
					for relative, checksum := range snapshot.Components {
						path := filepath.Join(snapshot.BundleRoot, relative)
						original, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						switch mutation {
						case "missing":
							err = os.Remove(path)
						case "changed":
							err = os.WriteFile(path, []byte("different component"), 0o700)
						case "empty":
							err = os.WriteFile(path, nil, 0o700)
						case "manifest missing":
							delete(snapshot.Components, relative)
						}
						if err != nil {
							t.Fatal(err)
						}
						called := false
						_, err = verifyBundle(context.Background(), snapshot, goos, func(context.Context, string) (string, error) {
							called = true
							return "0.163.0", nil
						})
						if err == nil || called {
							t.Fatalf("invalid %s accepted or executed CLI: err=%v called=%v", relative, err, called)
						}
						if err := os.WriteFile(path, original, 0o700); err != nil {
							t.Fatal(err)
						}
						snapshot.Components[relative] = checksum
					}
				})
			}
		}
	}
}

func TestBundleRejectsInvalidIdentityAndPaths(t *testing.T) {
	mutations := map[string]func(*BundleSnapshot){
		"schema":             func(s *BundleSnapshot) { s.SchemaVersion++ },
		"version":            func(s *BundleSnapshot) { s.Version = "0.162.1" },
		"layout":             func(s *BundleSnapshot) { s.Layout = "guessed" },
		"wrong root":         func(s *BundleSnapshot) { s.BundleRoot = filepath.Dir(s.BundleRoot) },
		"relative root":      func(s *BundleSnapshot) { s.BundleRoot = "." },
		"relative binary":    func(s *BundleSnapshot) { s.Binary = "codex" },
		"outside component":  func(s *BundleSnapshot) { s.Components[filepath.Join("..", "outside")] = strings.Repeat("0", 64) },
		"absolute component": func(s *BundleSnapshot) { s.Components[s.Binary] = strings.Repeat("0", 64) },
		"invalid hash": func(s *BundleSnapshot) {
			for key := range s.Components {
				s.Components[key] = strings.Repeat("z", 64)
				break
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			binary, reader := fakeBundle(t, "linux", "flat")
			snapshot, err := snapshotBundle(context.Background(), binary, "0.163.0", "linux", reader)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&snapshot)
			if _, err := verifyBundle(context.Background(), snapshot, "linux", reader); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestBundleSnapshotRejectsSingleBinaryWrongVersionAndCancellation(t *testing.T) {
	binary, reader := fakeBundle(t, "linux", "flat")
	if _, err := snapshotBundle(context.Background(), binary, "0.162.1", "linux", reader); err == nil {
		t.Fatal("wrong CLI version accepted")
	}
	if err := os.Remove(filepath.Join(filepath.Dir(binary), "codex-code-mode-host")); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotBundle(context.Background(), binary, "0.163.0", "linux", reader); err == nil {
		t.Fatal("single or incomplete binary accepted")
	}
	binary, reader = fakeBundle(t, "linux", "flat")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := snapshotBundle(ctx, binary, "0.163.0", "linux", reader); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestBundleRejectsSymlinkedComponent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires host privilege")
	}
	binary, reader := fakeBundle(t, "linux", "npm-vendor")
	snapshot, err := snapshotBundle(context.Background(), binary, "0.163.0", "linux", reader)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(snapshot.BundleRoot, "bin", "codex-code-mode-host")
	outside := filepath.Join(t.TempDir(), "helper")
	raw, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(helper); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, helper); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBundle(context.Background(), snapshot, "linux", reader); err == nil {
		t.Fatal("symlink escaped bundle root")
	}
}

func TestBundleRejectsNonExecutableAndLinkedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions and symlinks")
	}
	binary, reader := fakeBundle(t, "linux", "npm-vendor")
	snapshot, err := snapshotBundle(context.Background(), binary, "0.163.0", "linux", reader)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(snapshot.BundleRoot, "bin", "codex-code-mode-host")
	if err := os.Chmod(helper, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBundle(context.Background(), snapshot, "linux", reader); err == nil {
		t.Fatal("non-executable helper accepted")
	}
	if err := os.Chmod(helper, 0o700); err != nil {
		t.Fatal(err)
	}
	resources := filepath.Join(snapshot.BundleRoot, "codex-resources")
	relocated := filepath.Join(t.TempDir(), "relocated-resources")
	if err := os.Rename(resources, relocated); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relocated, resources); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBundle(context.Background(), snapshot, "linux", reader); err == nil {
		t.Fatal("symlinked directory escaped bundle root")
	}
}

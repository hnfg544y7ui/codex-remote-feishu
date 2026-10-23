package codexruntimecli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	sharedbackend "github.com/kxn/codex-remote-feishu/scripts/shared-backend"
)

func TestHostExportCLIDispatchAndManifest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := filepath.Join(t.TempDir(), "host-sources")
	if err := Run(context.Background(), []string{"host-export", "--directory", root}, &stdout, &stderr, "test"); err != nil {
		t.Fatal(err)
	}
	var manifest sharedbackend.Manifest
	if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	want := []string{"app_environment.py", "app_windows.py", "bootstrap.py", "host.py", "immediate_upgrade.py", "owner.py", "runtime_environment.py", "runtime_selection.py"}
	if len(manifest.Files) != len(want) || manifest.SchemaVersion != 1 || manifest.Directory != filepath.Join(root, manifest.SHA256) || stderr.Len() != 0 {
		t.Fatalf("invalid manifest=%+v stderr=%s", manifest, stderr.String())
	}
	for _, name := range want {
		content, err := os.ReadFile(filepath.Join(manifest.Directory, name))
		if err != nil {
			t.Fatalf("exported source %s is missing: %v", name, err)
		}
		if got := manifest.Files[name]; got != fmt.Sprintf("%x", sha256.Sum256(content)) {
			t.Errorf("exported source %s checksum mismatch: %q", name, got)
		}
	}
}

func TestHostExportCLIRejectsImplicitOrAmbiguousRoots(t *testing.T) {
	for _, args := range [][]string{nil, {"--directory", ""}, {"--directory", "root", "extra"}} {
		if err := runHostExport(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
}

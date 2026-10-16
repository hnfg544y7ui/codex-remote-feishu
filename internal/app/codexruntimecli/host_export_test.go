package codexruntimecli

import (
	"bytes"
	"context"
	"encoding/json"
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
	if len(manifest.Files) != 7 || manifest.SchemaVersion != 1 || manifest.Directory != filepath.Join(root, manifest.SHA256) || stderr.Len() != 0 {
		t.Fatalf("invalid manifest=%+v stderr=%s", manifest, stderr.String())
	}
}

func TestHostExportCLIRejectsImplicitOrAmbiguousRoots(t *testing.T) {
	for _, args := range [][]string{nil, {"--directory", ""}, {"--directory", "root", "extra"}} {
		if err := runHostExport(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
}

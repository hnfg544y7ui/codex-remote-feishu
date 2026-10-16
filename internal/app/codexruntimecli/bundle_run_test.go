package codexruntimecli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/app/codexmanaged"
)

func TestBundleCLIProducesSnapshotWithExactEnvironment(t *testing.T) {
	env := []string{"PATH=explicit-runtime"}
	want := codexmanaged.BundleSnapshot{SchemaVersion: 1, Binary: "native", Version: "0.163.0", Layout: "flat"}
	var stdout bytes.Buffer
	err := runBundle(context.Background(), []string{"bundle", "--binary", "native", "--version", "0.163.0"}, &stdout, &bytes.Buffer{}, env, bundleOperations{
		snapshot: func(ctx context.Context, binary, version string, got []string) (codexmanaged.BundleSnapshot, error) {
			if _, bounded := ctx.Deadline(); !bounded || binary != "native" || version != "0.163.0" || !reflect.DeepEqual(got, env) {
				t.Fatalf("unexpected bundle arguments: binary=%q version=%q env=%v", binary, version, got)
			}
			return want, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got codexmanaged.BundleSnapshot
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot=%+v error=%v", got, err)
	}
}

func TestVerifyBundleCLIAcceptsOwnerMetadataWithoutWritingFiles(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "selection.json")
	raw := []byte(`{"schemaVersion":1,"binary":"native","version":"0.163.0","bundleRoot":"root","layout":"flat","components":{"codex":"digest"},"sha256":"owner-checksum","previous":{"version":"0.162.1"}}`)
	if err := os.WriteFile(manifest, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	err := runBundle(context.Background(), []string{"verify-bundle", "--manifest", manifest}, &stdout, &bytes.Buffer{}, []string{"preserved=env"}, bundleOperations{
		verify: func(_ context.Context, snapshot codexmanaged.BundleSnapshot, env []string) (codexmanaged.BundleSnapshot, error) {
			if snapshot.Binary != "native" || snapshot.Components["codex"] != "digest" || len(env) != 1 {
				t.Fatalf("snapshot=%+v env=%v", snapshot, env)
			}
			return snapshot, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "owner-checksum") || strings.Contains(stdout.String(), "previous") {
		t.Fatalf("extra owner metadata leaked into canonical snapshot: %s", stdout.String())
	}
	current, err := os.ReadFile(manifest)
	if err != nil || !bytes.Equal(raw, current) {
		t.Fatalf("selection changed: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected writes: %v %v", entries, err)
	}
}

func TestBundleCLIRejectsBadArgumentsAndInvalidJSON(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(manifest, []byte(`{"version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"bundle"}, {"bundle", "--binary", "native"}, {"bundle", "--version", "0.163.0"},
		{"bundle", "--binary", "native", "--version", "0.163.0", "extra"},
		{"verify-bundle"}, {"verify-bundle", "--manifest", manifest},
	} {
		if err := runBundle(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}, nil, bundleOperations{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestRunDispatchesBundleWithoutNormalRuntimePaths(t *testing.T) {
	for _, command := range []string{"bundle", "verify-bundle"} {
		var stderr bytes.Buffer
		if err := Run(context.Background(), []string{command, "--help"}, &bytes.Buffer{}, &stderr, "test"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stderr.String(), "codex-runtime "+command) {
			t.Fatalf("help=%s", stderr.String())
		}
	}
}

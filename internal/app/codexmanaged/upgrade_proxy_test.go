package codexmanaged

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpgradeProxyModeIsNotPersistedAndInvalidPolicyDoesNotCreateRoot(t *testing.T) {
	data, err := json.Marshal(Options{Channel: "stable", UpgradeProxyMode: "direct"})
	if err != nil || strings.Contains(string(data), "direct") {
		t.Fatalf("network policy persisted: %s %v", data, err)
	}
	root := filepath.Join(t.TempDir(), "uncreated")
	if _, err := Prepare(context.Background(), root, Options{Version: "0.163.0", UpgradeProxyMode: "bad"}, nil); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("invalid mode mutated runtime: %v", err)
	}
}

func TestUpgradeDirectScopesProxyToNPMAndKeepsNativeEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell tools are Unix-only; Windows proxy integration covered in upgradeproxy")
	}
	tools := t.TempDir()
	scripts := map[string]string{
		"npm":   "#!/bin/sh\n[ -z \"$HTTPS_PROXY\" ] || exit 2\n[ \"$CODEX_HOME\" = \"/native/home\" ] || exit 3\n",
		"codex": "#!/bin/sh\n[ \"$HTTPS_PROXY\" = \"http://model-proxy:4567\" ] || exit 2\n[ \"$CODEX_HOME\" = \"/native/home\" ] || exit 3\nprintf 'codex-cli 0.163.0\\n'\n",
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(tools, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PATH=" + tools, "CODEX_HOME=/native/home", "HTTPS_PROXY=http://model-proxy:4567"}
	ops, err := managedUpgradeOperations("direct", env)
	if err != nil {
		t.Fatal(err)
	}
	if err := ops.install(context.Background(), t.TempDir(), "0.163.0"); err != nil {
		t.Fatal(err)
	}
	if actual, err := ops.readVersion(context.Background(), filepath.Join(tools, "codex")); err != nil || actual != "0.163.0" {
		t.Fatalf("native validation environment changed: %s %v", actual, err)
	}
	if env[2] != "HTTPS_PROXY=http://model-proxy:4567" {
		t.Fatal("input environment modified")
	}
}

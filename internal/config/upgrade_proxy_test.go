package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeProxyConfigDefaultAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"feishu":{"useSystemProxy":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAppConfigAtPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Upgrade.ProxyMode != UpgradeProxySystem {
		t.Fatalf("default = %q", loaded.Config.Upgrade.ProxyMode)
	}
	for _, mode := range []string{"system", "direct", " SYSTEM "} {
		loaded.Config.Upgrade.ProxyMode = mode
		if err := WriteAppConfig(path, loaded.Config); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadAppConfigAtPath(path); err != nil {
			t.Fatal(err)
		}
	}
	loaded.Config.Upgrade.ProxyMode = "bogus"
	before, _ := os.ReadFile(path)
	if err := WriteAppConfig(path, loaded.Config); err == nil {
		t.Fatal("invalid write allowed")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid write changed config")
	}
	if err := os.WriteFile(path, []byte(`{"upgrade":{"proxyMode":"bogus"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAppConfigAtPath(path); err == nil {
		t.Fatal("invalid read allowed")
	}
}

func TestWriteUpgradeProxyModePreservesUnrelatedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{"version":2,"feishu":{"useSystemProxy":false,"apps":[{"appID":"test","appSecret":"keep-secret"}]},"unknown":{"credential":"preserve"},"upgrade":{"futureOption":42}}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{UpgradeProxyDirect, UpgradeProxySystem} {
		if err := WriteUpgradeProxyMode(path, mode); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadAppConfigAtPath(path)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Config.Upgrade.ProxyMode != mode {
			t.Fatalf("mode = %q", loaded.Config.Upgrade.ProxyMode)
		}
		raw, _ := os.ReadFile(path)
		for _, sentinel := range []string{"keep-secret", "preserve", "futureOption", `"useSystemProxy": false`} {
			if !strings.Contains(string(raw), sentinel) {
				t.Fatalf("lost unrelated field %q", sentinel)
			}
		}
	}
	before, _ := os.ReadFile(path)
	if err := WriteUpgradeProxyMode(path, "bogus"); err == nil {
		t.Fatal("invalid mode allowed")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid setting changed config")
	}
}

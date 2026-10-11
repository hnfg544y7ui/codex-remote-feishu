package wrapper

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/config"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

func TestManagedRuntimeLoadsWithoutPATHOrDesktopCLI(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	t.Setenv("PATH", filepath.Join(home, "empty-path"))
	t.Setenv(config.XDGConfigHomeEnv, filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv(config.CodexRuntimeSourceEnv, config.CodexRuntimeSourceManaged)
	t.Setenv(config.CodexRealBinaryEnv, "")
	path := filepath.Join(home, "config.json")
	cfg := config.DefaultAppConfig()
	cfg.Wrapper.CodexRealBinary = filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-deleted", "bin", "linux-x86_64", "codex")
	if err := config.WriteAppConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.UnifiedConfigEnvPath, path)
	loaded, err := LoadConfig([]string{"app-server"}, "test", "test")
	if err != nil {
		t.Fatalf("managed runtime depends on missing desktop CLI: %v", err)
	}
	if loaded.CodexRuntime.Source != "managed" {
		t.Fatalf("lost source: %#v", loaded.CodexRuntime)
	}
	stored, err := config.LoadAppConfigAtPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Config.Wrapper.CodexRealBinary != cfg.Wrapper.CodexRealBinary {
		t.Fatal("managed startup rewrote legacy path")
	}
}

func TestExternalRuntimeNeverTriggersPrivateInstall(t *testing.T) {
	app := New(Config{Source: "headless", Managed: true, Lifetime: "daemon-owned", CodexRealBinary: "/external/codex", CodexRuntime: config.CodexRuntimeSettings{Source: "external"}})
	got, err := app.selectCodexChildBinary(context.Background(), nil, func(context.Context, string, string, []string) (string, error) {
		t.Fatal("external runtime triggered private install")
		return "", nil
	})
	if err != nil || got != "/external/codex" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestManagedRuntimeWithoutActiveReleaseDoesNotInstallOrFallback(t *testing.T) {
	root := t.TempDir()
	app := New(Config{CodexRealBinary: "/legacy/codex", CodexRuntime: config.CodexRuntimeSettings{Source: "managed"}, RuntimePaths: relayruntime.Paths{StateDir: root}})
	got, err := app.selectCodexChildBinary(context.Background(), nil, func(context.Context, string, string, []string) (string, error) {
		t.Fatal("managed child tried to install runtime")
		return "", nil
	})
	if err == nil || got != "" {
		t.Fatalf("missing active release fell back to %q: %v", got, err)
	}
}

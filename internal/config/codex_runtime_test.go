package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCodexRuntimeConfigRoundTripPreservesCredentials(t *testing.T) {
	cfg := DefaultAppConfig()
	cfg.Wrapper.CodexRuntime = CodexRuntimeSettings{Source: "managed", Channel: "alpha", Version: "0.135.0-alpha.1", AutoUpdate: true}
	cfg.Feishu.Apps = []FeishuAppConfig{{ID: "test", AppID: "cli_test", AppSecret: "keep-secret"}}
	cfg.VisionAssist = VisionAssistSettings{Protocol: "openai", APIKey: "keep-model-key"}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := WriteAppConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAppConfigAtPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Config.Wrapper.CodexRuntime, cfg.Wrapper.CodexRuntime) {
		t.Fatalf("runtime changed: %#v", loaded.Config.Wrapper.CodexRuntime)
	}
	if loaded.Config.Feishu.Apps[0].AppSecret != "keep-secret" || loaded.Config.VisionAssist.APIKey != "keep-model-key" {
		t.Fatal("runtime persistence removed credentials")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"codexRuntime"`) {
		t.Fatal("missing runtime JSON field")
	}
}

func TestCodexRuntimeEnvironmentOverridesAreNotPersisted(t *testing.T) {
	base := CodexRuntimeSettings{Source: "managed", Channel: "stable", Version: "0.134.0", AutoUpdate: true}
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultAppConfig()
	cfg.Wrapper.CodexRuntime = base
	if err := WriteAppConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv(UnifiedConfigEnvPath, path)
	t.Setenv(CodexRuntimeSourceEnv, "external")
	t.Setenv(CodexRuntimeChannelEnv, "alpha")
	t.Setenv(CodexRuntimeVersionEnv, "")
	t.Setenv(CodexRuntimeAutoUpdateEnv, "false")
	resolved, err := ResolveCodexRuntimeSettings(base)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != "external" || resolved.Channel != "alpha" || resolved.Version != "" || resolved.AutoUpdate {
		t.Fatalf("bad override: %#v", resolved)
	}
	if base.Version != "0.134.0" || !base.AutoUpdate {
		t.Fatal("environment mutated base configuration")
	}
	loaded, err := LoadWrapperConfig()
	if err != nil || !reflect.DeepEqual(loaded.CodexRuntime, resolved) {
		t.Fatalf("wrapper ignored runtime environment: %#v, %v", loaded.CodexRuntime, err)
	}
	stored, err := LoadAppConfigAtPath(path)
	if err != nil || !reflect.DeepEqual(stored.Config.Wrapper.CodexRuntime, base) {
		t.Fatalf("environment overrides persisted into configuration: %#v, %v", stored.Config.Wrapper.CodexRuntime, err)
	}
	t.Setenv(CodexRuntimeAutoUpdateEnv, "sometimes")
	if _, err := ResolveCodexRuntimeSettings(base); err == nil {
		t.Fatal("invalid auto-update boolean accepted")
	}
}

func TestCodexRuntimeRejectsInvalidConfiguration(t *testing.T) {
	for _, settings := range []CodexRuntimeSettings{
		{Source: "auto"}, {Channel: "beta"}, {Version: "latest"},
		{Version: "../codex"}, {Version: "0.1"}, {Version: "0.1.2.3"},
	} {
		t.Run(settings.Source+settings.Channel+settings.Version, func(t *testing.T) {
			cfg := DefaultAppConfig()
			cfg.Wrapper.CodexRuntime = settings
			path := filepath.Join(t.TempDir(), "config.json")
			if err := WriteAppConfig(path, cfg); err == nil {
				t.Fatal("invalid configuration written")
			}
			if err := os.WriteFile(path, []byte(`{"wrapper":{"codexRuntime":{"source":"bad"}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadAppConfigAtPath(path); err == nil {
				t.Fatal("invalid configuration loaded")
			}
		})
	}
	if err := ValidateCodexRuntimeSettings(CodexRuntimeSettings{}); err != nil {
		t.Fatalf("legacy default rejected: %v", err)
	}
	if err := ValidateCodexRuntimeSettings(CodexRuntimeSettings{Source: "legacy"}); err != nil {
		t.Fatal(err)
	}
}

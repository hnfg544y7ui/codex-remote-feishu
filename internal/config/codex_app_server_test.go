package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCodexAppServerConfigRoundTripPreservesCredentials(t *testing.T) {
	cfg := DefaultAppConfig()
	cfg.Wrapper.CodexAppServerURL = " ws://127.0.0.1:17601/ "
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
	if loaded.Config.Wrapper.CodexAppServerURL != "ws://127.0.0.1:17601" {
		t.Fatal("endpoint was not preserved and normalized")
	}
	if !reflect.DeepEqual(loaded.Config.Wrapper.CodexRuntime, cfg.Wrapper.CodexRuntime) ||
		!reflect.DeepEqual(loaded.Config.Feishu.Apps, cfg.Feishu.Apps) ||
		!reflect.DeepEqual(loaded.Config.VisionAssist, cfg.VisionAssist) {
		t.Fatal("shared endpoint persistence changed runtime settings or credentials")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(UnifiedConfigEnvPath, path)
	for _, override := range []string{"ws://[::1]:17602/", ""} {
		t.Setenv(CodexAppServerURLEnv, override)
		wrapper, err := LoadWrapperConfig()
		if err != nil {
			t.Fatal(err)
		}
		services, err := LoadServicesConfig()
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		if override != "" {
			want = "ws://[::1]:17602"
		}
		if wrapper.CodexAppServerURL != want || services.CodexAppServerURL != want {
			t.Fatal("wrapper and services disagree about environment override")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("runtime environment override was persisted")
	}
}

func TestCodexAppServerRejectsInvalidConfigurationAndEnvironment(t *testing.T) {
	for _, endpoint := range []string{
		"ws://192.168.1.2:17601", "ws://localhost:17601", "https://127.0.0.1:17601",
		"ws://127.0.0.1", "ws://127.0.0.1:0", "ws://127.0.0.1:65536",
		"ws://user:pass@127.0.0.1:17601", "ws://127.0.0.1:17601/?token=secret", "ws://127.0.0.1:17601/#x",
	} {
		t.Run(endpoint, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			cfg := DefaultAppConfig()
			cfg.Wrapper.CodexAppServerURL = endpoint
			if err := WriteAppConfig(path, cfg); err == nil {
				t.Fatal("unsafe endpoint written")
			}
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadAppConfigAtPath(path); err == nil {
				t.Fatal("unsafe stored endpoint loaded")
			}
			cfg.Wrapper.CodexAppServerURL = "ws://127.0.0.1:17601"
			if err := WriteAppConfig(path, cfg); err != nil {
				t.Fatal(err)
			}
			t.Setenv(UnifiedConfigEnvPath, path)
			t.Setenv(CodexAppServerURLEnv, endpoint)
			if _, err := LoadWrapperConfig(); err == nil {
				t.Fatal("unsafe environment endpoint fell back to stored endpoint")
			}
			if _, err := LoadServicesConfig(); err == nil {
				t.Fatal("services ignored unsafe environment endpoint")
			}
		})
	}
}

func TestCodexAppServerDefaultRemainsPrivateStdio(t *testing.T) {
	t.Setenv(UnifiedConfigEnvPath, filepath.Join(t.TempDir(), "config.json"))
	t.Setenv(CodexAppServerURLEnv, "")
	wrapper, err := LoadWrapperConfig()
	if err != nil {
		t.Fatal(err)
	}
	services, err := LoadServicesConfig()
	if err != nil {
		t.Fatal(err)
	}
	if wrapper.CodexAppServerURL != "" || services.CodexAppServerURL != "" {
		t.Fatal("shared backend unexpectedly enabled by default")
	}
}

package wrapper

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestSharedThreadSettingsCapabilityRequiresVerifiedSharedVersion(t *testing.T) {
	for _, test := range []struct {
		name, response    string
		shared, supported bool
	}{
		{"native", `{"result":{"userAgent":"codex_cli_rs/0.170.0 (Windows 10)"}}`, true, true},
		{"production-owner", `{"result":{"userAgent":"codex-remote-owner/0.170.0 (Debian 13.0.0; aarch64) unknown (settings-capability-readonly; 1.0)"}}`, true, true},
		{"different-client", `{"result":{"userAgent":"settings-capability-readonly/0.170.1 (Linux) unknown (client; 1.0)"}}`, true, true},
		{"old-owner", `{"result":{"userAgent":"codex-remote-owner/0.169.0 (Linux) unknown (client; 0.170.0)"}}`, true, false},
		{"owner-unknown-version", `{"result":{"userAgent":"codex-remote-owner/futurebad (Linux) unknown (client; 0.170.0)"}}`, true, false},
		{"owner-missing-version", `{"result":{"userAgent":"codex-remote-owner (Linux) unknown (codex/0.170.0)"}}`, true, false},
		{"owner-bad-suffix", `{"result":{"userAgent":"codex-remote-owner/0.170.0futurebad (Linux)"}}`, true, false},
		{"server-info", `{"result":{"serverInfo":{"version":"0.171.0"}}}`, true, true},
		{"old", `{"result":{"userAgent":"codex_cli_rs/0.169.9"}}`, true, false},
		{"unknown", `{"result":{"userAgent":"shared-test"}}`, true, false},
		{"not-shared", `{"result":{"userAgent":"codex_cli_rs/0.170.0"}}`, false, false},
		{"malformed", `{`, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &codexBackendRuntime{sharedBackend: test.shared, sharedThreadSettings: true}
			runtime.observeSharedSettingsVersion([]byte(test.response))
			if got := runtime.Capabilities().SharedThreadSettings; got != test.supported {
				t.Fatalf("supported=%v, want %v", got, test.supported)
			}
			if !test.supported {
				if _, err := runtime.TranslateCommand(agentproto.Command{Kind: agentproto.CommandThreadSettingsSet}); err == nil {
					t.Fatal("unsupported runtime accepted thread settings mutation")
				}
			}
		})
	}
}

package wrapper

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kxn/codex-remote-feishu/internal/adapter/relayws"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestSharedWrapperRunHelloUsesCompletedNativeHandshakeCapabilities(t *testing.T) {
	for _, version := range []string{"0.170.0", "0.169.0"} {
		t.Run(version, func(t *testing.T) {
			ownerURL := sharedTestServer(t, func(conn *websocket.Conn) {
				var initialize, initialized, resume map[string]any
				if conn.ReadJSON(&initialize) != nil {
					return
				}
				if conn.WriteJSON(map[string]any{"id": initialize["id"], "result": map[string]any{
					"userAgent": fmt.Sprintf("codex-remote-owner/%s (Debian 13.0.0; aarch64) unknown (settings-capability-readonly; 1.0)", version),
				}}) != nil {
					return
				}
				if conn.ReadJSON(&initialized) != nil || conn.ReadJSON(&resume) != nil {
					return
				}
				if conn.WriteJSON(map[string]any{"id": resume["id"], "result": map[string]any{
					"thread": map[string]any{"id": "native-thread"}, "model": "native-model", "reasoningEffort": nil,
				}}) != nil {
					return
				}
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			})
			helloCh := make(chan agentproto.Hello, 4)
			eventsCh := make(chan []agentproto.Event, 8)
			relay := relayws.NewServer(relayws.ServerCallbacks{
				OnHello: func(_ context.Context, _ relayws.ConnectionMeta, hello agentproto.Hello) {
					if hello.Instance.InstanceID == "shared-hello-test" {
						helloCh <- hello
					}
				},
				OnEvents: func(_ context.Context, _ relayws.ConnectionMeta, _ string, events []agentproto.Event) {
					eventsCh <- events
				},
			})
			relay.SetServerIdentity(agentproto.ServerIdentity{BinaryIdentity: agentproto.BinaryIdentity{
				Product: "codex-remote", Version: "test", BuildFingerprint: "fp-test", BinaryPath: "/test/codex-remote",
			}, PID: 1})
			defer relay.Close()
			relayHTTP := httptest.NewServer(relay)
			defer relayHTTP.Close()
			cfg := sharedTestApp(ownerURL).config
			cfg.RelayServerURL = "ws" + strings.TrimPrefix(relayHTTP.URL, "http")
			cfg.InstanceID = "shared-hello-test"
			cfg.WorkspaceRoot = t.TempDir()
			cfg.WorkspaceKey = cfg.WorkspaceRoot
			cfg.Version = "test"
			cfg.BuildFingerprint = "fp-test"
			cfg.BinaryPath = "/test/codex-remote"
			cfg.DaemonBinaryPath = cfg.BinaryPath
			cfg.ResumeThreadID = "native-thread"
			app := New(cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			var stdout, stderr testBuffer
			done := make(chan error, 1)
			go func() { _, err := app.Run(ctx, strings.NewReader(""), &stdout, &stderr); done <- err }()
			select {
			case hello := <-helloCh:
				if !hello.CapabilitiesDeclared || hello.Capabilities.SharedThreadSettings != (version == "0.170.0") {
					t.Fatalf("relay received pre-handshake capabilities: version=%s hello=%#v", version, hello)
				}
			case err := <-done:
				t.Fatalf("Run exited before hello: %v stderr=%s", err, stderr.String())
			case <-ctx.Done():
				t.Fatal("relay never received wrapper hello")
			}
			found := false
			for !found {
				select {
				case batch := <-eventsCh:
					found = batchHasEvent(batch, func(event agentproto.Event) bool {
						return event.Kind == agentproto.EventThreadSettingsUpdated && event.ThreadID == "native-thread" && event.ThreadSettings != nil && event.ThreadSettings.Model == "native-model" && event.ThreadSettings.ReasoningEffortSet
					})
				case err := <-done:
					t.Fatalf("Run exited before native settings: %v", err)
				case <-ctx.Done():
					t.Fatal("startup settings not delivered after hello")
				}
			}
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not stop after cancellation")
			}
		})
	}
}

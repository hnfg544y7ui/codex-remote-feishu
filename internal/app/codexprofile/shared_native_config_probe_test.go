package codexprofile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRunSharedNativeConfigProbeReadsActualBackendWithoutBinary(t *testing.T) {
	methods := make(chan string, 8)
	closed := make(chan struct{}, 2)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		defer func() { closed <- struct{}{} }()
		for {
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			method, _ := frame["method"].(string)
			methods <- method
			if method == "initialized" {
				continue
			}
			result := map[string]any{}
			if method == "config/read" {
				result["config"] = map[string]any{
					"model_provider":   "actual-shared",
					"chatgpt_base_url": OfficialChatGPTBaseURL,
					"model_providers": map[string]any{
						"actual-shared": map[string]any{"base_url": "https://shared.example/v1", "env_key": "SHARED_API_KEY", "api_key": "secret-not-observed"},
					},
				}
			}
			// A broadcast from another client is ignored by request ID.
			if err := conn.WriteJSON(map[string]any{"method": "thread/started", "params": map[string]any{"threadId": "outside"}}); err != nil {
				return
			}
			raw, _ := json.MarshalIndent(map[string]any{"id": frame["id"], "result": result}, "", "  ")
			if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	// A second probe proves the first did not stop or alter the shared server.
	for i := 0; i < 2; i++ {
		observation, err := RunNativeConfigProbe(context.Background(), NativeConfigProbeOptions{ServerURL: endpoint, Version: "test"})
		if err != nil {
			t.Fatal(err)
		}
		if observation.ModelProviderID != "actual-shared" || observation.ModelEndpoint != "https://shared.example/v1" ||
			observation.ChatGPTEndpoint != OfficialChatGPTBaseURL || !reflect.DeepEqual(observation.ProviderEnvKeys, []string{"SHARED_API_KEY"}) {
			t.Fatal("probe did not observe selected shared backend")
		}
		if _, err := json.Marshal(observation); err == nil {
			t.Fatal("raw observation unexpectedly serializable")
		}
		var got []string
		for j := 0; j < 3; j++ {
			got = append(got, <-methods)
		}
		if !reflect.DeepEqual(got, []string{"initialize", "initialized", "config/read"}) {
			t.Fatal("probe performed non-read-only RPCs")
		}
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("probe left its connection open")
		}
	}
}

func TestRunSharedNativeConfigProbeRejectsUnsafeOrUnavailableEndpointWithoutFallback(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	closedEndpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	server.Close()
	for _, test := range []struct{ endpoint, stage string }{
		{"ws://192.168.1.2:12345", "shared_url"},
		{"ws://127.0.0.1:12345/?token=secret", "shared_url"},
		{closedEndpoint, "shared_connect"},
	} {
		_, err := RunNativeConfigProbe(context.Background(), NativeConfigProbeOptions{ServerURL: test.endpoint, BinaryPath: "nonexistent-codex-private-fallback"})
		if err == nil || OAuthProbeErrorStage(err) != test.stage {
			t.Fatal("shared connection failure attempted private CLI fallback")
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), test.endpoint) {
			t.Fatal("shared probe error exposed endpoint contents")
		}
	}
}

func TestRunSharedNativeConfigProbeCancellationClosesSilentRead(t *testing.T) {
	for _, explicitCancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "cancel"}[explicitCancel], func(t *testing.T) {
			connected := make(chan struct{})
			closed := make(chan struct{})
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				defer close(closed)
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
				close(connected)
				_, _, _ = conn.ReadMessage() // Remain silent until the probe disconnects.
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			if explicitCancel {
				go func() { <-connected; cancel() }()
			}
			started := time.Now()
			_, err := RunNativeConfigProbe(ctx, NativeConfigProbeOptions{ServerURL: "ws" + strings.TrimPrefix(server.URL, "http")})
			if err == nil || time.Since(started) > 2*time.Second {
				t.Fatal("silent shared probe did not exit on context cancellation")
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("context cancellation did not close shared connection")
			}
		})
	}
}

func TestRunSharedNativeConfigProbeRejectsInvalidOrOversizedFrames(t *testing.T) {
	for _, payload := range []string{"[]", strings.Repeat("x", sharedNativeConfigProbeFrameLimit+1)} {
		upgrader := websocket.Upgrader{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			if _, _, err := conn.ReadMessage(); err == nil {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(payload))
			}
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := RunNativeConfigProbe(ctx, NativeConfigProbeOptions{ServerURL: "ws" + strings.TrimPrefix(server.URL, "http")})
		cancel()
		server.Close()
		if err == nil {
			t.Fatal("invalid or oversized shared response accepted")
		}
	}
}

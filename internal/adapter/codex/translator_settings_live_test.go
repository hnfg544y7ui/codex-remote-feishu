package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

// Opt-in integration coverage. The caller must start a disposable app-server
// with an isolated CODEX_HOME; this test never sends a turn/start or model task.
func TestLiveSharedThreadSettings(t *testing.T) {
	endpoint := os.Getenv("CODEX_REMOTE_TEST_MODEL_SYNC_URL")
	cwd := os.Getenv("CODEX_REMOTE_TEST_MODEL_SYNC_CWD")
	if endpoint == "" || os.Getenv("CODEX_REMOTE_TEST_MODEL_SYNC_ISOLATED") != "1" || cwd == "" {
		t.Skip("requires an explicitly isolated native/shared server and working directory")
	}
	remote := newSettingsLiveClient(t, endpoint, NewTranslator("live-remote"))
	remote.tr.SetSharedBackend(true)
	app := newSettingsLiveClient(t, endpoint, nil)
	started := remote.rpc("thread/start", map[string]any{"cwd": cwd, "model": "gpt-5.5", "config": map[string]any{"model_reasoning_effort": "low"}, "approvalPolicy": "never", "sandbox": "read-only"})
	thread := lookupString(started, "thread", "id")
	if thread == "" {
		t.Fatal("native server did not create the isolated thread")
	}
	remote.rpc("thread/inject_items", map[string]any{"threadId": thread, "items": []any{map[string]any{
		"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Isolated settings synchronization fixture; no inference."}},
	}}})
	app.rpc("thread/resume", map[string]any{"threadId": thread, "excludeTurns": true})
	remote.rpc("thread/resume", map[string]any{"threadId": thread, "excludeTurns": true})
	remote.requireSettings(thread, "gpt-5.5", "low")
	t.Log("initial resume publishes real native settings without a preceding mutation")

	remote.events = nil
	remote.settings(thread, &agentproto.ThreadSettingsSetCommand{Model: settingsString("gpt-5.5-mini"), ReasoningEffort: settingsString("high")})
	appState := app.rpc("thread/resume", map[string]any{"threadId": thread, "excludeTurns": true})
	if appState["model"] != "gpt-5.5-mini" || appState["reasoningEffort"] != "high" {
		t.Fatalf("Remote settings were not visible to the other client: %#v", appState)
	}
	var confirmed bool
	for _, event := range remote.events {
		if event.Kind == agentproto.EventThreadSettingsSetResult && event.ErrorMessage == "" {
			confirmed = true
		}
	}
	if !confirmed {
		t.Fatal("actual native ACK did not produce the correlated result")
	}
	t.Log("Remote -> native -> peer model/effort and real ACK passed")

	remote.events = nil
	app.rpc("thread/settings/update", map[string]any{"threadId": thread, "model": "gpt-5.5", "effort": "medium"})
	remote.rpc("thread/read", map[string]any{"threadId": thread, "includeTurns": false})
	remote.requireSettings(thread, "gpt-5.5", "medium")
	t.Log("peer -> native broadcast -> new Remote translator passed")

	app.rpc("thread/settings/update", map[string]any{"threadId": thread, "effort": "low"})
	remote.events = nil
	remote.rpc("thread/read", map[string]any{"threadId": thread, "includeTurns": false})
	remote.requireSettings(thread, "gpt-5.5", "low")
	app.rpc("thread/settings/update", map[string]any{"threadId": thread, "effort": nil})
	afterNull := app.rpc("thread/resume", map[string]any{"threadId": thread, "excludeTurns": true})
	if afterNull["reasoningEffort"] != "low" {
		t.Fatal("native effort:null behavior changed; reassess reset capability")
	}
	if _, err := remote.tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandThreadSettingsSet, CommandID: "unsupported-clear", Target: agentproto.Target{ThreadID: thread}, ThreadSettingsSet: &agentproto.ThreadSettingsSetCommand{ReasoningEffort: settingsString("")}}); err == nil {
		t.Fatal("Remote must not present native effort:null no-op as a successful reset")
	}
	t.Log("native null is a no-op; Remote correctly rejects unsupported reset")
	reconnected := newSettingsLiveClient(t, endpoint, NewTranslator("live-reconnected"))
	reconnected.tr.SetSharedBackend(true)
	startup, requestID, err := reconnected.tr.BuildSharedStartupResumeFrame(thread)
	if err != nil {
		t.Fatal(err)
	}
	reconnected.exchange(startup, requestID)
	reconnected.requireSettings(thread, "gpt-5.5", "low")
	if len(reconnected.generated) != 0 {
		t.Fatal("startup settings refresh attempted to start a model task")
	}
	t.Log("reconnected client reads latest native configuration without a model turn")

	// A stale captured surface/template must never reapply its old model.
	for _, mode := range []string{"", "on"} {
		remote.generated = nil
		frames, err := remote.tr.TranslateCommand(agentproto.Command{
			Kind: agentproto.CommandPromptSend, CommandID: "dry-turn-" + mode,
			Origin: agentproto.Origin{Surface: "live-test"}, Target: agentproto.Target{ThreadID: thread, CWD: cwd},
			Prompt:    agentproto.Prompt{Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "NEVER SEND: wire preparation only"}}},
			Overrides: agentproto.PromptOverrides{UseThreadSettings: true, Model: "stale-model", ReasoningEffort: "high", PlanMode: mode},
		})
		if err != nil || len(frames) != 1 {
			t.Fatalf("prepare mode=%q: %v", mode, err)
		}
		resume := liveSettingsFrame(t, frames[0])
		if resume["method"] != "thread/resume" {
			t.Fatalf("expected a fresh native settings read, got %#v", resume)
		}
		params := resume["params"].(map[string]any)
		if _, stale := params["model"]; stale {
			t.Fatalf("resume carries stale model: %#v", params)
		}
		remote.exchange(frames[0], resume["id"])
		if len(remote.generated) != 1 {
			t.Fatalf("settings refresh did not prepare exactly one turn: %d", len(remote.generated))
		}
		turn := liveSettingsFrame(t, remote.generated[0])
		if turn["method"] != "turn/start" {
			t.Fatalf("unexpected continuation: %#v", turn)
		}
		params = turn["params"].(map[string]any)
		if _, stale := params["model"]; stale {
			t.Fatal("shared turn reapplies a model override")
		}
		if _, stale := params["effort"]; stale {
			t.Fatal("shared turn reapplies an effort override")
		}
		if mode == "on" {
			settings := lookupMap(params, "collaborationMode", "settings")
			if settings["model"] != "gpt-5.5" || settings["reasoning_effort"] != "low" {
				t.Fatalf("plan mode reused stale settings: %#v", params)
			}
		} else if _, stale := params["collaborationMode"]; stale {
			t.Fatal("ordinary turn carries a stale collaboration override")
		}
		// Intentionally do not send the prepared turn/start to the backend.
		t.Logf("mode=%q uses current native settings; prepared turn was not sent", mode)
	}
}

type settingsLiveClient struct {
	t         *testing.T
	conn      *websocket.Conn
	tr        *Translator
	seq       int
	events    []agentproto.Event
	generated [][]byte
}

func newSettingsLiveClient(t *testing.T, endpoint string, tr *Translator) *settingsLiveClient {
	t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := &settingsLiveClient{t: t, conn: conn, tr: tr}
	client.rpc("initialize", map[string]any{"clientInfo": map[string]any{"name": "remote-model-sync-test", "version": "1.0"}, "capabilities": map[string]any{"experimentalApi": true}})
	if err := conn.WriteJSON(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	return client
}

func (c *settingsLiveClient) rpc(method string, params map[string]any) map[string]any {
	c.t.Helper()
	c.seq++
	id := fmt.Sprintf("live-%d", c.seq)
	raw, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		c.t.Fatal(err)
	}
	if c.tr != nil {
		if _, err := c.tr.ObserveClient(raw); err != nil {
			c.t.Fatal(err)
		}
	}
	return c.exchange(raw, id)
}

func (c *settingsLiveClient) exchange(raw []byte, id any) map[string]any {
	c.t.Helper()
	if err := c.conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		c.t.Fatal(err)
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		_, frame, err := c.conn.ReadMessage()
		if err != nil {
			c.t.Fatal(err)
		}
		message := liveSettingsFrame(c.t, frame)
		if c.tr != nil {
			result, err := c.tr.ObserveServer(frame)
			if err != nil {
				c.t.Fatal(err)
			}
			c.events = append(c.events, result.Events...)
			c.generated = append(c.generated, result.OutboundToCodex...)
		}
		if message["id"] == id {
			if problem, ok := message["error"]; ok {
				c.t.Fatalf("native request rejected: %#v", problem)
			}
			result, _ := message["result"].(map[string]any)
			return result
		}
	}
}

func (c *settingsLiveClient) settings(thread string, settings *agentproto.ThreadSettingsSetCommand) {
	c.t.Helper()
	frames, err := c.tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandThreadSettingsSet, CommandID: "live-settings", Target: agentproto.Target{ThreadID: thread}, ThreadSettingsSet: settings})
	if err != nil || len(frames) != 1 {
		c.t.Fatalf("settings translate failed: %v", err)
	}
	frame := liveSettingsFrame(c.t, frames[0])
	c.exchange(frames[0], frame["id"])
}

func (c *settingsLiveClient) requireSettings(thread, model, effort string) {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		for _, event := range c.events {
			if s := event.ThreadSettings; s != nil && s.ThreadID == thread && s.Model == model && s.ReasoningEffort == effort && s.ReasoningEffortSet {
				return
			}
		}
		// Notifications from another connection may arrive after a local read's
		// response. Wait for the actual broadcast rather than relying on ACK order.
		_, frame, err := c.conn.ReadMessage()
		if err != nil {
			c.t.Fatalf("missing authoritative model=%q effort=%q event: %v", model, effort, err)
		}
		result, err := c.tr.ObserveServer(frame)
		if err != nil {
			c.t.Fatal(err)
		}
		c.events = append(c.events, result.Events...)
		c.generated = append(c.generated, result.OutboundToCodex...)
	}
}

func liveSettingsFrame(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

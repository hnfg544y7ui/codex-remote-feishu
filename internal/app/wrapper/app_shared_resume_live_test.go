package wrapper

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kxn/codex-remote-feishu/internal/adapter/codex"
	"github.com/kxn/codex-remote-feishu/internal/app/appserverendpoint"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

// Explicit opt-in: subscribe to the selected native thread, but never send the
// translated turn/start continuation or execute any prompt, tool or approval.
func TestSharedResumeLiveExcludesHistory(t *testing.T) {
	endpoint, threadID := os.Getenv("CODEX_REMOTE_TEST_SHARED_HISTORY_URL"), os.Getenv("CODEX_REMOTE_TEST_SHARED_HISTORY_THREAD")
	if endpoint == "" || threadID == "" {
		t.Skip("requires explicit live endpoint and thread")
	}
	endpoint, err := appserverendpoint.NormalizeURL(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 5 * time.Second}).Dial(endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadLimit(sharedBackendFrameLimit)
	request := func(frame map[string]any) []byte {
		t.Helper()
		if err := conn.WriteJSON(frame); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			var response map[string]any
			if json.Unmarshal(raw, &response) != nil || response["id"] != frame["id"] {
				continue
			}
			if response["error"] != nil {
				t.Fatal("native request rejected")
			}
			return raw
		}
	}
	request(map[string]any{"id": "resume-live-init", "method": "initialize", "params": map[string]any{
		"clientInfo": map[string]any{"name": "remote_resume_test", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true},
	}})
	if err := conn.WriteJSON(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	tr := codex.NewTranslator("live-resume")
	tr.SetSharedBackend(true)
	frames, err := tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandPromptSend, Target: agentproto.Target{ThreadID: threadID},
		Origin: agentproto.Origin{Surface: "live-resume"}, Prompt: agentproto.Prompt{Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "unsent test prompt"}}}})
	if err != nil || len(frames) != 1 {
		t.Fatalf("build resume: %v", err)
	}
	var resume map[string]any
	if err := json.Unmarshal(frames[0], &resume); err != nil || resume["method"] != "thread/resume" || resume["params"].(map[string]any)["excludeTurns"] != true {
		t.Fatal("expected metadata-only resume")
	}
	raw := request(resume)
	result, err := tr.ObserveServer(raw)
	if err != nil || len(result.OutboundToCodex) != 1 {
		t.Fatalf("resume continuation: %v", err)
	}
	var followup map[string]any
	if json.Unmarshal(result.OutboundToCodex[0], &followup) != nil || followup["method"] != "turn/start" || followup["params"].(map[string]any)["threadId"] != threadID {
		t.Fatal("incorrect unsent continuation")
	}
	request(map[string]any{"id": "resume-live-check", "method": "thread/list", "params": map[string]any{"limit": 1}})
	t.Logf("resume_bytes=%d connection_usable=true continuation_not_sent=true", len(raw))
}

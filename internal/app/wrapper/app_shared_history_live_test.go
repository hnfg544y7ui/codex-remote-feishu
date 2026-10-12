package wrapper

import (
	"encoding/json"
	"github.com/gorilla/websocket"
	"github.com/kxn/codex-remote-feishu/internal/adapter/codex"
	"github.com/kxn/codex-remote-feishu/internal/app/appserverendpoint"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"os"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in read-only integration against a real native backend. No
// thread/resume, turn/start, approval, credentials or conversation text output.
func TestSharedHistoryLivePagination(t *testing.T) {
	endpoint, threadID := os.Getenv("CODEX_REMOTE_TEST_SHARED_HISTORY_URL"), os.Getenv("CODEX_REMOTE_TEST_SHARED_HISTORY_THREAD")
	if endpoint == "" || threadID == "" {
		t.Skip("requires explicit live history endpoint and thread")
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
	if err := conn.WriteJSON(map[string]any{"id": "history-live-init", "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "remote_history_live_test", "version": "1.0"}, "capabilities": map[string]any{"experimentalApi": true}}}); err != nil {
		t.Fatal(err)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var message map[string]any
		if json.Unmarshal(raw, &message) != nil {
			t.Fatal("invalid initialize response")
		}
		if message["id"] == "history-live-init" {
			if message["error"] != nil {
				t.Fatal("native initialize failed")
			}
			break
		}
	}
	if err := conn.WriteJSON(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	tr := codex.NewTranslator("live-history")
	tr.SetSharedBackend(true)
	read := func(commandID, turnID string) *agentproto.ThreadHistoryRecord {
		frames, err := tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandThreadHistoryRead, CommandID: commandID, Target: agentproto.Target{ThreadID: threadID, TurnID: turnID}})
		if err != nil {
			t.Fatal(err)
		}
		write := func(frames [][]byte) {
			for _, frame := range frames {
				if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
					t.Fatal(err)
				}
			}
		}
		write(frames)
		largest, pages := 0, 0
		deadline := time.Now().Add(45 * time.Second)
		for {
			_ = conn.SetReadDeadline(deadline)
			_, raw, err := conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) > largest {
				largest = len(raw)
			}
			pages++
			result, err := tr.ObserveServer(raw)
			if err != nil {
				t.Fatal(err)
			}
			write(result.OutboundToCodex)
			for _, event := range result.Events {
				if event.Kind != agentproto.EventThreadHistoryRead || event.CommandID != commandID {
					continue
				}
				if event.ErrorMessage != "" {
					t.Fatalf("native history failed: %s", event.ErrorMessage)
				}
				if event.ThreadHistory == nil || event.ThreadHistory.Thread.ThreadID != threadID {
					t.Fatal("history result lost its target")
				}
				t.Logf("history detail=%t frames=%d largest_response_bytes=%d turns=%d", turnID != "", pages, largest, len(event.ThreadHistory.Turns))
				return event.ThreadHistory
			}
		}
	}
	list := read("live-list", "")
	if len(list.Turns) < 2 {
		t.Fatal("live test needs at least two turns")
	}
	selected := list.Turns[len(list.Turns)-2]
	detail := read("live-detail", selected.TurnID)
	found := false
	for _, turn := range detail.Turns {
		if turn.TurnID == selected.TurnID {
			found = true
			if len(turn.Items) < len(selected.Items) {
				t.Fatal("detail hydration lost summary items")
			}
			for _, summaryItem := range selected.Items {
				if strings.TrimSpace(summaryItem.Text) == "" {
					continue
				}
				preserved := false
				for _, item := range turn.Items {
					if item.ItemID == summaryItem.ItemID && strings.TrimSpace(item.Text) != "" {
						preserved = true
						break
					}
				}
				if !preserved {
					t.Fatalf("detail hydration lost visible %s content", summaryItem.Kind)
				}
			}
			t.Logf("selected_turn_items=%d summary_items=%d", len(turn.Items), len(selected.Items))
		}
	}
	if !found {
		t.Fatal("detail read lost selected turn")
	}
	// A second list request on the same socket proves the history read did not
	// leave an oversized response or broken transport behind.
	again := read("live-list-again", "")
	if len(again.Turns) < len(list.Turns) {
		t.Fatal("read lost historical turns")
	}
}

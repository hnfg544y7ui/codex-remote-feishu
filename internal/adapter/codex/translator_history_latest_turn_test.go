package codex

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestLatestTurnReadUsesBoundedMetadataWithoutTakingOwnership(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "private"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			tr := NewTranslator("remote")
			if shared {
				tr.SetSharedBackend(true)
				sharedResume(t, tr, "selected")
			}
			current := tr.currentThreadID
			frames, err := tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandThreadHistoryRead,
				CommandID: "probe-current", Target: agentproto.Target{ThreadID: "selected"},
				HistoryRead: &agentproto.ThreadHistoryReadOptions{LatestTurnOnly: true}})
			if err != nil {
				t.Fatal(err)
			}
			read := historyRequest(t, frames, "thread/read")
			if read["params"].(map[string]any)["includeTurns"] != false {
				t.Fatal("probe fetched full conversation history")
			}
			result := historyResponse(t, tr, read, map[string]any{"thread": map[string]any{"id": "selected", "status": map[string]any{"type": "active"}}})
			page := historyRequest(t, result.OutboundToCodex, "thread/turns/list")
			params := page["params"].(map[string]any)
			if params["threadId"] != "selected" || params["limit"] != float64(1) || params["sortDirection"] != "desc" {
				t.Fatal("probe must ask for exactly the most recent turn metadata", params)
			}
			result = historyResponse(t, tr, page, map[string]any{"threadId": "selected", "nextCursor": "older-turns-exist",
				"data": []any{map[string]any{"id": "live-turn", "status": "inProgress", "items": []any{}}}})
			if len(result.OutboundToCodex) != 0 || len(result.Events) != 1 || result.Events[0].Kind != agentproto.EventThreadHistoryRead {
				t.Fatalf("probe must stop after one metadata page: %#v", result)
			}
			event := result.Events[0]
			if event.CommandID != "probe-current" || event.ThreadID != "selected" || event.ThreadHistory == nil || len(event.ThreadHistory.Turns) != 1 || event.ThreadHistory.Turns[0].TurnID != "live-turn" {
				t.Fatal("probe response lost its exact request identity", event)
			}
			if tr.currentThreadID != current || (shared && tr.shared.ownTurns["live-turn"]) {
				t.Fatal("metadata query changed thread selection or claimed a turn")
			}
		})
	}
}

func TestLatestTurnReadRejectsWrongThreadAndOversizedPage(t *testing.T) {
	for _, wrongThread := range []bool{false, true} {
		tr := NewTranslator("remote")
		tr.SetSharedBackend(true)
		sharedResume(t, tr, "selected")
		frames, err := tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandThreadHistoryRead,
			CommandID: "probe-current", Target: agentproto.Target{ThreadID: "selected"},
			HistoryRead: &agentproto.ThreadHistoryReadOptions{LatestTurnOnly: true}})
		if err != nil {
			t.Fatal(err)
		}
		result := historyResponse(t, tr, historyRequest(t, frames, "thread/read"), map[string]any{"thread": map[string]any{"id": "selected"}})
		page := historyTurnsPage("one", "two")
		if wrongThread {
			page = historyTurnsPage("one")
			page["threadId"] = "other"
		}
		result = historyResponse(t, tr, historyRequest(t, result.OutboundToCodex, "thread/turns/list"), page)
		if len(result.Events) != 1 || result.Events[0].ErrorMessage == "" || result.Events[0].ThreadHistory != nil || len(result.OutboundToCodex) != 0 {
			t.Fatal("invalid probe response must fail closed without querying more pages", result)
		}
	}
}

package codex

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func historyRequest(t *testing.T, frames [][]byte, method string) map[string]any {
	t.Helper()
	if len(frames) != 1 {
		t.Fatalf("expected one history request, got %q", frames)
	}
	var message map[string]any
	if err := json.Unmarshal(frames[0], &message); err != nil {
		t.Fatal(err)
	}
	if message["method"] != method {
		t.Fatalf("expected %s, got %s", method, frames[0])
	}
	return message
}

func historyResponse(t *testing.T, tr *Translator, request map[string]any, result any) Result {
	t.Helper()
	frame, err := json.Marshal(map[string]any{"id": request["id"], "result": result})
	if err != nil {
		t.Fatal(err)
	}
	return sharedObserve(t, tr, string(frame))
}

func startHistory(t *testing.T, tr *Translator, command, thread, turn string) map[string]any {
	t.Helper()
	frames, err := tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandThreadHistoryRead, CommandID: command, Target: agentproto.Target{ThreadID: thread, TurnID: turn}})
	if err != nil {
		t.Fatal(err)
	}
	request := historyRequest(t, frames, "thread/read")
	if request["params"].(map[string]any)["includeTurns"] != false {
		t.Fatal("shared history must never request all full turns")
	}
	return request
}

func historyTurnsPage(turns ...string) map[string]any {
	data := []any{}
	for _, turn := range turns {
		data = append(data, map[string]any{"id": turn, "itemsView": "summary", "status": "completed", "items": []any{
			map[string]any{"id": turn + "-user", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "question"}}},
			map[string]any{"id": turn + "-agent", "type": "agentMessage", "text": "final summary"},
		}})
	}
	return map[string]any{"data": data, "nextCursor": nil}
}

func TestSharedHistoryPaginationSummaryAndDetail(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "selected")
	tr.shared.ownTurns["owned-turn"] = true
	metadata := startHistory(t, tr, "history", "history-thread", "turn-2")
	result := historyResponse(t, tr, metadata, map[string]any{"thread": map[string]any{"id": "history-thread", "name": "name"}})
	first := historyRequest(t, result.OutboundToCodex, "thread/turns/list")
	if params := first["params"].(map[string]any); params["limit"] != float64(50) || params["sortDirection"] != "asc" {
		t.Fatal(params)
	}
	page := historyTurnsPage("turn-1")
	page["nextCursor"] = "next"
	result = historyResponse(t, tr, first, page)
	second := historyRequest(t, result.OutboundToCodex, "thread/turns/list")
	if second["params"].(map[string]any)["cursor"] != "next" {
		t.Fatal(second)
	}
	result = historyResponse(t, tr, second, historyTurnsPage("turn-2"))
	items := historyRequest(t, result.OutboundToCodex, "thread/items/list")
	if params := items["params"].(map[string]any); params["turnId"] != "turn-2" || params["threadId"] != "history-thread" {
		t.Fatal(params)
	}
	result = historyResponse(t, tr, items, map[string]any{"data": []any{map[string]any{"id": "cmd", "type": "commandExecution", "command": "go test", "exitCode": 0}}, "nextCursor": "items-next"})
	items = historyRequest(t, result.OutboundToCodex, "thread/items/list")
	result = historyResponse(t, tr, items, map[string]any{"data": []any{map[string]any{"id": "answer", "type": "agentMessage", "text": "full answer"}}, "nextCursor": nil})
	if !result.Suppress || len(result.Events) != 1 || result.Events[0].CommandID != "history" || result.Events[0].ThreadHistory == nil {
		t.Fatal(result)
	}
	history := result.Events[0].ThreadHistory
	if history.Thread.Name != "name" || len(history.Turns) != 2 || history.Turns[0].TurnID != "turn-1" || history.Turns[1].TurnID != "turn-2" {
		t.Fatal(history)
	}
	if history.Turns[0].Items[1].Text != "final summary" || history.Turns[1].Items[0].Command != "go test" || history.Turns[1].Items[1].Text != "full answer" {
		t.Fatal(history.Turns)
	}
	if len(tr.pendingHistoryPages) != 0 || len(tr.shared.requests) != 0 || tr.currentThreadID != "selected" || tr.shared.selectedThread != "selected" || !reflect.DeepEqual(tr.shared.threads, map[string]bool{"selected": true}) || !reflect.DeepEqual(tr.shared.ownTurns, map[string]bool{"owned-turn": true}) {
		t.Fatal("history read changed selection/ownership or retained pending state")
	}
}

func TestSharedHistoryConcurrentQueriesUseExactResponseIDs(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	a := startHistory(t, tr, "command-a", "thread", "")
	b := startHistory(t, tr, "command-b", "thread", "")
	unknown := sharedObserve(t, tr, `{"id":"another-client","result":{"thread":{"id":"wrong"}}}`)
	if len(unknown.Events) != 0 {
		t.Fatal(unknown)
	}
	br := historyResponse(t, tr, b, map[string]any{"thread": map[string]any{"id": "thread"}})
	ar := historyResponse(t, tr, a, map[string]any{"thread": map[string]any{"id": "thread"}})
	br = historyResponse(t, tr, historyRequest(t, br.OutboundToCodex, "thread/turns/list"), historyTurnsPage("b"))
	ar = historyResponse(t, tr, historyRequest(t, ar.OutboundToCodex, "thread/turns/list"), historyTurnsPage("a"))
	if br.Events[0].CommandID != "command-b" || br.Events[0].ThreadHistory.Turns[0].TurnID != "b" || ar.Events[0].CommandID != "command-a" || ar.Events[0].ThreadHistory.Turns[0].TurnID != "a" || len(tr.pendingHistoryPages) != 0 {
		t.Fatalf("responses crossed jobs: %#v %#v", br.Events, ar.Events)
	}
}

func TestSharedHistoryBadPagesFailWithoutDisconnecting(t *testing.T) {
	cases := []struct {
		name   string
		page   map[string]any
		second map[string]any
	}{
		{"missing data", map[string]any{}, nil},
		{"invalid cursor", map[string]any{"data": []any{}, "nextCursor": 1}, nil},
		{"empty next page", map[string]any{"data": []any{}, "nextCursor": "next"}, nil},
		{"invalid turn", map[string]any{"data": []any{map[string]any{}}, "nextCursor": nil}, nil},
		{"wrong thread", map[string]any{"threadId": "wrong", "data": []any{}}, nil},
		{"repeated cursor", map[string]any{"data": historyTurnsPage("first")["data"], "nextCursor": "same"}, map[string]any{"data": historyTurnsPage("second")["data"], "nextCursor": "same"}},
		{"repeated turn", map[string]any{"data": historyTurnsPage("first")["data"], "nextCursor": "next"}, historyTurnsPage("first")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewTranslator("remote")
			tr.SetSharedBackend(true)
			request := startHistory(t, tr, "command", "thread", "")
			result := historyResponse(t, tr, request, map[string]any{"thread": map[string]any{"id": "thread"}})
			result = historyResponse(t, tr, historyRequest(t, result.OutboundToCodex, "thread/turns/list"), tc.page)
			if tc.second != nil {
				result = historyResponse(t, tr, historyRequest(t, result.OutboundToCodex, "thread/turns/list"), tc.second)
			}
			assertHistoryFailed(t, tr, result)
		})
	}
}

func assertHistoryFailed(t *testing.T, tr *Translator, result Result) {
	t.Helper()
	if len(result.Events) != 1 || result.Events[0].Kind != agentproto.EventThreadHistoryRead || result.Events[0].CommandID != "command" || result.Events[0].ThreadID != "thread" || result.Events[0].ThreadHistory != nil || result.Events[0].ErrorMessage == "" || len(result.OutboundToCodex) != 0 || len(tr.pendingHistoryPages) != 0 || len(tr.shared.requests) != 0 || tr.shared.problem != nil {
		t.Fatalf("history failure must complete only its command without poisoning transport: %#v", result)
	}
}

func TestSharedHistoryMetadataAndNativeErrorFailCommand(t *testing.T) {
	for _, payload := range []any{map[string]any{"thread": map[string]any{"id": "wrong"}}, nil} {
		tr := NewTranslator("remote")
		tr.SetSharedBackend(true)
		request := startHistory(t, tr, "command", "thread", "")
		result := historyResponse(t, tr, request, payload)
		assertHistoryFailed(t, tr, result)
	}
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	request := startHistory(t, tr, "command", "thread", "")
	frame, _ := json.Marshal(map[string]any{"id": request["id"], "error": map[string]any{"code": -32000, "message": "not available"}})
	assertHistoryFailed(t, tr, sharedObserve(t, tr, string(frame)))
}

func TestSharedHistoryConnectionResetDropsPaginationWithoutTurnReplay(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "selected")
	request := startHistory(t, tr, "command", "thread", "")
	tr.ResetSharedConnection()
	result := historyResponse(t, tr, request, map[string]any{"thread": map[string]any{"id": "thread"}})
	if len(tr.pendingHistoryPages) != 0 || len(result.Events) != 0 || len(result.OutboundToCodex) != 0 || tr.currentThreadID != "selected" {
		t.Fatal("reconnection revived stale history query or changed selected thread")
	}
}

func TestSharedHistoryDetailFailuresCompleteQuery(t *testing.T) {
	for _, scenario := range []string{"missing turn", "wrong turn", "invalid item", "repeated item", "native error"} {
		t.Run(scenario, func(t *testing.T) {
			tr := NewTranslator("remote")
			tr.SetSharedBackend(true)
			request := startHistory(t, tr, "command", "thread", "detail")
			result := historyResponse(t, tr, request, map[string]any{"thread": map[string]any{"id": "thread"}})
			page := historyTurnsPage("detail")
			if scenario == "missing turn" {
				page = historyTurnsPage("other")
			}
			result = historyResponse(t, tr, historyRequest(t, result.OutboundToCodex, "thread/turns/list"), page)
			if scenario != "missing turn" {
				request = historyRequest(t, result.OutboundToCodex, "thread/items/list")
				page = map[string]any{"data": []any{}, "nextCursor": nil}
				switch scenario {
				case "wrong turn":
					page["turnId"] = "wrong"
				case "invalid item":
					page["data"] = []any{map[string]any{}}
				case "repeated item":
					page["data"] = []any{map[string]any{"id": "same", "type": "agentMessage", "text": "answer"}}
					page["nextCursor"] = "next"
				case "native error":
					frame, _ := json.Marshal(map[string]any{"id": request["id"], "error": map[string]any{"code": -32000, "message": "items unavailable"}})
					assertHistoryFailed(t, tr, sharedObserve(t, tr, string(frame)))
					return
				}
				result = historyResponse(t, tr, request, page)
				if scenario == "repeated item" {
					page["nextCursor"] = nil
					result = historyResponse(t, tr, historyRequest(t, result.OutboundToCodex, "thread/items/list"), page)
				}
			}
			assertHistoryFailed(t, tr, result)
		})
	}
}

func TestSharedHistoryPaginationHasFinitePageBudget(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	request := startHistory(t, tr, "command", "thread", "")
	result := historyResponse(t, tr, request, map[string]any{"thread": map[string]any{"id": "thread"}})
	request = historyRequest(t, result.OutboundToCodex, "thread/turns/list")
	tr.pendingHistoryPages[request["id"].(string)].job.pages = 10000
	result = historyResponse(t, tr, request, historyTurnsPage("turn"))
	assertHistoryFailed(t, tr, result)
}

func TestSharedHistoryUnsupportedPaginationDoesNotFallbackAndConnectionRemainsUsable(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "selected")
	tr.shared.ownTurns["owned"] = true
	request := startHistory(t, tr, "command", "thread", "")
	result := historyResponse(t, tr, request, map[string]any{"thread": map[string]any{"id": "thread"}})
	request = historyRequest(t, result.OutboundToCodex, "thread/turns/list")
	frame, _ := json.Marshal(map[string]any{"id": request["id"], "error": map[string]any{"code": -32601, "message": "method not found"}})
	result = sharedObserve(t, tr, string(frame))
	assertHistoryFailed(t, tr, result)
	if len(result.OutboundToCodex) != 0 {
		t.Fatal("unsupported pagination must not fall back to unbounded full history")
	}
	request = startHistory(t, tr, "retry", "thread", "")
	result = historyResponse(t, tr, request, map[string]any{"thread": map[string]any{"id": "thread"}})
	result = historyResponse(t, tr, historyRequest(t, result.OutboundToCodex, "thread/turns/list"), historyTurnsPage("turn"))
	if len(result.Events) != 1 || result.Events[0].CommandID != "retry" || result.Events[0].ThreadHistory == nil || result.Events[0].ErrorMessage != "" || tr.shared.selectedThread != "selected" || !reflect.DeepEqual(tr.shared.ownTurns, map[string]bool{"owned": true}) || len(tr.pendingHistoryPages) != 0 || len(tr.shared.requests) != 0 {
		t.Fatal("failed history query damaged connection or selection/turn ownership")
	}
}

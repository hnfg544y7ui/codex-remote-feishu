package codex

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/pathcanon"
)

func startHistoryItemsPage(t *testing.T, tr *Translator) map[string]any {
	t.Helper()
	request := startHistory(t, tr, "command", "thread", "detail")
	result := historyResponse(t, tr, request, map[string]any{"thread": map[string]any{"id": "thread"}})
	result = historyResponse(t, tr, historyRequest(t, result.OutboundToCodex, "thread/turns/list"), historyTurnsPage("detail"))
	return historyRequest(t, result.OutboundToCodex, "thread/items/list")
}

func wrappedHistoryItem(item map[string]any) map[string]any {
	return map[string]any{"turnId": "detail", "item": item, "startedAtMs": 1781000000000, "completedAtMs": 1781000001000}
}

func TestSharedHistoryWrappedItemsPreserveNativeContentAndDetails(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	request := startHistoryItemsPage(t, tr)
	result := historyResponse(t, tr, request, map[string]any{"data": []any{
		wrappedHistoryItem(map[string]any{"id": "input", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "check the build"}}}),
		wrappedHistoryItem(map[string]any{"id": "command", "type": "commandExecution", "status": "completed", "command": "go test ./...", "cwd": "/tmp/project", "exitCode": 0}),
	}, "nextCursor": "next"})
	request = historyRequest(t, result.OutboundToCodex, "thread/items/list")
	if request["params"].(map[string]any)["cursor"] != "next" {
		t.Fatal("wrapped items must continue pagination")
	}
	result = historyResponse(t, tr, request, map[string]any{"data": []any{
		wrappedHistoryItem(map[string]any{"id": "answer", "type": "agentMessage", "text": "the build passed"}),
	}, "nextCursor": nil})
	if len(result.Events) != 1 || result.Events[0].ThreadHistory == nil {
		t.Fatal(result)
	}
	items := result.Events[0].ThreadHistory.Turns[0].Items
	if len(items) != 3 || items[0].ItemID != "input" || items[0].Kind != "user_message" || items[0].Text != "check the build" || items[1].ItemID != "command" || items[1].Command != "go test ./..." || items[1].CWD != pathcanon.Native("/tmp/project") || items[1].ExitCode == nil || *items[1].ExitCode != 0 || items[2].ItemID != "answer" || items[2].Kind != "agent_message" || items[2].Text != "the build passed" {
		t.Fatalf("wrapped native fields were lost: %#v", items)
	}
	if len(tr.pendingHistoryPages) != 0 || len(tr.shared.requests) != 0 {
		t.Fatal("completed wrapped pagination retained pending state")
	}
}

func TestSharedHistoryWrappedItemsRejectWrongTurnAndMalformedItem(t *testing.T) {
	for _, entry := range []map[string]any{
		{"turnId": "wrong", "item": map[string]any{"id": "item", "type": "agentMessage", "text": "answer"}},
		{"turnId": nil, "item": map[string]any{"id": "item", "type": "agentMessage", "text": "answer"}},
		{"turnId": 1, "item": map[string]any{"id": "item", "type": "agentMessage", "text": "answer"}},
		{"turnId": map[string]any{}, "item": map[string]any{"id": "item", "type": "agentMessage", "text": "answer"}},
		{"turnId": "detail", "item": nil},
		{"turnId": "detail", "item": "invalid"},
		{"turnId": "detail", "item": []any{}},
	} {
		tr := NewTranslator("remote")
		tr.SetSharedBackend(true)
		request := startHistoryItemsPage(t, tr)
		result := historyResponse(t, tr, request, map[string]any{"data": []any{entry}, "nextCursor": nil})
		assertHistoryFailed(t, tr, result)
	}
}

func TestSharedHistoryWrappedItemsDetectRepeatedNativeItemID(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	request := startHistoryItemsPage(t, tr)
	entry := wrappedHistoryItem(map[string]any{"id": "same", "type": "agentMessage", "text": "answer"})
	result := historyResponse(t, tr, request, map[string]any{"data": []any{entry}, "nextCursor": "next"})
	result = historyResponse(t, tr, historyRequest(t, result.OutboundToCodex, "thread/items/list"), map[string]any{"data": []any{entry}, "nextCursor": nil})
	assertHistoryFailed(t, tr, result)
}

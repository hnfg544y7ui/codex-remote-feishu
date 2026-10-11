package codex

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestSharedBackendStartupResumeSelectsOnlyAfterExactResponse(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	frame, requestID, err := tr.BuildSharedStartupResumeFrame("thread")
	if err != nil {
		t.Fatal(err)
	}
	var message map[string]any
	if err := json.Unmarshal(frame, &message); err != nil {
		t.Fatal(err)
	}
	params := message["params"].(map[string]any)
	if len(params) != 1 || params["threadId"] != "thread" || tr.currentThreadID != "" {
		t.Fatalf("startup resume changed launch settings or premature focus: %s", frame)
	}
	result := sharedObserve(t, tr, fmt.Sprintf(`{"id":%q,"result":{"thread":{"id":"thread"}}}`, requestID))
	if len(result.Events) != 0 || len(result.OutboundToCodex) != 0 || tr.currentThreadID != "thread" || !tr.shared.threads["thread"] {
		t.Fatalf("startup scope requires only exact resume response: %#v", result)
	}
}

func TestSharedBackendSwitchStopsPreviousThreadSubscription(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "old")
	sharedResume(t, tr, "new")
	result := sharedObserve(t, tr, `{"method":"turn/started","params":{"threadId":"old","turn":{"id":"outside-turn"}}}`)
	if !result.Suppress || len(result.Events) != 0 || tr.currentThreadID != "new" {
		t.Fatalf("old subscription remained active: %#v", result)
	}
}

func TestSharedBackendDifferentInstancesDoNotDuplicateThreadEvents(t *testing.T) {
	first, second := NewTranslator("first"), NewTranslator("second")
	first.SetSharedBackend(true)
	second.SetSharedBackend(true)
	sharedResume(t, first, "first-thread")
	sharedResume(t, second, "second-thread")
	frame := `{"method":"turn/started","params":{"threadId":"first-thread","turn":{"id":"desktop-turn"}}}`
	if result := sharedObserve(t, first, frame); len(result.Events) != 1 {
		t.Fatalf("subscribed instance missed event: %#v", result)
	}
	if result := sharedObserve(t, second, frame); len(result.Events) != 0 || !result.Suppress {
		t.Fatalf("another instance duplicated event: %#v", result)
	}
}

func TestSharedBackendOwnDesktopNativeRequestKeepsTypedID(t *testing.T) {
	tr := NewTranslator("desktop-bridge")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "thread")
	if _, err := tr.ObserveClient([]byte(`{"id":1,"method":"turn/start","params":{"threadId":"thread"}}`)); err != nil {
		t.Fatal(err)
	}
	// String "1" is a different JSON-RPC ID from numeric 1.
	wrong := sharedObserve(t, tr, `{"id":"1","result":{"turn":{"id":"wrong-turn"}}}`)
	if !wrong.Suppress || len(tr.shared.ownTurns) != 0 {
		t.Fatal("JSON ID type collision registered another client's turn")
	}
	sharedObserve(t, tr, `{"id":1,"result":{"turn":{"id":"desktop-own-turn"}}}`)
	result := sharedObserve(t, tr, `{"id":7,"method":"item/tool/call","params":{"threadId":"thread","turnId":"desktop-own-turn","tool":"automation_update","arguments":{}}}`)
	if result.Suppress || len(result.Events) != 0 {
		t.Fatalf("desktop's registered native callback was intercepted: %#v", result)
	}
	if _, err := tr.ObserveClient([]byte(`{"id":"7","result":{}}`)); err == nil {
		t.Fatal("string callback id was allowed to consume numeric native request")
	}
	if result, err := tr.ObserveClient([]byte(`{"id":7,"result":{}}`)); err != nil || result.Suppress {
		t.Fatalf("typed desktop native response rejected: %v %#v", err, result)
	}
}

func TestSharedBackendOwnRemoteDynamicCallbackRemainsActionable(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "thread")
	requestID := sharedPrompt(t, tr, "thread")
	sharedObserve(t, tr, fmt.Sprintf(`{"id":%q,"result":{"turn":{"id":"remote-turn"}}}`, requestID))
	result := sharedObserve(t, tr, `{"id":8,"method":"item/tool/call","params":{"threadId":"thread","turnId":"remote-turn","tool":"automation_update","arguments":{}}}`)
	if !result.Suppress || len(result.Events) != 1 || result.Events[0].RequestPrompt == nil ||
		result.Events[0].RequestPrompt.Type != agentproto.RequestTypeToolCallback ||
		result.Events[0].Initiator.Kind != agentproto.InitiatorRemoteSurface {
		t.Fatalf("owned Remote callback lost its actionable contract: %#v", result)
	}
	command := agentproto.Command{Kind: agentproto.CommandRequestRespond, Origin: agentproto.Origin{Surface: "surface"}}
	command.Request.RequestID = result.Events[0].RequestID
	command.Request.Response = map[string]any{"unsupported": true}
	if frames, err := tr.TranslateCommand(command); err != nil || len(frames) != 1 {
		t.Fatalf("Remote unsupported callback reply rejected: %v %s", err, frames)
	}
}

func TestSharedBackendRealNumericApprovalResolvedBroadcastIsScoped(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "thread")
	requestID := sharedPrompt(t, tr, "thread")
	sharedObserve(t, tr, fmt.Sprintf(`{"id":%q,"result":{"turn":{"id":"remote-turn"}}}`, requestID))
	// Shape captured from the real isolated Windows app-server. Native IDs are
	// numeric, and resolved notifications can omit turnId entirely.
	approval := sharedObserve(t, tr, `{"method":"item/commandExecution/requestApproval","id":0,"params":{"kind":"command","threadId":"thread","turnId":"remote-turn","itemId":"call_probe","environmentId":"local","command":"Write-Output probe","commandActions":[{"type":"unknown","command":"Write-Output probe"}],"availableDecisions":["accept","cancel"]}}`)
	if len(approval.Events) != 1 {
		t.Fatalf("real numeric approval not captured: %#v", approval)
	}
	outside := sharedObserve(t, tr, `{"method":"serverRequest/resolved","params":{"threadId":"thread","requestId":1}}`)
	if len(outside.Events) != 0 || !outside.Suppress {
		t.Fatal("unregistered outside approval resolved mutated projection")
	}
	own := sharedObserve(t, tr, `{"method":"serverRequest/resolved","params":{"threadId":"thread","requestId":0}}`)
	if len(own.Events) != 1 || own.Events[0].Kind != agentproto.EventRequestResolved || own.Events[0].RequestID != approval.Events[0].RequestID {
		t.Fatalf("registered numeric resolution lost: %#v", own)
	}
	command := agentproto.Command{Kind: agentproto.CommandRequestRespond, Origin: agentproto.Origin{Surface: "surface"}}
	command.Request.RequestID, command.Request.Response = approval.Events[0].RequestID, map[string]any{"decision": "cancel"}
	if _, err := tr.TranslateCommand(command); err == nil {
		t.Fatal("already resolved external approval remained answerable")
	}
	if duplicate := sharedObserve(t, tr, `{"method":"serverRequest/resolved","params":{"threadId":"thread","requestId":0}}`); len(duplicate.Events) != 0 {
		t.Fatal("resolved broadcast emitted twice")
	}
}

func TestSharedBackendPendingNativeOwnershipSurvivesHistoryEviction(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "thread")
	requestID := sharedPrompt(t, tr, "thread")
	sharedObserve(t, tr, fmt.Sprintf(`{"id":%q,"result":{"turn":{"id":"remote-turn"}}}`, requestID))
	nativeFrame := func(id int) string {
		return fmt.Sprintf(`{"method":"item/commandExecution/requestApproval","id":%d,"params":{"threadId":"thread","turnId":"remote-turn","command":"Write-Output probe"}}`, id)
	}
	first := sharedObserve(t, tr, nativeFrame(0))
	if len(first.Events) != 1 {
		t.Fatalf("initial approval missing: %#v", first)
	}
	for id := 1; id <= 600; id++ {
		sharedObserve(t, tr, nativeFrame(id))
		sharedObserve(t, tr, fmt.Sprintf(`{"method":"serverRequest/resolved","params":{"threadId":"thread","requestId":%d}}`, id))
	}
	if len(tr.shared.nativeOrder) != 513 || len(tr.shared.nativeRequests) != 1 ||
		!tr.shared.seenNative[first.Events[0].RequestID] ||
		tr.shared.nativeOwners[first.Events[0].RequestID].Kind != agentproto.InitiatorRemoteSurface {
		t.Fatal("pending native ownership was evicted or completed history became unbounded")
	}
	duplicate := sharedObserve(t, tr, nativeFrame(0))
	if !duplicate.Suppress || len(duplicate.Events) != 0 {
		t.Fatal("long-lived pending approval was projected twice after history eviction")
	}
	command := agentproto.Command{Kind: agentproto.CommandRequestRespond, Origin: agentproto.Origin{Surface: "surface"}}
	command.Request.RequestID = first.Events[0].RequestID
	if err := tr.sharedRequestRespondAllowed(command); err != nil {
		t.Fatalf("long-lived pending approval lost answer ownership: %v", err)
	}
	resolved := sharedObserve(t, tr, `{"method":"serverRequest/resolved","params":{"threadId":"thread","requestId":0}}`)
	if len(resolved.Events) != 1 || resolved.Events[0].RequestID != first.Events[0].RequestID ||
		len(tr.shared.nativeRequests) != 0 || len(tr.shared.nativeOrder) != 512 {
		t.Fatalf("pending resolution ownership or completed bound drifted: %#v", resolved)
	}
	if err := tr.sharedRequestRespondAllowed(command); err == nil {
		t.Fatal("resolved long-lived approval remained answerable")
	}
}

func TestSharedBackendOwnershipBufferIsBounded(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "thread")
	sharedPrompt(t, tr, "thread")
	for index := 0; index < 128; index++ {
		result := sharedObserve(t, tr, fmt.Sprintf(`{"method":"turn/started","params":{"threadId":"thread","turn":{"id":"unknown-%d"}}}`, index))
		if !result.Suppress || len(result.Events) != 0 {
			t.Fatal("ambiguous turn escaped before ownership response")
		}
	}
	result, err := tr.ObserveServer([]byte(`{"method":"turn/started","params":{"threadId":"thread","turn":{"id":"overflow"}}}`))
	if err == nil || len(result.Events) != 0 {
		t.Fatalf("unbounded ownership buffer or invented completion: %v %#v", err, result)
	}
}

func TestSharedBackendMissingOwnershipEvidenceFailsClosed(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "thread")
	requestID := sharedPrompt(t, tr, "thread")
	result, err := tr.ObserveServer([]byte(fmt.Sprintf(`{"id":%q,"result":{}}`, requestID)))
	if err == nil || len(result.Events) != 0 {
		t.Fatalf("missing turn ID was guessed from thread: %v %#v", err, result)
	}
	result, err = tr.ObserveServer([]byte(`{"id":4,"method":"account/chatgptAuthTokens/refresh","params":{}}`))
	if err == nil || len(result.Events) != 0 || len(result.OutboundToCodex) != 0 {
		t.Fatalf("context-free callback was handled without ownership: %v %#v", err, result)
	}
}

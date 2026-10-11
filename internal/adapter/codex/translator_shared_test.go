package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func sharedObserve(t *testing.T, tr *Translator, frame string) Result {
	t.Helper()
	result, err := tr.ObserveServer([]byte(frame))
	if err != nil {
		t.Fatalf("observe %s: %v", frame, err)
	}
	return result
}

func sharedResume(t *testing.T, tr *Translator, thread string) {
	t.Helper()
	if _, err := tr.ObserveClient([]byte(fmt.Sprintf(`{"id":"resume","method":"thread/resume","params":{"threadId":%q}}`, thread))); err != nil {
		t.Fatal(err)
	}
	sharedObserve(t, tr, fmt.Sprintf(`{"id":"resume","result":{"thread":{"id":%q}}}`, thread))
}

func sharedPrompt(t *testing.T, tr *Translator, thread string) string {
	t.Helper()
	frames, err := tr.TranslateCommand(agentproto.Command{
		Kind: agentproto.CommandPromptSend, Origin: agentproto.Origin{Surface: "surface"},
		Target: agentproto.Target{ThreadID: thread}, Prompt: agentproto.Prompt{Inputs: []agentproto.Input{{Type: agentproto.InputText, Text: "test"}}},
	})
	if err != nil || len(frames) != 1 {
		t.Fatalf("translate prompt: %v %s", err, frames)
	}
	var frame map[string]any
	if err := json.Unmarshal(frames[0], &frame); err != nil {
		t.Fatal(err)
	}
	if frame["method"] != "turn/start" {
		t.Fatalf("expected resumed thread direct turn/start: %s", frames[0])
	}
	return frame["id"].(string)
}

func TestSharedBackendTurnOwnershipUsesResponseID(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "thread")
	requestID := sharedPrompt(t, tr, "thread")
	for _, turn := range []string{"desktop-turn", "remote-turn"} {
		result := sharedObserve(t, tr, fmt.Sprintf(`{"method":"turn/started","params":{"threadId":"thread","turn":{"id":%q}}}`, turn))
		if !result.Suppress || len(result.Events) != 0 {
			t.Fatalf("notification must await its exact response: %#v", result)
		}
	}
	result := sharedObserve(t, tr, fmt.Sprintf(`{"id":%q,"result":{"turn":{"id":"remote-turn"}}}`, requestID))
	if len(result.Events) != 2 || result.Events[0].Initiator.Kind != agentproto.InitiatorLocalUI ||
		result.Events[1].Initiator.Kind != agentproto.InitiatorRemoteSurface || result.Events[1].Initiator.SurfaceSessionID != "surface" {
		t.Fatalf("request response must distinguish interleaved turns: %#v", result.Events)
	}
}

func TestSharedBackendFailedRemoteResponseDoesNotClaimDesktopTurn(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "thread")
	requestID := sharedPrompt(t, tr, "thread")
	sharedObserve(t, tr, `{"method":"turn/started","params":{"threadId":"thread","turn":{"id":"desktop-turn"}}}`)
	result := sharedObserve(t, tr, fmt.Sprintf(`{"id":%q,"error":{"message":"turn already active"}}`, requestID))
	if len(result.Events) != 2 || result.Events[1].TurnID != "desktop-turn" || result.Events[1].Initiator.Kind != agentproto.InitiatorLocalUI {
		t.Fatalf("outside turn misattributed after rejection: %#v", result.Events)
	}
	if tr.activeTurnByThread["thread"] != "desktop-turn" {
		t.Fatal("rejected request must not complete the running desktop turn")
	}
}

func TestSharedBackendIgnoresUnsubscribedThreadAndDoesNotMoveFocus(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "selected")
	for _, frame := range []string{
		`{"method":"thread/started","params":{"thread":{"id":"outside","cwd":"/outside"}}}`,
		`{"method":"turn/started","params":{"threadId":"outside","turn":{"id":"outside-turn"}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"outside","turnId":"outside-turn","delta":"secret"}}`,
	} {
		result := sharedObserve(t, tr, frame)
		if !result.Suppress || len(result.Events) != 0 || tr.currentThreadID != "selected" {
			t.Fatalf("outside broadcast changed local projection: %#v", result)
		}
	}
	if _, exists := tr.activeTurnByThread["outside"]; exists {
		t.Fatal("unsubscribed turn became active")
	}
}

func TestSharedBackendThreadStartedBeforeCreateResponse(t *testing.T) {
	tr := NewTranslator("desktop")
	tr.SetSharedBackend(true)
	if _, err := tr.ObserveClient([]byte(`{"id":2,"method":"thread/start","params":{}}`)); err != nil {
		t.Fatal(err)
	}
	sharedObserve(t, tr, `{"method":"thread/started","params":{"thread":{"id":"outside"}}}`)
	sharedObserve(t, tr, `{"method":"thread/started","params":{"thread":{"id":"own"}}}`)
	result := sharedObserve(t, tr, `{"id":2,"result":{"thread":{"id":"own"}}}`)
	if len(result.Events) != 1 || result.Events[0].ThreadID != "own" || tr.currentThreadID != "own" {
		t.Fatalf("thread creation ownership leaked: %#v focus=%s", result.Events, tr.currentThreadID)
	}
}

func TestSharedBackendNativeApprovalOwnershipAndDedup(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "thread")
	requestID := sharedPrompt(t, tr, "thread")
	sharedObserve(t, tr, fmt.Sprintf(`{"id":%q,"result":{"turn":{"id":"remote-turn"}}}`, requestID))
	sharedObserve(t, tr, `{"method":"turn/started","params":{"threadId":"thread","turn":{"id":"remote-turn"}}}`)
	broadcast := sharedObserve(t, tr, `{"method":"serverRequest/started","params":{"threadId":"thread","turnId":"remote-turn","request":{"id":"approval","type":"commandExecution"}}}`)
	if !broadcast.Suppress || len(broadcast.Events) != 0 {
		t.Fatal("broadcast started must not create an answerable request")
	}
	native := `{"id":"approval","method":"item/commandExecution/requestApproval","params":{"threadId":"thread","turnId":"remote-turn","command":"echo hi"}}`
	result := sharedObserve(t, tr, native)
	if len(result.Events) != 1 || result.Events[0].Kind != agentproto.EventRequestStarted || !result.Suppress {
		t.Fatalf("native remote approval not delivered only to Feishu: %#v", result)
	}
	if duplicate := sharedObserve(t, tr, native); len(duplicate.Events) != 0 {
		t.Fatal("duplicate native request produced duplicate approval")
	}
	command := agentproto.Command{Kind: agentproto.CommandRequestRespond, Origin: agentproto.Origin{Surface: "other"}}
	command.Request.RequestID, command.Request.Response = result.Events[0].RequestID, map[string]any{"decision": "accept"}
	if _, err := tr.TranslateCommand(command); err == nil {
		t.Fatal("another surface was allowed to answer")
	}
	command.Origin.Surface = "surface"
	if frames, err := tr.TranslateCommand(command); err != nil || len(frames) != 1 {
		t.Fatalf("owned approval rejected: %v %s", err, frames)
	}
	if _, err := tr.TranslateCommand(command); err == nil {
		t.Fatal("consumed approval answered twice")
	}
	if result := sharedObserve(t, tr, native); len(result.Events) != 0 {
		t.Fatal("late duplicate request became actionable again")
	}
}

func TestSharedBackendDoesNotAnswerDesktopCallback(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "thread")
	sharedObserve(t, tr, `{"method":"turn/started","params":{"threadId":"thread","turn":{"id":"desktop-turn"}}}`)
	result := sharedObserve(t, tr, `{"id":9,"method":"item/tool/call","params":{"threadId":"thread","turnId":"desktop-turn","tool":"automation_update","arguments":{}}}`)
	if !result.Suppress || len(result.Events) != 0 || len(result.OutboundToCodex) != 0 {
		t.Fatalf("desktop callback stolen by Remote: %#v", result)
	}
	if _, err := tr.ObserveClient([]byte(`{"id":9,"result":{"unsupported":true}}`)); err == nil {
		t.Fatal("unregistered native response accepted")
	}
}

func TestSharedBackendTimeoutAndReconnectDoNotReplayTurn(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "thread")
	oldRequestID := sharedPrompt(t, tr, "thread")
	if err := tr.CheckSharedBackendTimeout(time.Now().Add(16 * time.Second)); err == nil {
		t.Fatal("silent ownership handshake has no timeout")
	}
	tr.ResetSharedConnection()
	if err := tr.CheckSharedBackendTimeout(time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("old request survived reconnect: %v", err)
	}
	frame, requestID, ok, err := tr.BuildChildRestartRestoreFrame("restart")
	if err != nil || !ok || requestID == oldRequestID || !strings.Contains(string(frame), `"method":"thread/resume"`) {
		t.Fatalf("reconnect must only resume: %v %s", err, frame)
	}
	stale := sharedObserve(t, tr, fmt.Sprintf(`{"id":%q,"result":{"turn":{"id":"stale-turn"}}}`, oldRequestID))
	if len(stale.Events) != 0 || !stale.Suppress {
		t.Fatal("old generation response was consumed")
	}
	sharedObserve(t, tr, fmt.Sprintf(`{"id":%q,"result":{"thread":{"id":"thread"}}}`, requestID))
	if len(tr.shared.ownTurns) != 0 || len(tr.shared.requests) != 0 {
		t.Fatal("resume replayed an old turn")
	}
}

func TestSharedBackendDuplicateLifecycleDoesNotDuplicateEvents(t *testing.T) {
	tr := NewTranslator("remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "thread")
	for _, method := range []string{"turn/started", "turn/completed"} {
		frame := fmt.Sprintf(`{"method":%q,"params":{"threadId":"thread","turn":{"id":"desktop-turn","status":"completed"}}}`, method)
		first := sharedObserve(t, tr, frame)
		second := sharedObserve(t, tr, frame)
		if len(first.Events) != 1 || len(second.Events) != 0 || !second.Suppress {
			t.Fatalf("duplicate lifecycle projection: %#v %#v", first, second)
		}
	}
}

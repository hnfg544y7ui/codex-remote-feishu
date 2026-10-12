package codex

import (
	"fmt"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestSharedBackendGoalControlResponsesAreCorrelated(t *testing.T) {
	tr := NewTranslator("goal-remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "goal-thread")
	for _, kind := range []agentproto.CommandKind{agentproto.CommandThreadGoalSet, agentproto.CommandThreadGoalGet, agentproto.CommandThreadGoalClear} {
		command := agentproto.Command{CommandID: string(kind), Kind: kind, Target: agentproto.Target{ThreadID: "goal-thread"}, Goal: agentproto.GoalCommand{Status: "paused", Purpose: "user_control"}}
		frames, err := tr.TranslateCommand(command)
		if err != nil || len(frames) != 1 {
			t.Fatalf("translate %s: %v", kind, err)
		}
		request := decodeGoalRequest(t, frames[0])
		if request["method"] != threadGoalRPCMethod(kind) {
			t.Fatalf("wrong goal method: %#v", request)
		}
		foreign := sharedObserve(t, tr, `{"id":"foreign-goal","result":{"goal":{"threadId":"goal-thread","objective":"other","status":"active"}}}`)
		if !foreign.Suppress || len(foreign.Events) != 0 {
			t.Fatalf("foreign response leaked: %#v", foreign)
		}
		body := `{"goal":{"threadId":"goal-thread","objective":"isolated","status":"paused","createdAt":1,"updatedAt":2}}`
		if kind == agentproto.CommandThreadGoalClear {
			body = `{"cleared":true}`
		}
		result := sharedObserve(t, tr, fmt.Sprintf(`{"id":%q,"result":%s}`, request["id"], body))
		if len(result.Events) != 1 || result.Events[0].Kind != agentproto.EventThreadGoalCommandResult || result.Events[0].CommandID != command.CommandID || result.Events[0].ThreadID != "goal-thread" {
			t.Fatalf("goal response lost ownership: %#v", result)
		}
	}
}

func TestSharedBackendActiveGoalDoesNotClaimAutomaticTurnCallbacks(t *testing.T) {
	tr := NewTranslator("goal-remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "goal-thread")
	goal := sharedObserve(t, tr, `{"method":"thread/goal/updated","params":{"threadId":"goal-thread","goal":{"objective":"isolated","status":"active","createdAt":1,"updatedAt":2}}}`)
	if len(goal.Events) != 1 || goal.Events[0].Kind != agentproto.EventThreadGoalUpdated {
		t.Fatalf("subscribed goal notification lost: %#v", goal)
	}
	started := sharedObserve(t, tr, `{"method":"turn/started","params":{"threadId":"goal-thread","turn":{"id":"goal-auto-turn"}}}`)
	if len(started.Events) != 1 || started.Events[0].Initiator.Kind != agentproto.InitiatorLocalUI {
		t.Fatalf("unknown automatic turn claimed by Remote: %#v", started)
	}
	request := sharedObserve(t, tr, `{"id":"auto-approval","method":"item/commandExecution/requestApproval","params":{"threadId":"goal-thread","turnId":"goal-auto-turn","command":"echo isolated"}}`)
	if !request.Suppress || len(request.Events) != 0 || len(request.OutboundToCodex) != 0 {
		t.Fatalf("active Goal stole outside callback: %#v", request)
	}
	completed := sharedObserve(t, tr, `{"method":"turn/completed","params":{"threadId":"goal-thread","turn":{"id":"goal-auto-turn","status":"completed"}}}`)
	if len(completed.Events) != 1 || completed.Events[0].Kind != agentproto.EventTurnCompleted || completed.Events[0].Initiator.Kind != agentproto.InitiatorLocalUI {
		t.Fatalf("observed Goal turn completion lost: %#v", completed)
	}
	outside := sharedObserve(t, tr, `{"method":"thread/goal/updated","params":{"threadId":"outside-thread","goal":{"objective":"private","status":"active"}}}`)
	if !outside.Suppress || len(outside.Events) != 0 {
		t.Fatalf("outside goal leaked: %#v", outside)
	}
}

func TestSharedBackendGoalResponseDoesNotResolvePendingTurnOwnership(t *testing.T) {
	tr := NewTranslator("goal-remote")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "goal-thread")
	promptID := sharedPrompt(t, tr, "goal-thread")
	frames, err := tr.TranslateCommand(agentproto.Command{CommandID: "goal-query", Kind: agentproto.CommandThreadGoalGet, Target: agentproto.Target{ThreadID: "goal-thread"}})
	if err != nil {
		t.Fatal(err)
	}
	goalID := decodeGoalRequest(t, frames[0])["id"]
	buffered := sharedObserve(t, tr, `{"method":"turn/started","params":{"threadId":"goal-thread","turn":{"id":"goal-auto-turn"}}}`)
	if !buffered.Suppress || len(buffered.Events) != 0 {
		t.Fatalf("turn was not buffered: %#v", buffered)
	}
	queried := sharedObserve(t, tr, fmt.Sprintf(`{"id":%q,"result":{"goal":{"objective":"isolated","status":"active"}}}`, goalID))
	if len(queried.Events) != 1 || queried.Events[0].Kind != agentproto.EventThreadGoalCommandResult {
		t.Fatalf("goal query resolved unrelated turn: %#v", queried)
	}
	settled := sharedObserve(t, tr, fmt.Sprintf(`{"id":%q,"error":{"message":"turn already active"}}`, promptID))
	if len(settled.Events) != 2 || settled.Events[1].TurnID != "goal-auto-turn" || settled.Events[1].Initiator.Kind != agentproto.InitiatorLocalUI {
		t.Fatalf("buffered automatic turn lost: %#v", settled)
	}
}

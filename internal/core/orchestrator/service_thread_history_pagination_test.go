package orchestrator

import (
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"testing"
	"time"
)

func TestThreadHistoryQueryCarriesOnlySelectedDetailTurn(t *testing.T) {
	now := time.Now()
	svc := newServiceForTest(&now)
	svc.MaterializeSurface("surface-1", "app-1", "chat-1", "user-1")
	surface := svc.root.Surfaces["surface-1"]
	inst := &state.InstanceRecord{InstanceID: "inst-1", Backend: agentproto.BackendCodex, Threads: map[string]*state.ThreadRecord{"thread-1": {ThreadID: "thread-1"}}}
	flow := newOwnerCardFlowRecord(ownerCardFlowKindThreadHistory, "history-1", "user-1", now, time.Minute, ownerCardFlowPhaseLoading)
	for _, turnID := range []string{"", "selected-turn-1"} {
		record := &activeThreadHistoryRecord{ThreadID: "thread-1", TurnID: turnID}
		events := svc.startThreadHistoryQuery(surface, inst, flow, record, "", false)
		if len(events) != 2 || events[1].DaemonCommand == nil || events[1].DaemonCommand.HistoryTurnID != turnID {
			t.Fatalf("history query lost its detail scope %q: %#v", turnID, events)
		}
	}
}

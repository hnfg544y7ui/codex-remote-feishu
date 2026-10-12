package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestThreadHistoryDaemonCommandDispatchesAgentCommandAndStoresPending(t *testing.T) {
	app := New(":0", ":0", &recordingGateway{}, agentproto.ServerIdentity{})
	var sent []agentproto.Command
	app.sendAgentCommand = func(instanceID string, command agentproto.Command) error {
		if instanceID != "inst-1" {
			t.Fatalf("unexpected instance id: %s", instanceID)
		}
		sent = append(sent, command)
		return nil
	}

	events := app.handleThreadHistoryDaemonCommand(control.DaemonCommand{
		Kind:             control.DaemonCommandThreadHistoryRead,
		SurfaceSessionID: "surface-1",
		InstanceID:       "inst-1",
		ThreadID:         "thread-1",
		HistoryTurnID:    "turn-detail-1",
		SourceMessageID:  "msg-1",
	})
	if len(events) != 0 {
		t.Fatalf("expected no immediate UI events on success, got %#v", events)
	}
	if len(sent) != 1 || sent[0].Kind != agentproto.CommandThreadHistoryRead || sent[0].Target.ThreadID != "thread-1" {
		t.Fatalf("unexpected history command dispatch: %#v", sent)
	}
	if sent[0].Target.TurnID != "turn-detail-1" {
		t.Fatalf("selected detail turn was lost: %#v", sent[0].Target)
	}
	if pending, ok := app.pendingThreadHistoryReads[sent[0].CommandID]; !ok || pending.SurfaceSessionID != "surface-1" || pending.ThreadID != "thread-1" {
		t.Fatalf("expected pending history request to be recorded, got %#v", app.pendingThreadHistoryReads)
	}
}

func TestThreadHistoryDaemonCommandDispatchFailureReturnsNotice(t *testing.T) {
	app := New(":0", ":0", &recordingGateway{}, agentproto.ServerIdentity{})
	app.sendAgentCommand = func(string, agentproto.Command) error {
		return errors.New("relay unavailable")
	}

	events := app.handleThreadHistoryDaemonCommand(control.DaemonCommand{
		Kind:             control.DaemonCommandThreadHistoryRead,
		SurfaceSessionID: "surface-1",
		InstanceID:       "inst-1",
		ThreadID:         "thread-1",
	})
	if len(events) != 1 || events[0].Notice == nil || events[0].Notice.Code != "history_query_dispatch_failed" {
		t.Fatalf("expected dispatch failure notice, got %#v", events)
	}
	if len(app.pendingThreadHistoryReads) != 0 {
		t.Fatalf("expected no pending history requests after dispatch failure, got %#v", app.pendingThreadHistoryReads)
	}
}

func TestThreadHistoryCommandRejectClearsPendingAndEmitsNotice(t *testing.T) {
	app := New(":0", ":0", &recordingGateway{}, agentproto.ServerIdentity{})
	app.pendingThreadHistoryReads["cmd-history-1"] = pendingThreadHistoryRead{
		SurfaceSessionID: "surface-1",
		InstanceID:       "inst-1",
		ThreadID:         "thread-1",
	}

	app.onCommandAck(context.Background(), "inst-1", agentproto.CommandAck{
		CommandID: "cmd-history-1",
		Accepted:  false,
	})
	if len(app.pendingThreadHistoryReads) != 0 {
		t.Fatalf("expected pending history request to clear after reject, got %#v", app.pendingThreadHistoryReads)
	}
}

func TestThreadHistoryCommandAcceptKeepsPendingUntilResult(t *testing.T) {
	app := New(":0", ":0", &recordingGateway{}, agentproto.ServerIdentity{})
	app.pendingThreadHistoryReads["cmd-history-1"] = pendingThreadHistoryRead{
		SurfaceSessionID: "surface-1",
		InstanceID:       "inst-1",
		ThreadID:         "thread-1",
	}

	app.onCommandAck(context.Background(), "inst-1", agentproto.CommandAck{
		CommandID: "cmd-history-1",
		Accepted:  true,
	})
	if len(app.pendingThreadHistoryReads) != 1 {
		t.Fatalf("expected pending history request to remain until result arrives, got %#v", app.pendingThreadHistoryReads)
	}
}

func TestThreadHistoryEventStoresResultOnSurface(t *testing.T) {
	app := New(":0", ":0", &recordingGateway{}, agentproto.ServerIdentity{})
	app.service.MaterializeSurface("surface-1", "app-1", "chat-1", "user-1")
	app.service.UpsertInstance(&state.InstanceRecord{
		InstanceID: "inst-1",
		Online:     true,
		Threads: map[string]*state.ThreadRecord{
			"thread-1": {ThreadID: "thread-1"},
		},
	})
	app.pendingThreadHistoryReads["cmd-history-1"] = pendingThreadHistoryRead{
		SurfaceSessionID: "surface-1",
		InstanceID:       "inst-1",
		ThreadID:         "thread-1",
	}

	app.onEvents(context.Background(), "inst-1", []agentproto.Event{{
		Kind:      agentproto.EventThreadHistoryRead,
		CommandID: "cmd-history-1",
		ThreadID:  "thread-1",
		ThreadHistory: &agentproto.ThreadHistoryRecord{
			Thread: agentproto.ThreadSnapshotRecord{
				ThreadID: "thread-1",
				Name:     "修复登录",
			},
			Turns: []agentproto.ThreadHistoryTurnRecord{{
				TurnID: "turn-1",
				Status: "completed",
			}},
		},
	}})

	if len(app.pendingThreadHistoryReads) != 0 {
		t.Fatalf("expected pending history request to clear after result, got %#v", app.pendingThreadHistoryReads)
	}
	history := app.service.SurfaceThreadHistory("surface-1")
	if history == nil || history.Thread.ThreadID != "thread-1" || len(history.Turns) != 1 {
		t.Fatalf("expected stored surface history result, got %#v", history)
	}
}

package daemon

import (
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"testing"
)

func TestThreadHistoryFastResponseCannotBeLostDuringDispatch(t *testing.T) {
	app := New(":0", ":0", &recordingGateway{}, agentproto.ServerIdentity{})
	app.sendAgentCommand = func(instanceID string, command agentproto.Command) error {
		app.mu.Lock()
		defer app.mu.Unlock()
		_, handled := app.handleThreadHistoryEventLocked(instanceID, agentproto.Event{
			Kind: agentproto.EventThreadHistoryRead, CommandID: command.CommandID,
			ThreadID: "thread-1", ErrorMessage: "unsupported history pagination",
		})
		if !handled {
			t.Fatal("fast response did not find its pending command")
		}
		return nil
	}
	app.handleThreadHistoryDaemonCommand(control.DaemonCommand{
		Kind: control.DaemonCommandThreadHistoryRead, SurfaceSessionID: "surface-1",
		InstanceID: "inst-1", ThreadID: "thread-1",
	})
	if len(app.pendingThreadHistoryReads) != 0 {
		t.Fatal("completed history command was resurrected after dispatch")
	}
}

func TestThreadHistoryFailureWithoutPayloadClearsLoading(t *testing.T) {
	app := New(":0", ":0", &recordingGateway{}, agentproto.ServerIdentity{})
	app.pendingThreadHistoryReads["cmd-history-1"] = pendingThreadHistoryRead{
		SurfaceSessionID: "surface-1", InstanceID: "inst-1", ThreadID: "thread-1",
	}
	events, handled := app.handleThreadHistoryEventLocked("inst-1", agentproto.Event{
		Kind: agentproto.EventThreadHistoryRead, CommandID: "cmd-history-1",
		ThreadID: "thread-1", ErrorMessage: "pagination cursor repeated",
	})
	if !handled || len(app.pendingThreadHistoryReads) != 0 {
		t.Fatal("history error left a pending read")
	}
	if len(events) != 1 || events[0].Notice == nil || events[0].Notice.Code != "history_query_failed" {
		t.Fatalf("expected an actionable failure instead of disconnected transport: %#v", events)
	}
}

func TestNewHistorySelectionIgnoresOlderPartialRead(t *testing.T) {
	app := New(":0", ":0", &recordingGateway{}, agentproto.ServerIdentity{})
	app.pendingThreadHistoryReads["older-detail"] = pendingThreadHistoryRead{SurfaceSessionID: "surface-1", InstanceID: "inst-1", ThreadID: "thread-1"}
	app.pendingThreadHistoryReads["other-surface"] = pendingThreadHistoryRead{SurfaceSessionID: "surface-2", InstanceID: "inst-1", ThreadID: "thread-1"}
	app.sendAgentCommand = func(string, agentproto.Command) error { return nil }
	app.handleThreadHistoryDaemonCommand(control.DaemonCommand{Kind: control.DaemonCommandThreadHistoryRead, SurfaceSessionID: "surface-1", InstanceID: "inst-1", ThreadID: "thread-1", HistoryTurnID: "new-detail"})
	if _, ok := app.pendingThreadHistoryReads["older-detail"]; ok {
		t.Fatal("older partial read can overwrite the new detail")
	}
	if _, ok := app.pendingThreadHistoryReads["other-surface"]; !ok {
		t.Fatal("another surface's history was discarded")
	}
	_, handled := app.handleThreadHistoryEventLocked("inst-1", agentproto.Event{Kind: agentproto.EventThreadHistoryRead, CommandID: "older-detail", ThreadID: "thread-1", ThreadHistory: &agentproto.ThreadHistoryRecord{Thread: agentproto.ThreadSnapshotRecord{ThreadID: "thread-1"}}})
	if handled {
		t.Fatal("stale history result was consumed")
	}
}

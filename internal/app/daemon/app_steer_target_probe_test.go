package daemon

import (
	"context"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestSteerTargetProbeFastResponseDoesNotOverwritePendingPrompt(t *testing.T) {
	app := New(":0", ":0", &recordingGateway{}, agentproto.ServerIdentity{})
	var sent []agentproto.Command
	app.sendAgentCommand = func(instanceID string, command agentproto.Command) error {
		if instanceID != "inst-1" {
			t.Fatal("steer discovery changed its captured instance", instanceID)
		}
		sent = append(sent, command)
		if command.Kind == agentproto.CommandThreadHistoryRead {
			app.onEvents(context.Background(), instanceID, []agentproto.Event{{Kind: agentproto.EventThreadHistoryRead,
				CommandID: command.CommandID, ThreadID: "thread-1",
				ThreadHistory: &agentproto.ThreadHistoryRecord{Thread: agentproto.ThreadSnapshotRecord{ThreadID: "thread-1", RuntimeStatus: &agentproto.ThreadRuntimeStatus{Type: agentproto.ThreadRuntimeStatusTypeActive}},
					Turns: []agentproto.ThreadHistoryTurnRecord{{TurnID: "native-existing-turn", Status: "inProgress"}}}}})
		}
		return nil
	}
	workspace := evalSymlinkForTest(t, t.TempDir())
	app.service.UpsertInstance(&state.InstanceRecord{InstanceID: "inst-1", DisplayName: "probe", ShortName: "probe",
		Online: true, WorkspaceRoot: workspace, WorkspaceKey: workspace, ObservedFocusedThreadID: "thread-1", Threads: map[string]*state.ThreadRecord{
			"thread-1": {ThreadID: "thread-1", Name: "one", Loaded: true, CWD: workspace, WorkspaceKey: workspace},
		}})
	const surfaceID = "surface-1"
	for _, action := range []control.Action{
		{Kind: control.ActionAttachInstance, InstanceID: "inst-1"},
		{Kind: control.ActionUseThread, ThreadID: "thread-1"},
		{Kind: control.ActionTextMessage, MessageID: "pending-prompt", Text: "original"},
	} {
		action.SurfaceSessionID, action.ChatID, action.ActorUserID = surfaceID, "chat-1", "user-1"
		app.HandleAction(context.Background(), action)
	}
	app.onEvents(context.Background(), "inst-1", []agentproto.Event{{Kind: agentproto.EventThreadRuntimeStatusUpdated,
		ThreadID: "thread-1", RuntimeStatus: &agentproto.ThreadRuntimeStatus{Type: agentproto.ThreadRuntimeStatusTypeActive}}})
	for _, id := range []string{"queued-one", "queued-two"} {
		app.HandleAction(context.Background(), control.Action{Kind: control.ActionTextMessage,
			SurfaceSessionID: surfaceID, ChatID: "chat-1", ActorUserID: "user-1", MessageID: id, Text: id})
	}
	pendingBefore := app.service.PendingRemoteTurns()
	if len(pendingBefore) != 1 {
		t.Fatal("fixture must retain exactly one unconfirmed prompt", pendingBefore)
	}
	app.HandleAction(context.Background(), control.Action{Kind: control.ActionSteerAll,
		SurfaceSessionID: surfaceID, ChatID: "chat-1", ActorUserID: "user-1"})
	var steer *agentproto.Command
	prompts, queries := 0, 0
	for i := range sent {
		switch sent[i].Kind {
		case agentproto.CommandPromptSend:
			prompts++
		case agentproto.CommandThreadHistoryRead:
			queries++
		case agentproto.CommandTurnSteer:
			if steer != nil {
				t.Fatal("multiple steer requests were sent")
			}
			steer = &sent[i]
		}
	}
	if prompts != 1 || queries != 1 || steer == nil || steer.Target.TurnID != "native-existing-turn" || len(steer.Prompt.Inputs) != 2 {
		t.Fatal("expected one bounded query followed by one steer, without prompt replay", sent)
	}
	app.onCommandAck(context.Background(), "inst-1", agentproto.CommandAck{CommandID: steer.CommandID, Accepted: true})
	pendingAfter := app.service.PendingRemoteTurns()
	if len(pendingAfter) != 1 || pendingAfter[0].CommandID != pendingBefore[0].CommandID {
		t.Fatal("steer query or acknowledgement rebound the unrelated prompt", pendingBefore, pendingAfter)
	}
	if len(app.pendingThreadHistoryReads) != 0 {
		t.Fatal("internal target discovery leaked into the user's history card flow")
	}
}

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type renameHookGateway struct {
	*lifecycleGateway
	onApply func()
}

func (g *renameHookGateway) Apply(ctx context.Context, operations []feishu.Operation) error {
	if err := g.lifecycleGateway.Apply(ctx, operations); err != nil {
		return err
	}
	if hook := g.onApply; hook != nil {
		g.onApply = nil
		hook()
	}
	return nil
}

func newRenameAppForTest(t *testing.T, gateway *renameHookGateway) *App {
	t.Helper()
	app := New(":0", ":0", gateway, agentproto.ServerIdentity{})
	app.service.MaterializeSurface("surface-1", "app-1", "chat-1", "user-1")
	for _, id := range []string{"inst-1", "inst-2"} {
		root := t.TempDir()
		app.service.UpsertInstance(&state.InstanceRecord{
			InstanceID: id, DisplayName: id, WorkspaceRoot: root, WorkspaceKey: root, Online: true,
			Threads: map[string]*state.ThreadRecord{id + "-thread": {ThreadID: id + "-thread", Name: "原名称", CWD: root, Loaded: true}},
		})
	}
	app.service.ApplySurfaceAction(control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "surface-1", InstanceID: "inst-1"})
	app.service.ApplySurfaceAction(control.Action{Kind: control.ActionUseThread, SurfaceSessionID: "surface-1", ThreadID: "inst-1-thread"})
	return app
}

func TestRenameDispatchUsesOriginalInstanceAfterOwnerCardDelivery(t *testing.T) {
	gateway := &renameHookGateway{lifecycleGateway: newLifecycleGateway()}
	app := newRenameAppForTest(t, gateway)
	var target string
	var captured agentproto.Command
	app.sendAgentCommand = func(instanceID string, command agentproto.Command) error {
		target, captured = instanceID, command
		return nil
	}
	gateway.onApply = func() {
		app.service.ApplySurfaceAction(control.Action{Kind: control.ActionDetach, SurfaceSessionID: "surface-1"})
		app.service.ApplySurfaceAction(control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "surface-1", InstanceID: "inst-2"})
		app.service.ApplySurfaceAction(control.Action{Kind: control.ActionUseThread, SurfaceSessionID: "surface-1", ThreadID: "inst-2-thread"})
	}
	events := app.service.ApplySurfaceAction(control.Action{Kind: control.ActionRenameCommand, SurfaceSessionID: "surface-1", Text: "/rename 原会话新名称"})
	app.handleUIEvents(context.Background(), events)
	if app.service.AttachedInstanceID("surface-1") != "inst-2" {
		t.Fatal("delivery hook did not switch instance")
	}
	if target != "inst-1" || captured.Target.ThreadID != "inst-1-thread" {
		t.Fatalf("rename retargeted after delivery: instance=%q command=%#v", target, captured)
	}
	result := app.service.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind: agentproto.EventThreadNameCommandResult, CommandID: captured.CommandID, ThreadID: captured.Target.ThreadID, Name: captured.Name,
	})
	app.handleUIEvents(context.Background(), result)
	ops := gateway.snapshotOperations()
	if len(ops) != 2 || ops[0].Kind != feishu.OperationSendCard || ops[1].Kind != feishu.OperationUpdateCard || ops[0].MessageID != ops[1].MessageID {
		t.Fatalf("rename must send one owner card and update its terminal result: %#v", ops)
	}
	if app.service.Instance("inst-2").Threads["inst-2-thread"].Name != "原名称" {
		t.Fatal("renamed switched-to thread")
	}
}

func TestRenameExpiredBeforeDispatchDoesNotFallBackToAttachedInstance(t *testing.T) {
	gateway := &renameHookGateway{lifecycleGateway: newLifecycleGateway()}
	app := newRenameAppForTest(t, gateway)
	var commands []agentproto.Command
	var terminal []eventcontract.Event
	app.sendAgentCommand = func(_ string, command agentproto.Command) error { commands = append(commands, command); return nil }
	gateway.onApply = func() { terminal = app.service.Tick(time.Now().Add(time.Minute)) }
	events := app.service.ApplySurfaceAction(control.Action{Kind: control.ActionRenameCommand, SurfaceSessionID: "surface-1", Text: "/rename 不应发送"})
	app.handleUIEvents(context.Background(), events)
	if len(commands) != 0 {
		t.Fatalf("expired rename was sent: %#v", commands)
	}
	app.handleUIEvents(context.Background(), terminal)
}

func TestRenameMenuHandoffReplacesOriginalCard(t *testing.T) {
	gateway := &renameHookGateway{lifecycleGateway: newLifecycleGateway()}
	app := newRenameAppForTest(t, gateway)
	var captured agentproto.Command
	app.sendAgentCommand = func(_ string, command agentproto.Command) error { captured = command; return nil }
	result := handleGatewayActionForTest(context.Background(), app, control.Action{
		Kind: control.ActionRenameCommand, GatewayID: "app-1", SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1",
		MessageID: "menu-owner", Text: "/rename 中文  新名称",
		Inbound: &control.ActionInboundMeta{CardDaemonLifecycleID: app.daemonLifecycleID},
	})
	if result != nil && result.ReplaceCurrentCard != nil || captured.Kind != agentproto.CommandThreadNameSet {
		t.Fatalf("rename menu unexpectedly used callback replacement or failed dispatch: %#v", result)
	}
	initial := gateway.snapshotOperations()
	if len(initial) != 1 || initial[0].Kind != feishu.OperationUpdateCard || initial[0].MessageID != "menu-owner" {
		t.Fatalf("menu handoff must update original card without appending: %#v", initial)
	}
	app.handleUIEvents(context.Background(), app.service.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind: agentproto.EventThreadNameCommandResult, CommandID: captured.CommandID, ThreadID: captured.Target.ThreadID, Name: captured.Name,
	}))
	ops := gateway.snapshotOperations()
	if len(ops) != 2 || ops[1].Kind != feishu.OperationUpdateCard || ops[1].MessageID != "menu-owner" {
		t.Fatalf("rename completion did not update original menu card: %#v", ops)
	}
}

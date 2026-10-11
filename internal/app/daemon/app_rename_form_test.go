package daemon

import (
	"context"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	gatewaypkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/gateway"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestRenameMenuFormRenderedCallbackRoundTrip(t *testing.T) {
	for _, scenario := range []string{"success", "blank", "switched-target", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			gateway := &renameHookGateway{lifecycleGateway: newLifecycleGateway()}
			app := newRenameAppForTest(t, gateway)
			var commands []agentproto.Command
			app.sendAgentCommand = func(_ string, command agentproto.Command) error {
				commands = append(commands, command)
				return nil
			}
			menu := handleGatewayActionForTest(context.Background(), app, control.Action{
				Kind: control.ActionShowCommandMenu, GatewayID: "app-1", SurfaceSessionID: "surface-1",
				ChatID: "chat-1", ActorUserID: "user-1", MessageID: "menu-owner", Text: "/menu current_work",
				Inbound: &control.ActionInboundMeta{CardDaemonLifecycleID: app.daemonLifecycleID},
			})
			if menu == nil || menu.ReplaceCurrentCard == nil {
				t.Fatal("missing basic commands menu callback replacement")
			}
			userID := "user-1"
			parseCallback := func(payload map[string]any, fields map[string]interface{}) control.Action {
				t.Helper()
				action, ok := gatewaypkg.ParseCardActionTriggerEvent(gatewaypkg.RoutingEnv{
					GatewayID: "app-1", SurfaceForCardAction: func(gatewaypkg.CardActionSurfaceLookup) string { return "surface-1" },
				}, &larkcallback.CardActionTriggerEvent{Event: &larkcallback.CardActionTriggerRequest{
					Operator: &larkcallback.Operator{UserID: &userID},
					Action:   &larkcallback.CallBackAction{Value: payload, FormValue: fields},
					Context:  &larkcallback.Context{OpenChatID: "chat-1", OpenMessageID: "menu-owner"},
				}})
				if !ok {
					t.Fatal("rendered callback did not parse")
				}
				return action
			}
			launcher := findRenameCallbackPayload(menu.ReplaceCurrentCard.CardElements, "page_local_action")
			if launcher == nil {
				t.Fatal("menu lacks rename launcher")
			}
			formResult := app.HandleGatewayAction(context.Background(), parseCallback(launcher, nil))
			ops := gateway.snapshotOperations()
			if formResult == nil || formResult.ReplaceCurrentCard == nil || len(ops) != 0 || len(commands) != 0 {
				t.Fatalf("form must be the synchronous callback replacement, with no REST patch or dispatch: result=%#v ops=%#v", formResult, ops)
			}
			payload := findRenameFormSubmitPayload(formResult.ReplaceCurrentCard.CardElements)
			if payload == nil || payload["thread_id"] != "inst-1-thread" || payload["instance_id"] != "inst-1" {
				t.Fatalf("rendered form lacks exact target: %#v", payload)
			}
			name := "中文  新名称"
			if scenario == "blank" {
				name = "   "
			}
			if scenario == "switched-target" {
				app.service.ApplySurfaceAction(control.Action{Kind: control.ActionDetach, SurfaceSessionID: "surface-1"})
				app.service.ApplySurfaceAction(control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "surface-1", InstanceID: "inst-2"})
				app.service.ApplySurfaceAction(control.Action{Kind: control.ActionUseThread, SurfaceSessionID: "surface-1", ThreadID: "inst-2-thread"})
			}
			if scenario == "stale" {
				payload["daemon_lifecycle_id"] = "old-daemon"
			}
			action := parseCallback(payload, map[string]interface{}{"command_args": name})
			if action.InstanceID != "inst-1" || action.ThreadID != "inst-1-thread" || action.LocalPageAction {
				t.Fatalf("form submission did not preserve its target: %#v", action)
			}
			submitResult := app.HandleGatewayAction(context.Background(), action)
			if scenario == "blank" && (submitResult == nil || submitResult.ReplaceCurrentCard == nil || findRenameFormSubmitPayload(submitResult.ReplaceCurrentCard.CardElements) == nil || len(gateway.snapshotOperations()) != 0) {
				t.Fatal("blank submission must return the editable form in the callback without a REST patch")
			}
			if scenario != "success" {
				if len(commands) != 0 {
					t.Fatalf("invalid form submission dispatched: %#v", commands)
				}
				return
			}
			if len(commands) != 1 || commands[0].Name != name || commands[0].Target.ThreadID != "inst-1-thread" {
				t.Fatalf("form did not dispatch the entered name to its original target: %#v", commands)
			}
			app.handleUIEvents(context.Background(), app.service.ApplyAgentEvent("inst-1", agentproto.Event{
				Kind: agentproto.EventThreadNameCommandResult, CommandID: commands[0].CommandID, ThreadID: "inst-1-thread", Name: name,
			}))
			ops = gateway.snapshotOperations()
			if len(ops) != 2 {
				t.Fatalf("expected only asynchronous waiting and completion patches: %#v", ops)
			}
			for _, op := range ops {
				if op.Kind != feishu.OperationUpdateCard || op.MessageID != "menu-owner" {
					t.Fatalf("form flow appended or updated another card: %#v", op)
				}
			}
		})
	}
}

func findRenameFormSubmitPayload(value any) map[string]any {
	return findRenameCallbackPayload(value, "page_submit")
}

func findRenameCallbackPayload(value any, kind string) map[string]any {
	switch current := value.(type) {
	case map[string]any:
		if current["kind"] == kind && current["action_kind"] == string(control.ActionRenameCommand) {
			return current
		}
		for _, nested := range current {
			if payload := findRenameCallbackPayload(nested, kind); payload != nil {
				return payload
			}
		}
	case []map[string]any:
		for _, nested := range current {
			if payload := findRenameCallbackPayload(nested, kind); payload != nil {
				return payload
			}
		}
	case []any:
		for _, nested := range current {
			if payload := findRenameCallbackPayload(nested, kind); payload != nil {
				return payload
			}
		}
	}
	return nil
}

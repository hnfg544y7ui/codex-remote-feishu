package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu/gateway"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestGoalClearRenderedButtonsRoundTrip(t *testing.T) {
	for _, input := range []string{"/goal clear", "清除当前目标"} {
		for _, choice := range []string{"确认清除", "取消"} {
			t.Run(input+"/"+choice, func(t *testing.T) {
				svc, surfaceID, _ := goalInterlockTestSetup(t)
				action := goalContextActionForTest(surfaceID, "user-1", input)
				if parsed, ok := control.ParseFeishuNaturalGoalAction(input); ok {
					action.NaturalGoal = parsed.NaturalGoal
				}
				events := svc.ApplySurfaceAction(action)
				if findAgentCommand(events, agentproto.CommandThreadGoalClear) != nil {
					t.Fatal("opening confirmation cleared Goal")
				}
				buttons := goalProjectedButtonsForTest(t, events)
				if len(buttons) != 2 {
					t.Fatalf("confirmation must render two buttons, got %v", buttons)
				}
				callback := goalRenderedCallbackForTest(t, surfaceID, "user-1", buttons[choice])
				if callback.Inbound == nil || callback.Inbound.CardDaemonLifecycleID != "goal-card-life" ||
					(choice == "确认清除" && (callback.CatalogFamilyID == "" || callback.CatalogVariantID == "")) {
					t.Fatalf("rendered callback lost lifecycle or command provenance: %#v payload=%#v", callback, buttons[choice])
				}
				if choice == "确认清除" {
					expected := goalClearConfirmCommandForTest(t, findPageEvent(events))
					if callback.Text != expected || !strings.Contains(callback.Text, goalContextFlagPrefix) {
						t.Fatalf("rendered confirmation lost bound context: %q want %q", callback.Text, expected)
					}
				}
				result := svc.ApplySurfaceAction(callback)
				if choice == "取消" {
					if findAgentCommand(result, agentproto.CommandThreadGoalClear) != nil ||
						findAgentCommand(result, agentproto.CommandThreadGoalGet) == nil {
						t.Fatal("cancel must read status without clearing Goal")
					}
					return
				}
				clear := findAgentCommand(result, agentproto.CommandThreadGoalClear)
				if clear == nil || clear.Target.ThreadID != "thread-1" || clear.Goal.Purpose != "user_control" {
					t.Fatalf("rendered confirmation did not clear original thread: %#v", clear)
				}
				assertGoalFeedbackOperation(t, result, true, "om-goal-card")
				if findAgentCommand(svc.ApplySurfaceAction(callback), agentproto.CommandThreadGoalClear) != nil {
					t.Fatal("rendered confirmation was replayable")
				}
			})
		}
	}
}

func TestGoalRenderedConfirmationRejectsChangedContext(t *testing.T) {
	for _, mutation := range []string{"actor", "selection", "budget"} {
		t.Run(mutation, func(t *testing.T) {
			svc, surfaceID, instanceID := goalInterlockTestSetup(t)
			events := svc.ApplySurfaceAction(goalContextActionForTest(surfaceID, "user-1", "/goal clear"))
			buttons := goalProjectedButtonsForTest(t, events)
			actor := "user-1"
			switch mutation {
			case "actor":
				actor = "user-2"
			case "selection":
				svc.root.Surfaces[surfaceID].SelectedThreadID = "thread-2"
			case "budget":
				budget := int64(1234)
				svc.root.Instances[instanceID].Threads["thread-1"].ThreadGoal.TokenBudget = &budget
			}
			callback := goalRenderedCallbackForTest(t, surfaceID, actor, buttons["确认清除"])
			result := svc.ApplySurfaceAction(callback)
			if findAgentCommand(result, agentproto.CommandThreadGoalClear) != nil || findPageEvent(result) == nil {
				t.Fatal("changed context must reject clear with visible feedback")
			}
		})
	}
}

func TestGoalStatusRendersCommandButtons(t *testing.T) {
	for _, status := range []string{"active", "paused", "complete", "none"} {
		t.Run(status, func(t *testing.T) {
			svc, surfaceID, instanceID := goalInterlockTestSetup(t)
			var goal *agentproto.ThreadGoalUpdate
			if status != "none" {
				goal = agentproto.CloneThreadGoalUpdate(svc.root.Instances[instanceID].Threads["thread-1"].ThreadGoal)
				goal.Status = status
			}
			page := goalStatusPage(svc.root.Surfaces[surfaceID], goal)
			buttons := goalProjectedButtonsForTest(t, []eventcontract.Event{svc.pageEvent(svc.root.Surfaces[surfaceID], page)})
			for _, expected := range page.RelatedButtons {
				callback := goalRenderedCallbackForTest(t, surfaceID, "user-1", buttons[expected.Label])
				if callback.Text != expected.CommandText || callback.Kind != control.ActionGoalCommand {
					t.Fatalf("button %s did not round-trip: %#v", expected.Label, callback)
				}
			}
		})
	}
}

func goalProjectedButtonsForTest(t *testing.T, events []eventcontract.Event) map[string]map[string]any {
	t.Helper()
	buttons := map[string]map[string]any{}
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if value["tag"] == "button" {
				text, _ := value["text"].(map[string]any)
				label, _ := text["content"].(string)
				payload, _ := value["value"].(map[string]any)
				if behaviors, ok := value["behaviors"].([]any); ok {
					for _, behavior := range behaviors {
						if callback, ok := behavior.(map[string]any); ok && callback["type"] == "callback" {
							payload, _ = callback["value"].(map[string]any)
						}
					}
				}
				buttons[label] = payload
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	for _, event := range events {
		event.DaemonLifecycleID = "goal-card-life"
		for _, operation := range feishu.NewProjector().ProjectEvent("chat-1", event) {
			encoded, err := json.Marshal(operation.CardElements)
			if err != nil {
				t.Fatal(err)
			}
			var elements any
			if err := json.Unmarshal(encoded, &elements); err != nil {
				t.Fatal(err)
			}
			walk(elements)
		}
	}
	return buttons
}

func goalRenderedCallbackForTest(t *testing.T, surfaceID, actor string, value map[string]any) control.Action {
	t.Helper()
	if len(value) == 0 {
		t.Fatal("missing rendered callback value")
	}
	encoded, err := json.Marshal(map[string]any{"event": map[string]any{
		"operator": map[string]any{"open_id": actor},
		"context":  map[string]any{"open_chat_id": "chat-1", "open_message_id": "om-goal-card"},
		"action":   map[string]any{"value": value},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var callback larkcallback.CardActionTriggerEvent
	if err := json.Unmarshal(encoded, &callback); err != nil {
		t.Fatal(err)
	}
	action, ok := gateway.ParseCardActionTriggerEvent(gateway.RoutingEnv{
		GatewayID: "goal-test", SurfaceForCardAction: func(gateway.CardActionSurfaceLookup) string { return surfaceID },
	}, &callback)
	if !ok {
		t.Fatal("gateway rejected rendered Goal callback")
	}
	return action
}

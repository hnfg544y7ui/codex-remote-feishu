package gateway

import (
	"strconv"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestPlanInboundMessageEventNaturalGoalPreservesIntentAndMetadata(t *testing.T) {
	const text = "创建Goal：完成登录重构，预算5万tokens"
	event := naturalGoalP2PEvent("om-goal-new", text)
	recorded := false
	planned, ok, err := PlanInboundMessageEvent(naturalGoalInboundEnv(func(_, _ string) {
		recorded = true
	}), event)
	if err != nil {
		t.Fatalf("PlanInboundMessageEvent returned error: %v", err)
	}
	if !ok || planned.Action == nil || planned.Queue != nil {
		t.Fatalf("expected natural Goal action, ok=%v planned=%#v", ok, planned)
	}
	if recorded {
		t.Fatal("natural Goal control action must not enter the queued-message lane")
	}
	action := planned.Action
	if action.Kind != control.ActionGoalCommand || action.CommandID != control.FeishuCommandGoal {
		t.Fatalf("unexpected command identity: %#v", action)
	}
	if action.Text != text {
		t.Fatalf("Text = %q, want original input %q", action.Text, text)
	}
	if action.NaturalGoal == nil || action.NaturalGoal.Operation != "new" || action.NaturalGoal.Objective != "完成登录重构" {
		t.Fatalf("unexpected natural Goal intent: %#v", action.NaturalGoal)
	}
	if action.NaturalGoal.TokenBudget == nil || *action.NaturalGoal.TokenBudget != 50_000 {
		t.Fatalf("token budget = %#v, want 50000", action.NaturalGoal.TokenBudget)
	}
	if action.GatewayID != "app-goal" || action.SurfaceSessionID != "feishu:app-goal:user:ou_user" || action.ChatID != "oc_goal" || action.ActorUserID != "ou_user" || action.MessageID != "om-goal-new" {
		t.Fatalf("unexpected routing metadata: %#v", action)
	}
	if action.Inbound == nil || action.Inbound.EventID != "evt-om-goal-new" || action.Inbound.EventType != "im.message.receive_v1" || action.Inbound.RequestID != "req-om-goal-new" || action.Inbound.OpenMessageID != "om-goal-new" {
		t.Fatalf("unexpected inbound metadata: %#v", action.Inbound)
	}
}

func TestPlanInboundMessageEventNaturalGoalControlCommandsBypassQueue(t *testing.T) {
	for _, test := range []struct {
		name      string
		text      string
		operation string
	}{
		{name: "pause", text: "暂停当前Goal", operation: "pause"},
		{name: "get", text: "查看当前Goal状态", operation: "get"},
		{name: "resume", text: "继续执行目标", operation: "resume"},
	} {
		t.Run(test.name, func(t *testing.T) {
			planned, ok, err := PlanInboundMessageEvent(naturalGoalInboundEnv(func(_, _ string) {
				t.Fatal("control action must not be recorded as a queued message")
			}), naturalGoalP2PEvent("om-goal-"+test.name, test.text))
			if err != nil {
				t.Fatalf("PlanInboundMessageEvent returned error: %v", err)
			}
			if !ok || planned.Action == nil || planned.Queue != nil || planned.Action.NaturalGoal == nil {
				t.Fatalf("expected direct natural Goal action, ok=%v planned=%#v", ok, planned)
			}
			if planned.Action.NaturalGoal.Operation != test.operation || planned.Action.Text != test.text {
				t.Fatalf("unexpected action: %#v", planned.Action)
			}
		})
	}
}

func TestPlanInboundMessageEventNaturalGoalUsesExistingGroupAuthorization(t *testing.T) {
	t.Run("current bot mention", func(t *testing.T) {
		env := naturalGoalInboundEnv(func(_, _ string) {
			t.Fatal("mentioned control action must not be queued")
		})
		env.BotOpenID = "ou_bot"
		env.PrimaryGatewayForChat = func(string) string { return "another-gateway" }
		event := groupTextEventWithCurrentBotMention("om-goal-mentioned", "oc_chat", "@_user_1 暂停当前Goal", "OpenCode")

		planned, ok, err := PlanInboundMessageEvent(env, event)
		if err != nil {
			t.Fatalf("PlanInboundMessageEvent returned error: %v", err)
		}
		if !ok || planned.Action == nil || planned.Queue != nil || planned.Action.NaturalGoal == nil {
			t.Fatalf("expected mentioned natural Goal action, ok=%v planned=%#v", ok, planned)
		}
		if planned.Action.ActorUserID != "ou_user" || planned.Action.NaturalGoal.Operation != "pause" || planned.Action.Text != "暂停当前Goal" {
			t.Fatalf("unexpected mentioned action: %#v", planned.Action)
		}
	})

	t.Run("primary gateway without mention", func(t *testing.T) {
		env := naturalGoalInboundEnv(func(_, _ string) {
			t.Fatal("primary-gateway control action must not be queued")
		})
		env.BotOpenID = "ou_bot"
		env.PrimaryGatewayForChat = func(string) string { return "app-goal" }

		planned, ok, err := PlanInboundMessageEvent(env, groupTextEventWithoutMention("om-goal-primary", "oc_chat", "查看当前Goal状态"))
		if err != nil {
			t.Fatalf("PlanInboundMessageEvent returned error: %v", err)
		}
		if !ok || planned.Action == nil || planned.Action.NaturalGoal == nil || planned.Action.NaturalGoal.Operation != "get" {
			t.Fatalf("expected primary-gateway natural Goal action, ok=%v planned=%#v", ok, planned)
		}
	})

	t.Run("mentioned slash stays slash", func(t *testing.T) {
		env := naturalGoalInboundEnv(func(_, _ string) {
			t.Fatal("mentioned slash action must not be queued")
		})
		env.BotOpenID = "ou_bot"
		env.PrimaryGatewayForChat = func(string) string { return "another-gateway" }

		planned, ok, err := PlanInboundMessageEvent(env, groupTextEventWithCurrentBotMention("om-goal-slash", "oc_chat", "@_user_1 /goal pause", "OpenCode"))
		if err != nil {
			t.Fatalf("PlanInboundMessageEvent returned error: %v", err)
		}
		if !ok || planned.Action == nil || planned.Queue != nil {
			t.Fatalf("expected existing mentioned slash action, ok=%v planned=%#v", ok, planned)
		}
		if planned.Action.Kind != control.ActionGoalCommand || planned.Action.CommandID != control.FeishuCommandGoal || planned.Action.Text != "/goal pause" || planned.Action.NaturalGoal != nil {
			t.Fatalf("mentioned slash action changed unexpectedly: %#v", planned.Action)
		}
	})

	t.Run("non-primary gateway without mention", func(t *testing.T) {
		recorded := false
		env := naturalGoalInboundEnv(func(_, _ string) { recorded = true })
		env.BotOpenID = "ou_bot"
		env.PrimaryGatewayForChat = func(string) string { return "another-gateway" }

		planned, ok, err := PlanInboundMessageEvent(env, groupTextEventWithoutMention("om-goal-not-primary", "oc_chat", "暂停当前Goal"))
		if err != nil {
			t.Fatalf("PlanInboundMessageEvent returned error: %v", err)
		}
		if ok || planned.Action != nil || planned.Queue != nil || recorded {
			t.Fatalf("expected existing group gate to ignore message, ok=%v planned=%#v recorded=%v", ok, planned, recorded)
		}
	})
}

func TestPlanInboundMessageEventNaturalGoalDoesNotCaptureOtherMessages(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
	}{
		{name: "ordinary task", text: "帮我修复登录测试"},
		{name: "negative", text: "不要暂停当前Goal"},
		{name: "quoted", text: "“暂停当前Goal”"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorded := false
			planned, ok, err := PlanInboundMessageEvent(naturalGoalInboundEnv(func(_, _ string) {
				recorded = true
			}), naturalGoalP2PEvent("om-nongoal-"+test.name, test.text))
			if err != nil {
				t.Fatalf("PlanInboundMessageEvent returned error: %v", err)
			}
			if !ok || planned.Action != nil || planned.Queue == nil || !recorded {
				t.Fatalf("expected ordinary text queue, ok=%v planned=%#v recorded=%v", ok, planned, recorded)
			}
			if planned.Queue.text != test.text {
				t.Fatalf("queued text = %q, want %q", planned.Queue.text, test.text)
			}
		})
	}

	t.Run("rich post", func(t *testing.T) {
		event := naturalGoalP2PEvent("om-goal-post", "")
		event.Event.Message.MessageType = stringRef("post")
		event.Event.Message.Content = stringRef(`{"title":"","content":[[{"tag":"text","text":"暂停当前Goal"},{"tag":"img","image_key":"img-key"}]]}`)
		recorded := false
		planned, ok, err := PlanInboundMessageEvent(naturalGoalInboundEnv(func(_, _ string) {
			recorded = true
		}), event)
		if err != nil {
			t.Fatalf("PlanInboundMessageEvent returned error: %v", err)
		}
		if !ok || planned.Action != nil || planned.Queue == nil || planned.Queue.messageType != "post" || !recorded {
			t.Fatalf("expected post queue without natural Goal parsing, ok=%v planned=%#v recorded=%v", ok, planned, recorded)
		}
	})
}

func TestPlanInboundMessageEventNaturalGoalRequiresActorWithoutChangingSlash(t *testing.T) {
	naturalEvent := naturalGoalP2PEvent("om-goal-no-actor", "暂停当前Goal")
	naturalEvent.Event.Sender = nil
	recorded := false
	planned, ok, err := PlanInboundMessageEvent(naturalGoalInboundEnv(func(_, _ string) {
		recorded = true
	}), naturalEvent)
	if err != nil {
		t.Fatalf("PlanInboundMessageEvent returned error: %v", err)
	}
	if !ok || planned.Action != nil || planned.Queue == nil || !recorded {
		t.Fatalf("expected actorless natural text to queue, ok=%v planned=%#v recorded=%v", ok, planned, recorded)
	}

	slashEvent := naturalGoalP2PEvent("om-slash-no-actor", "/goal pause")
	slashEvent.Event.Sender = nil
	planned, ok, err = PlanInboundMessageEvent(naturalGoalInboundEnv(func(_, _ string) {
		t.Fatal("slash action must not be queued")
	}), slashEvent)
	if err != nil {
		t.Fatalf("PlanInboundMessageEvent returned error: %v", err)
	}
	if !ok || planned.Action == nil || planned.Queue != nil {
		t.Fatalf("expected existing slash action, ok=%v planned=%#v", ok, planned)
	}
	if planned.Action.Kind != control.ActionGoalCommand || planned.Action.CommandID != control.FeishuCommandGoal || planned.Action.Text != "/goal pause" || planned.Action.NaturalGoal != nil || planned.Action.ActorUserID != "" {
		t.Fatalf("slash action changed unexpectedly: %#v", planned.Action)
	}
}

func naturalGoalInboundEnv(record func(messageID, surfaceSessionID string)) InboundEnv {
	return InboundEnv{
		GatewayID:                     "app-goal",
		ParseTextActionWithoutCatalog: parseTextAction,
		RecordSurfaceMessage:          record,
	}
}

func naturalGoalP2PEvent(messageID, text string) *larkim.P2MessageReceiveV1 {
	return &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{
			Header: &larkevent.EventHeader{
				EventID:    "evt-" + messageID,
				EventType:  "im.message.receive_v1",
				CreateTime: "1710000000000",
			},
		},
		EventReq: &larkevent.EventReq{Header: map[string][]string{
			larkcore.HttpHeaderKeyRequestId: {"req-" + messageID},
		}},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: stringRef("ou_user")},
				SenderType: stringRef("user"),
			},
			Message: &larkim.EventMessage{
				MessageId:   stringRef(messageID),
				ChatId:      stringRef("oc_goal"),
				ChatType:    stringRef("p2p"),
				MessageType: stringRef("text"),
				CreateTime:  stringRef("1710000000123"),
				Content:     stringRef(`{"text":` + strconv.Quote(text) + `}`),
			},
		},
	}
}

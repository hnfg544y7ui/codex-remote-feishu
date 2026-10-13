package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

func TestInboundTopicMessageRoutesAllInputTypes(t *testing.T) {
	for _, tc := range []struct {
		kind, content string
		want          control.ActionKind
	}{
		{"text", `{"text":"hello"}`, control.ActionTextMessage},
		{"image", `{"image_key":"image-key"}`, control.ActionImageMessage},
		{"file", `{"file_key":"file-key","file_name":"file.txt"}`, control.ActionFileMessage},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			env := naturalGoalInboundEnv(func(_, _ string) {})
			env.BotOpenID = "ou_bot"
			env.PrimaryGatewayForChat = func(string) string { return "app-goal" }
			env.DownloadImage = func(context.Context, string, string) (string, string, error) { return "image.png", "image/png", nil }
			env.DownloadFile = func(context.Context, string, string, string) (string, error) { return "file.txt", nil }
			ev := naturalGoalP2PEvent("om_input", "hello")
			msg := ev.Event.Message
			msg.ChatType = stringValueRef("group")
			msg.ChatId = stringValueRef("oc_1")
			msg.RootId = stringValueRef("om_root")
			msg.ThreadId = stringValueRef("omt_1")
			msg.MessageType = stringValueRef(tc.kind)
			msg.Content = stringValueRef(tc.content)
			plan, ok, err := PlanInboundMessageEvent(env, ev)
			if err != nil || !ok || plan.Queue == nil {
				t.Fatalf("plan=%#v ok=%t err=%v", plan, ok, err)
			}
			action, ok, err := plan.Queue.parseAction(t.Context(), env)
			surface := feishuidentity.TopicRef("app-goal", "oc_1", "om_root").SurfaceID()
			if err != nil || !ok || action.Kind != tc.want || action.SurfaceSessionID != surface || action.FeishuTopicID != "omt_1" || action.FeishuTopicRootMessageID != "om_root" {
				t.Fatalf("action=%#v ok=%t err=%v", action, ok, err)
			}
			if value := plan.Queue.eventMessage().ThreadId; value == nil || *value != "omt_1" {
				t.Fatal("async lane dropped native topic ID")
			}
		})
	}
}

func TestInboundTopicMissingRootFailsClosedAndMappingRecovers(t *testing.T) {
	env := naturalGoalInboundEnv(func(_, _ string) {})
	env.BotOpenID = "ou_bot"
	env.PrimaryGatewayForChat = func(string) string { return "app-goal" }
	ev := naturalGoalP2PEvent("om_input", "hello")
	ev.Event.Message.ChatType = stringValueRef("group")
	ev.Event.Message.ChatId = stringValueRef("oc_1")
	ev.Event.Message.ThreadId = stringValueRef("omt_1")
	if plan, ok, err := PlanInboundMessageEvent(env, ev); err == nil || ok || plan.Queue != nil || plan.Action != nil {
		t.Fatalf("unknown topic used group: %#v %t %v", plan, ok, err)
	}
	env.ResolveTopicSurface = func(chat, native, root string) string {
		return feishuidentity.TopicRef("app-goal", "oc_1", "om_root").SurfaceID()
	}
	plan, ok, err := PlanInboundMessageEvent(env, ev)
	if err != nil || !ok || plan.Queue == nil || plan.Queue.rootMessageID != "om_root" {
		t.Fatalf("mapping recovery %#v %t %v", plan, ok, err)
	}
	env.ResolveTopicSurface = func(chat, native, root string) string {
		return feishuidentity.TopicRef("app-goal", "oc_other", "om_root").SurfaceID()
	}
	if _, ok, err := PlanInboundMessageEvent(env, ev); err == nil || ok {
		t.Fatal("accepted cross-chat mapping")
	}
}

func TestLegacyQuotedGroupInputDoesNotBecomeTopic(t *testing.T) {
	env := naturalGoalInboundEnv(func(_, _ string) {})
	env.BotOpenID = "ou_bot"
	env.PrimaryGatewayForChat = func(string) string { return "app-goal" }
	ev := naturalGoalP2PEvent("om_input", "hello")
	ev.Event.Message.ChatType = stringValueRef("group")
	ev.Event.Message.ChatId = stringValueRef("oc_1")
	ev.Event.Message.RootId = stringValueRef("om_old_group_message")
	plan, ok, err := PlanInboundMessageEvent(env, ev)
	if err != nil || !ok || plan.Queue == nil || plan.Queue.surfaceID != "feishu:app-goal:chat:oc_1" {
		t.Fatalf("legacy route changed %#v %t %v", plan, ok, err)
	}
}

func TestInboundTopicSlashCommandKeepsNativeMetadata(t *testing.T) {
	env := naturalGoalInboundEnv(func(_, _ string) {})
	env.BotOpenID = "ou_bot"
	env.PrimaryGatewayForChat = func(string) string { return "app-goal" }
	ev := naturalGoalP2PEvent("om_input", "/status")
	ev.Event.Message.ChatType = stringValueRef("group")
	ev.Event.Message.ChatId = stringValueRef("oc_1")
	ev.Event.Message.RootId = stringValueRef("om_root")
	ev.Event.Message.ThreadId = stringValueRef("omt_1")
	data, _ := json.Marshal(map[string]string{"text": "/status"})
	ev.Event.Message.Content = stringValueRef(string(data))
	plan, ok, err := PlanInboundMessageEvent(env, ev)
	if err != nil || !ok || plan.Action == nil || plan.Action.FeishuTopicID != "omt_1" || plan.Action.FeishuTopicRootMessageID != "om_root" {
		t.Fatalf("metadata dropped %#v %t %v", plan, ok, err)
	}
}

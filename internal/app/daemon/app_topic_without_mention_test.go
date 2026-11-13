package daemon

import (
	"context"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	feishugateway "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/gateway"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestRestoredTopicWithoutMentionKeepsOwnerAndRoomPolicy(t *testing.T) {
	stateDir := t.TempDir()
	app := newRestoreHintTestApp(stateDir)
	surfaceID, events := topicAppCreatedEvents(t, app, "om_topic_root")
	app.gateway = &topicReceiptTestGateway{apply: func(ops []feishu.Operation) error {
		ops[0].MessageID = "om_welcome"
		ops[0].FeishuTopicID = "omt_known"
		ops[0].FeishuTopicRootMessageID = "om_topic_root"
		return nil
	}}
	app.mu.Lock()
	err := app.deliverUIEventLocked(context.Background(), events[0])
	app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	restored := newRestoreHintTestApp(stateDir)
	hooks := restored.gatewayRuntimeHooks()
	env := feishugateway.InboundEnv{
		GatewayID: "app-1", BotOpenID: "ou_bot",
		PrimaryGatewayForChat: hooks.PrimaryGatewayForChat,
		ResolveTopicSurface: func(chat, native, root string) string {
			return hooks.ResolveTopicSurface("app-1", chat, native, root)
		},
		ParseTextActionWithoutCatalog: control.ParseFeishuTextActionWithoutCatalog,
		RecordSurfaceMessage:          func(string, string) {},
	}
	if hooks.PrimaryGatewayForChat("oc_room") != "" {
		t.Fatal("fixture unexpectedly configured a primary bot")
	}
	ptr := func(v string) *string { return &v }
	message := func(actor, native, root string) *larkim.P2MessageReceiveV1 {
		return &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{SenderId: &larkim.UserId{OpenId: ptr(actor)}, SenderType: ptr("user")},
			Message: &larkim.EventMessage{MessageId: ptr("om_input"), ChatId: ptr("oc_room"), ChatType: ptr("group"),
				ThreadId: ptr(native), RootId: ptr(root), MessageType: ptr("text"), Content: ptr(`{"text":"/use"}`)},
		}}
	}
	parse := func(actor, native, root string) *control.Action {
		t.Helper()
		plan, ok, err := feishugateway.PlanInboundMessageEvent(env, message(actor, native, root))
		if err != nil || !ok || plan.Action == nil || plan.Action.SurfaceSessionID != surfaceID {
			t.Fatalf("known persisted topic did not route without mention: ok=%v err=%v", ok, err)
		}
		return plan.Action
	}
	owner := parse("ou_owner", "omt_known", "om_topic_root")
	if blocked := restored.service.ValidateFeishuTopicAction(*owner); len(blocked) != 0 {
		t.Fatal("topic owner could not open /use without a group primary")
	}
	for _, tc := range []struct{ actor, native, code string }{
		{"ou_other", "omt_known", "topic_owner_required"},
		{"ou_owner", "omt_other", "topic_route_mismatch"},
	} {
		action := parse(tc.actor, tc.native, "om_topic_root")
		blocked := restored.service.ValidateFeishuTopicAction(*action)
		if len(blocked) != 1 || blocked[0].Notice == nil || blocked[0].Notice.Code != tc.code {
			t.Fatalf("unmentioned topic action bypassed %s", tc.code)
		}
	}
	if plan, ok, err := feishugateway.PlanInboundMessageEvent(env, message("ou_owner", "omt_unknown", "om_unknown")); err != nil || ok || plan.Action != nil || plan.Queue != nil {
		t.Fatal("derived unknown topic was treated as trusted")
	}
	if plan, ok, err := feishugateway.PlanInboundMessageEvent(env, message("ou_owner", "", "")); err != nil || ok || plan.Action != nil || plan.Queue != nil {
		t.Fatal("topic exception changed the main group policy")
	}
	surface := restored.topicSurfaceLocked(surfaceID)
	surface.FeishuTopicClosed = true
	blocked := restored.service.ValidateFeishuTopicAction(*parse("ou_owner", "omt_known", "om_topic_root"))
	if len(blocked) != 1 || blocked[0].Notice == nil || blocked[0].Notice.Code != "topic_closed" {
		t.Fatal("closed topic resumed task admission without a mention")
	}
	if hooks.PrimaryGatewayForChat("oc_room") != "" || len(surface.QueueItems) != 0 {
		t.Fatal("routing a topic changed the room primary or dispatched work")
	}
}

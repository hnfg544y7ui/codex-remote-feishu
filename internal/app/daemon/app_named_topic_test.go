package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	feishugateway "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/gateway"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func namedTopicCommand() control.Action {
	return control.Action{Kind: control.ActionTopicCommand, SurfaceSessionID: "feishu:app-1:chat:oc_room", GatewayID: "app-1", ChatID: "oc_room", ActorUserID: "ou_owner", MessageID: "om_command", Value: "new 多目相机调试", Text: "/topic new 多目相机调试"}
}

func namedTopicTestGateway(t *testing.T, root string, creates *int) *topicReceiptTestGateway {
	t.Helper()
	return &topicReceiptTestGateway{apply: func(ops []feishu.Operation) error {
		for i := range ops {
			op := &ops[i]
			if op.Kind == feishu.OperationCreateTopicRoot {
				*creates++
				if op.Text != "多目相机调试" || op.ReplyToMessageID != "" || op.ReplyInThread || len(op.IdempotencyKey) != 32 {
					t.Fatalf("invalid root op %#v", op)
				}
				op.MessageID = root
			} else if op.ReplyInThread {
				if op.ReplyToMessageID != root {
					t.Fatalf("output used command instead of root: %#v", op)
				}
				op.MessageID = "om_welcome"
				op.FeishuTopicID = "omt_named"
				op.FeishuTopicRootMessageID = root
			}
		}
		return nil
	}}
}

func TestNamedTopicCreationRestartsWithHumanOwnerAndNoMention(t *testing.T) {
	stateDir := t.TempDir()
	app := newRestoreHintTestApp(stateDir)
	creates := 0
	app.gateway = namedTopicTestGateway(t, "om_title", &creates)
	app.handleAction(t.Context(), namedTopicCommand())
	surfaceID := feishuidentity.TopicRef("app-1", "oc_room", "om_title").SurfaceID()
	surface := app.topicSurfaceLocked(surfaceID)
	if surface == nil || surface.FeishuTopicOwnerID != "ou_owner" || surface.FeishuTopicTitle != "多目相机调试" || surface.FeishuTopicID != "omt_named" || app.topicSurfaceLocked(feishuidentity.TopicRef("app-1", "oc_room", "om_command").SurfaceID()) != nil {
		t.Fatalf("new root binding incorrect: %#v", surface)
	}
	app.handleAction(t.Context(), namedTopicCommand())
	restored := newRestoreHintTestApp(stateDir)
	restored.gateway = namedTopicTestGateway(t, "om_title", &creates)
	restored.handleAction(t.Context(), namedTopicCommand())
	if creates != 1 {
		t.Fatalf("duplicate/restart sent %d roots", creates)
	}
	hooks := restored.gatewayRuntimeHooks()
	ptr := func(s string) *string { return &s }
	event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender:  &larkim.EventSender{SenderId: &larkim.UserId{OpenId: ptr("ou_owner")}, SenderType: ptr("user")},
		Message: &larkim.EventMessage{MessageId: ptr("om_use"), ChatId: ptr("oc_room"), ChatType: ptr("group"), ThreadId: ptr("omt_named"), RootId: ptr("om_title"), MessageType: ptr("text"), Content: ptr(`{"text":"/use"}`)},
	}}
	env := feishugateway.InboundEnv{GatewayID: "app-1", BotOpenID: "ou_bot", ResolveTopicSurface: func(chat, native, root string) string { return hooks.ResolveTopicSurface("app-1", chat, native, root) }, ParseTextActionWithoutCatalog: control.ParseFeishuTextActionWithoutCatalog, RecordSurfaceMessage: func(string, string) {}}
	plan, ok, err := feishugateway.PlanInboundMessageEvent(env, event)
	if err != nil || !ok || plan.Action == nil || plan.Action.SurfaceSessionID != surfaceID || len(restored.service.ValidateFeishuTopicAction(*plan.Action)) != 0 {
		t.Fatalf("new root could not route human /use without mention: %#v %v", plan, err)
	}
	plan.Action.ActorUserID = "ou_other"
	if len(restored.service.ValidateFeishuTopicAction(*plan.Action)) != 1 {
		t.Fatal("bot root lost human owner check")
	}
}

func TestNamedTopicCreationUnknownNeverReplaysAfterRestart(t *testing.T) {
	stateDir := t.TempDir()
	app := newRestoreHintTestApp(stateDir)
	sends := 0
	app.gateway = &topicReceiptTestGateway{apply: func(ops []feishu.Operation) error {
		if ops[0].Kind == feishu.OperationCreateTopicRoot {
			sends++
			return errors.New("connection lost after send")
		}
		return nil
	}}
	app.handleAction(t.Context(), namedTopicCommand())
	restored := newRestoreHintTestApp(stateDir)
	restored.gateway = app.gateway
	restored.handleAction(t.Context(), namedTopicCommand())
	if sends != 1 || restored.resolveTopicSurface("app-1", "oc_room", "", "om_command") != "" {
		t.Fatal("unknown send replayed or bound command")
	}
	for _, s := range restored.service.Surfaces() {
		if s.FeishuTopicOwnerID != "" {
			t.Fatal("unknown send established topic")
		}
	}
}

func TestNamedTopicCreationPendingAndLegacyGates(t *testing.T) {
	app := newRestoreHintTestApp(t.TempDir())
	creates := 0
	app.gateway = &topicReceiptTestGateway{apply: func(ops []feishu.Operation) error {
		if ops[0].Kind == feishu.OperationCreateTopicRoot {
			creates++
			// Apply releases App.mu. Duplicate ingress while the root is in flight
			// must see the persisted intent and must not issue another send.
			app.handleAction(context.Background(), namedTopicCommand())
			ops[0].MessageID = "om_title"
		}
		if ops[0].ReplyInThread {
			ops[0].MessageID = "om_welcome"
			ops[0].FeishuTopicID = "omt_named"
			ops[0].FeishuTopicRootMessageID = "om_title"
		}
		return nil
	}}
	app.handleAction(t.Context(), namedTopicCommand())
	if creates != 1 {
		t.Fatal("concurrent duplicate created another root")
	}
	legacy := newRestoreHintTestApp(t.TempDir())
	surfaceID, _ := topicAppCreatedEvents(t, legacy, "om_command")
	if err := legacy.persistTopicSurfaceLocked(legacy.topicSurfaceLocked(surfaceID)); err != nil {
		t.Fatal(err)
	}
	legacy.gateway = namedTopicTestGateway(t, "om_command", &creates)
	legacy.handleAction(t.Context(), namedTopicCommand())
	if creates != 1 {
		t.Fatal("old topic was migrated or recreated")
	}
}

func TestNamedTopicCreationJournalFailureDoesNotSendRoot(t *testing.T) {
	app := newRestoreHintTestApp(t.TempDir())
	if err := os.WriteFile(filepath.Join(app.headlessRuntime.Paths.StateDir, "feishu-topic-creations"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	app.gateway = &topicReceiptTestGateway{apply: func(ops []feishu.Operation) error {
		if ops[0].Kind == feishu.OperationCreateTopicRoot {
			t.Fatal("sent before durable intent")
		}
		return nil
	}}
	app.handleAction(t.Context(), namedTopicCommand())
	app.mu.Lock()
	long := namedTopicCommand()
	long.Value = "new " + strings.Repeat("名", 201)
	_, err := app.prepareNamedTopicLocked(t.Context(), long)
	app.mu.Unlock()
	if err == nil {
		t.Fatal("accepted oversized title")
	}
}

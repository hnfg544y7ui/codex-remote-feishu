package gateway

import (
	"context"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func boundTopicInboundEvent(kind, content string) *larkim.P2MessageReceiveV1 {
	event := naturalGoalP2PEvent("om_topic_input", "hello")
	message := event.Event.Message
	message.ChatType = stringValueRef("group")
	message.ChatId = stringValueRef("oc_topic_chat")
	message.RootId = stringValueRef("om_topic_root")
	message.ThreadId = stringValueRef("omt_topic")
	message.MessageType = stringValueRef(kind)
	message.Content = stringValueRef(content)
	return event
}

func boundTopicInboundEnv(t *testing.T) InboundEnv {
	t.Helper()
	env := naturalGoalInboundEnv(func(_, _ string) {})
	env.BotOpenID = "ou_bot"
	env.ResolveTopicSurface = func(chat, native, root string) string {
		if chat == "oc_topic_chat" && (native == "omt_topic" || root == "om_topic_root") {
			return feishuidentity.TopicRef("app-goal", "oc_topic_chat", "om_topic_root").SurfaceID()
		}
		return ""
	}
	return env
}

func TestBoundTopicWithoutMentionRoutesAllInputsWithoutPrimary(t *testing.T) {
	for _, primary := range []string{"other-gateway", "", "no-lookup"} {
		t.Run(primary, func(t *testing.T) {
			for _, tc := range []struct {
				kind, content string
				want          control.ActionKind
				command       bool
			}{
				{kind: "text", content: `{"text":"继续检查"}`, want: control.ActionTextMessage},
				{kind: "text", content: `{"text":"/use"}`, command: true},
				{kind: "image", content: `{"image_key":"image-key"}`, want: control.ActionImageMessage},
				{kind: "file", content: `{"file_key":"file-key","file_name":"file.txt"}`, want: control.ActionFileMessage},
			} {
				t.Run(tc.kind+tc.content, func(t *testing.T) {
					env := boundTopicInboundEnv(t)
					currentPrimary := primary
					if primary != "no-lookup" {
						env.PrimaryGatewayForChat = func(string) string { return currentPrimary }
					}
					lookups, records, images, files := 0, 0, 0, 0
					resolver := env.ResolveTopicSurface
					env.ResolveTopicSurface = func(chat, native, root string) string { lookups++; return resolver(chat, native, root) }
					env.RecordSurfaceMessage = func(_, _ string) { records++ }
					env.DownloadImage = func(context.Context, string, string) (string, string, error) {
						images++
						return "image.png", "image/png", nil
					}
					env.DownloadFile = func(context.Context, string, string, string) (string, error) { files++; return "file.txt", nil }
					plan, ok, err := PlanInboundMessageEvent(env, boundTopicInboundEvent(tc.kind, tc.content))
					if err != nil || !ok {
						t.Fatalf("known topic blocked: ok=%v err=%v", ok, err)
					}
					var action control.Action
					if tc.command {
						if plan.Action == nil || plan.Queue != nil {
							t.Fatal("known topic slash command was not parsed")
						}
						action = *plan.Action
					} else {
						if plan.Queue == nil || plan.Action != nil {
							t.Fatal("known topic task did not enter input lane")
						}
						if images != 0 || files != 0 {
							t.Fatal("attachment downloaded during routing")
						}
						action, ok, err = plan.Queue.parseAction(t.Context(), env)
						if err != nil || !ok || action.Kind != tc.want {
							t.Fatalf("task parse failed: kind=%s ok=%v err=%v", action.Kind, ok, err)
						}
					}
					expected := feishuidentity.TopicRef("app-goal", "oc_topic_chat", "om_topic_root").SurfaceID()
					if action.SurfaceSessionID != expected || action.GatewayID != "app-goal" || action.ChatID != "oc_topic_chat" || action.ActorUserID != "ou_user" || action.FeishuTopicID != "omt_topic" || action.FeishuTopicRootMessageID != "om_topic_root" {
						t.Fatalf("topic identity lost: %#v", action)
					}
					if currentPrimary != primary || lookups != 1 {
						t.Fatal("routing changed primary or looked up topic more than once")
					}
					if tc.kind == "image" && images != 1 || tc.kind == "file" && files != 1 {
						t.Fatal("accepted media did not use existing downloader")
					}
					if !tc.command && records != 1 {
						t.Fatal("accepted task did not preserve message tracking")
					}
				})
			}
		})
	}
}

func TestBoundTopicWithoutMentionOnlyAcceptsTrustedMatchingMapping(t *testing.T) {
	for _, tc := range []struct {
		name, mapped string
		root, native string
		wantError    bool
	}{
		{name: "unknown-native-topic", root: "om_topic_root", native: "omt_topic"},
		{name: "forged-quoted-root", root: "om_unrecognized_quote"},
		{name: "invalid-map", root: "om_topic_root", native: "omt_topic", mapped: "forged", wantError: true},
		{name: "group-map", root: "om_topic_root", native: "omt_topic", mapped: "feishu:app-goal:chat:oc_topic_chat", wantError: true},
		{name: "cross-gateway", root: "om_topic_root", native: "omt_topic", mapped: feishuidentity.TopicRef("other-gateway", "oc_topic_chat", "om_topic_root").SurfaceID(), wantError: true},
		{name: "cross-chat", root: "om_topic_root", native: "omt_topic", mapped: feishuidentity.TopicRef("app-goal", "oc_other", "om_topic_root").SurfaceID(), wantError: true},
		{name: "root-mismatch", root: "om_topic_root", native: "omt_topic", mapped: feishuidentity.TopicRef("app-goal", "oc_topic_chat", "om_other").SurfaceID(), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := boundTopicInboundEnv(t)
			env.PrimaryGatewayForChat = func(string) string { return "other-gateway" }
			env.ResolveTopicSurface = func(_, _, _ string) string { return tc.mapped }
			event := boundTopicInboundEvent("image", `{"image_key":"image-key"}`)
			event.Event.Message.RootId = stringValueRef(tc.root)
			event.Event.Message.ThreadId = stringValueRef(tc.native)
			records, downloads, dispatched := 0, 0, 0
			env.RecordSurfaceMessage = func(_, _ string) { records++ }
			env.DownloadImage = func(context.Context, string, string) (string, string, error) {
				downloads++
				return "image.png", "image/png", nil
			}
			err := HandleInboundMessageEvent(t.Context(), env, event, nil, func(context.Context, control.Action) error { dispatched++; return nil })
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v wantError=%v", err, tc.wantError)
			}
			if records != 0 || downloads != 0 || dispatched != 0 {
				t.Fatal("untrusted topic read media or dispatched work")
			}
		})
	}
}

func TestBoundTopicWithoutMentionKeepsIdentityAndMentionGates(t *testing.T) {
	for _, scenario := range []string{"app-sender", "bot-sender", "unknown-sender", "missing-actor", "missing-bot-identity", "other-bot-mentioned"} {
		t.Run(scenario, func(t *testing.T) {
			env := boundTopicInboundEnv(t)
			env.PrimaryGatewayForChat = func(string) string { return "other-gateway" }
			event := boundTopicInboundEvent("file", `{"file_key":"file-key","file_name":"file.txt"}`)
			switch scenario {
			case "app-sender":
				event.Event.Sender.SenderType = stringValueRef("app")
			case "bot-sender":
				event.Event.Sender.SenderType = stringValueRef("bot")
			case "unknown-sender":
				event.Event.Sender.SenderType = nil
			case "missing-actor":
				event.Event.Sender.SenderId = nil
			case "missing-bot-identity":
				env.BotOpenID = ""
			case "other-bot-mentioned":
				event.Event.Message.Mentions = []*larkim.MentionEvent{{Id: &larkim.UserId{OpenId: stringValueRef("ou_other_bot")}}}
			}
			downloads, records, dispatched := 0, 0, 0
			env.DownloadFile = func(context.Context, string, string, string) (string, error) { downloads++; return "file.txt", nil }
			env.RecordSurfaceMessage = func(_, _ string) { records++ }
			if err := HandleInboundMessageEvent(t.Context(), env, event, nil, func(context.Context, control.Action) error { dispatched++; return nil }); err != nil {
				t.Fatal(err)
			}
			if downloads != 0 || records != 0 || dispatched != 0 {
				t.Fatal("identity/mention gate was bypassed")
			}
		})
	}
}

func TestBoundTopicWithoutMentionAllowsKnownRootOrNativeID(t *testing.T) {
	for _, missing := range []string{"native", "root"} {
		t.Run(missing, func(t *testing.T) {
			env := boundTopicInboundEnv(t)
			event := boundTopicInboundEvent("text", `{"text":"继续"}`)
			if missing == "native" {
				event.Event.Message.ThreadId = nil
			} else {
				event.Event.Message.RootId = nil
			}
			plan, ok, err := PlanInboundMessageEvent(env, event)
			if err != nil || !ok || plan.Queue == nil || plan.Queue.rootMessageID != "om_topic_root" {
				t.Fatalf("known topic was not resolved: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestBoundTopicExceptionDoesNotChangeGroupMainQuotesOrP2P(t *testing.T) {
	for _, scenario := range []string{"group-main-primary", "group-main-other", "unknown-quote-primary", "p2p"} {
		t.Run(scenario, func(t *testing.T) {
			env := boundTopicInboundEnv(t)
			event := boundTopicInboundEvent("text", `{"text":"hello"}`)
			message := event.Event.Message
			resolverCalls := 0
			env.ResolveTopicSurface = func(_, _, _ string) string { resolverCalls++; return "" }
			env.PrimaryGatewayForChat = func(string) string { return "app-goal" }
			switch scenario {
			case "group-main-primary", "group-main-other":
				message.RootId = nil
				message.ThreadId = nil
				// No topic evidence means even a buggy resolver cannot turn group-main
				// traffic into a known topic and bypass the primary bot policy.
				env.ResolveTopicSurface = func(_, _, _ string) string {
					resolverCalls++
					return feishuidentity.TopicRef("app-goal", "oc_topic_chat", "om_topic_root").SurfaceID()
				}
				if scenario == "group-main-other" {
					env.PrimaryGatewayForChat = func(string) string { return "other-gateway" }
				}
			case "unknown-quote-primary":
				message.ThreadId = nil
				message.RootId = stringValueRef("om_unknown_quote")
			case "p2p":
				message.ChatType = stringValueRef("p2p")
				env.BotOpenID = ""
				env.PrimaryGatewayForChat = nil
			}
			plan, ok, err := PlanInboundMessageEvent(env, event)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "group-main-other" {
				if ok || plan.Action != nil || plan.Queue != nil {
					t.Fatal("group-main bypassed primary")
				}
				return
			}
			if !ok || plan.Queue == nil {
				t.Fatal("legacy route was blocked")
			}
			expected := SurfaceIDForInbound("app-goal", "oc_topic_chat", *message.ChatType, "ou_user")
			if plan.Queue.surfaceID != expected {
				t.Fatalf("legacy route became topic: %q", plan.Queue.surfaceID)
			}
			if scenario != "unknown-quote-primary" && resolverCalls != 0 {
				t.Fatal("non-topic route consulted topic binding")
			}
		})
	}
}

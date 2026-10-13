package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gatewaypkg "github.com/kxn/codex-remote-feishu/internal/adapter/feishu/gateway"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func topicTestReply(root, chat, native string) *larkim.ReplyMessageResp {
	return &larkim.ReplyMessageResp{Data: &larkim.ReplyMessageRespData{
		MessageId: stringRef("om_topic_reply"), RootId: stringRef(root), ChatId: stringRef(chat), ThreadId: stringRef(native),
		MessageAppLink: stringRef("https://example.test/topic"),
	}}
}

func TestTopicTransportReplyFailureNeverFallsBack(t *testing.T) {
	for _, kind := range []OperationKind{OperationSendText, OperationSendCard, OperationSendImage} {
		t.Run(string(kind), func(t *testing.T) {
			g := NewLiveGateway(LiveGatewayConfig{GatewayID: "app-1"})
			g.createMessageFn = func(context.Context, string, string, string, string) (*larkim.CreateMessageResp, error) {
				t.Fatal("topic output leaked to group create")
				return nil, nil
			}
			g.replyMessageFn = func(context.Context, string, string, string) (*larkim.ReplyMessageResp, error) {
				t.Fatal("used legacy reply")
				return nil, nil
			}
			g.replyTopicMessageFn = func(context.Context, string, string, string) (*larkim.ReplyMessageResp, error) {
				return nil, errors.New("topic unavailable")
			}
			g.uploadImagePathFn = func(context.Context, string) (string, error) { return "img-key", nil }
			op := Operation{Kind: kind, SurfaceSessionID: feishuidentity.TopicRef("app-1", "oc_1", "om_root").SurfaceID(), ChatID: "oc_1", Text: "hello", ImagePath: "unused"}
			if err := g.Apply(t.Context(), []Operation{op}); err == nil {
				t.Fatal("expected topic delivery failure")
			}
		})
	}
}

func TestTopicTransportCapturesAndValidatesNativeReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, root, chat, native string
		fail                     bool
	}{
		{"valid", "om_root", "oc_1", "omt_1", false},
		{"root mismatch", "om_elsewhere", "oc_1", "omt_1", true},
		{"chat mismatch", "om_root", "oc_other", "omt_1", true},
		{"missing native", "om_root", "oc_1", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewLiveGateway(LiveGatewayConfig{GatewayID: "app-1"})
			g.replyTopicMessageFn = func(_ context.Context, root, msgType, content string) (*larkim.ReplyMessageResp, error) {
				if root != "om_root" || msgType != "text" {
					t.Fatalf("unexpected target/type %s %s", root, msgType)
				}
				return topicTestReply(tc.root, tc.chat, tc.native), nil
			}
			ops := []Operation{{Kind: OperationSendText, SurfaceSessionID: feishuidentity.TopicRef("app-1", "oc_1", "om_root").SurfaceID(), ChatID: "oc_1", Text: "hello"}}
			err := g.Apply(t.Context(), ops)
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v fail=%v", err, tc.fail)
			}
			if !tc.fail && (ops[0].FeishuTopicID != "omt_1" || ops[0].FeishuTopicRootMessageID != "om_root" || ops[0].FeishuTopicMessageURL == "" || !ops[0].ReplyInThread) {
				t.Fatalf("missing receipt %#v", ops[0])
			}
		})
	}
}

func TestTopicCallbacksRequireMessageOwnershipAndActualChat(t *testing.T) {
	surface := feishuidentity.TopicRef("app-1", "oc_1", "om_root").SurfaceID()
	g := NewLiveGateway(LiveGatewayConfig{GatewayID: "app-1"})
	lookup := gatewaypkg.CardActionSurfaceLookup{MessageID: "om_card", PayloadSurfaceSessionID: surface, ChatID: "oc_1", OperatorID: "ou_1"}
	if got := g.surfaceForCardAction(lookup); got != "" {
		t.Fatalf("unknown payload-only card routed: %s", got)
	}
	g.recordSurfaceMessage("om_card", surface)
	if got := g.surfaceForCardAction(lookup); got != surface {
		t.Fatalf("owned callback=%s", got)
	}
	lookup.ChatID = "oc_other"
	if got := g.surfaceForCardAction(lookup); got != "" {
		t.Fatalf("forwarded callback routed: %s", got)
	}
	g = NewLiveGateway(LiveGatewayConfig{GatewayID: "app-1", LookupTopicMessageSurface: func(messageID, chatID string) string {
		if messageID == "om_card" && chatID == "oc_1" {
			return surface
		}
		return ""
	}})
	lookup.ChatID = "oc_1"
	if got := g.surfaceForCardAction(lookup); got != surface {
		t.Fatalf("persisted callback=%s", got)
	}
}

func TestDirectTopicMediaUsesReplyTransport(t *testing.T) {
	g := NewLiveGateway(LiveGatewayConfig{GatewayID: "app-1"})
	surface := feishuidentity.TopicRef("app-1", "oc_1", "om_root").SurfaceID()
	g.createMessageFn = func(context.Context, string, string, string, string) (*larkim.CreateMessageResp, error) {
		t.Fatal("topic media leaked to group")
		return nil, nil
	}
	g.uploadFilePathFn = func(context.Context, string) (string, string, error) { return "file-key", "file.txt", nil }
	g.uploadImagePathFn = func(context.Context, string) (string, error) { return "image-key", nil }
	g.uploadVideoPathFn = func(context.Context, string) (string, string, error) { return "video-key", "video.mp4", nil }
	var sent []string
	g.replyTopicMessageFn = func(_ context.Context, root, msgType, content string) (*larkim.ReplyMessageResp, error) {
		if root != "om_root" {
			t.Fatalf("root=%s", root)
		}
		sent = append(sent, msgType)
		return topicTestReply(root, "oc_1", "omt_1"), nil
	}
	if _, err := g.SendIMFile(t.Context(), IMFileSendRequest{SurfaceSessionID: surface, ChatID: "oc_1", Path: "file.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.SendIMImage(t.Context(), IMImageSendRequest{SurfaceSessionID: surface, ChatID: "oc_1", Path: "image.png"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.SendIMVideo(t.Context(), IMVideoSendRequest{SurfaceSessionID: surface, ChatID: "oc_1", Path: "video.mp4"}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 3 || sent[0] != "file" || sent[1] != "image" || sent[2] != "media" {
		t.Fatalf("sent=%v", sent)
	}
}

func TestTopicSDKReplyUsesNativeThreadMode(t *testing.T) {
	var got bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"topic-test-token","expire":3600}`))
		case "/open-apis/im/v1/messages/om_root/reply":
			var body struct {
				ReplyInThread bool   `json:"reply_in_thread"`
				MsgType       string `json:"msg_type"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			got = body.ReplyInThread
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_reply","root_id":"om_root","chat_id":"oc_1","thread_id":"omt_1"}}`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	g := NewLiveGateway(LiveGatewayConfig{GatewayID: "topic-sdk", AppID: "topic-test-sdk", AppSecret: "unused", Domain: server.URL})
	op := Operation{Kind: OperationSendText, SurfaceSessionID: feishuidentity.TopicRef("topic-sdk", "oc_1", "om_root").SurfaceID(), ChatID: "oc_1", Text: "hello"}
	if err := g.Apply(t.Context(), []Operation{op}); err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("SDK request omitted reply_in_thread=true")
	}
}

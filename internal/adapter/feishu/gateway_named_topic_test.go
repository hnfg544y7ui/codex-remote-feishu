package feishu

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

func TestNamedTopicRootSDKUsesTitleAndIndependentCreate(t *testing.T) {
	for _, tc := range []struct {
		name, receipt string
		wantErr       bool
	}{
		{"valid", `{"message_id":"om_title","chat_id":"oc_room"}`, false},
		{"wrong-chat", `{"message_id":"om_title","chat_id":"oc_other"}`, true},
		{"wrong-root", `{"message_id":"om_title","chat_id":"oc_room","root_id":"om_other"}`, true},
		{"missing-id", `{"chat_id":"oc_room"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creates := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"named-topic-test","expire":3600}`))
					return
				}
				if r.URL.Path != "/open-apis/im/v1/messages" || r.URL.Query().Get("receive_id_type") != "chat_id" {
					t.Errorf("unexpected root transport: %s", r.URL)
					w.WriteHeader(404)
					return
				}
				creates++
				var body struct {
					ReceiveID string `json:"receive_id"`
					MsgType   string `json:"msg_type"`
					Content   string `json:"content"`
					UUID      string `json:"uuid"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				var post feishuLocalizedPostContent
				if err := json.Unmarshal([]byte(body.Content), &post); err != nil {
					t.Error(err)
				}
				if body.ReceiveID != "oc_room" || body.MsgType != "post" || body.UUID != strings.Repeat("a", 32) || post.ZhCN.Title != "多目相机调试" || strings.Contains(body.Content, "/topic new") {
					t.Errorf("root did not use actual topic title: %#v", body)
				}
				_, _ = w.Write([]byte(`{"code":0,"data":` + tc.receipt + `}`))
			}))
			defer server.Close()
			g := NewLiveGateway(LiveGatewayConfig{GatewayID: "app-topic", AppID: "named-topic-" + tc.name, AppSecret: "unused", Domain: server.URL})
			ops := []Operation{{Kind: OperationCreateTopicRoot, GatewayID: "app-topic", SurfaceSessionID: "feishu:app-topic:chat:oc_room", ChatID: "oc_room", Text: "多目相机调试", IdempotencyKey: strings.Repeat("a", 32)}}
			err := g.Apply(t.Context(), ops)
			if (err != nil) != tc.wantErr || creates != 1 {
				t.Fatalf("creates=%d err=%v", creates, err)
			}
			if !tc.wantErr && (ops[0].MessageID != "om_title" || g.lookupSurfaceMessage("om_title") != feishuidentity.TopicRef("app-topic", "oc_room", "om_title").SurfaceID()) {
				t.Fatal("root ownership was not recorded as topic")
			}
			if tc.wantErr && (ops[0].MessageID != "" || g.lookupSurfaceMessage("om_title") != "") {
				t.Fatal("unverified receipt established ownership")
			}
			ops[0].ReplyToMessageID = "om_command"
			if err := g.Apply(t.Context(), ops); err == nil || creates != 1 {
				t.Fatal("invalid root send reached transport")
			}
		})
	}
}

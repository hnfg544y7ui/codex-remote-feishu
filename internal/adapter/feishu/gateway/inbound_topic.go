package gateway

import (
	"fmt"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
	"github.com/kxn/codex-remote-feishu/internal/xutil"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"strings"
)

func resolveInboundTopicSurface(env InboundEnv, message *larkim.EventMessage, senderUserID string) (string, string, string, error) {
	gatewayID := strings.TrimSpace(env.GatewayID)
	chatID := strings.TrimSpace(xutil.StringValue(message.ChatId))
	surfaceID := SurfaceIDForInbound(gatewayID, chatID, xutil.StringValue(message.ChatType), senderUserID)
	nativeTopicID := strings.TrimSpace(xutil.StringValue(message.ThreadId))
	rootID := strings.TrimSpace(xutil.StringValue(message.RootId))
	if strings.EqualFold(strings.TrimSpace(xutil.StringValue(message.ChatType)), "p2p") {
		return surfaceID, "", rootID, nil
	}
	mapped := ""
	if env.ResolveTopicSurface != nil {
		mapped = strings.TrimSpace(env.ResolveTopicSurface(chatID, nativeTopicID, rootID))
	}
	if mapped != "" {
		ref, ok := feishuidentity.ParseSurfaceRef(mapped)
		if !ok || !ref.IsTopic() || ref.GatewayID != gatewayID || ref.TopicChatID() != chatID || (rootID != "" && rootID != ref.TopicRootMessageID()) {
			return "", "", "", fmt.Errorf("invalid Feishu topic routing binding")
		}
		return ref.SurfaceID(), nativeTopicID, ref.TopicRootMessageID(), nil
	}
	if nativeTopicID == "" {
		// Ordinary quoted group replies also carry root_id. Keep the legacy route
		// unless a persisted binding recognizes the root or native thread_id exists.
		return surfaceID, "", rootID, nil
	}
	if rootID == "" {
		return "", "", "", fmt.Errorf("Feishu topic root unavailable; select a topic session before sending")
	}
	surfaceID = feishuidentity.TopicRef(gatewayID, chatID, rootID).SurfaceID()
	if surfaceID == "" {
		return "", "", "", fmt.Errorf("invalid Feishu topic root identity")
	}
	return surfaceID, nativeTopicID, rootID, nil
}

package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
	"github.com/kxn/codex-remote-feishu/internal/xutil"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// replyTopicMessage never substitutes a chat-level create when the reply fails.
func (g *LiveGateway) replyTopicMessage(ctx context.Context, messageID, msgType, content string) (*larkim.ReplyMessageResp, error) {
	return DoSDK(ctx, g.broker, CallSpec{
		GatewayID: g.config.GatewayID, API: "im.v1.message.reply", Class: CallClassIMSend,
		Priority: CallPriorityInteractive, ResourceKey: FeishuResourceKey{MessageID: messageID},
		Retry: RetryRateLimitOnly, Permission: PermissionCooldownOnly,
	}, func(callCtx context.Context, client *lark.Client) (*larkim.ReplyMessageResp, error) {
		resp, err := client.Im.V1.Message.Reply(callCtx, larkim.NewReplyMessageReqBuilder().MessageId(messageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().MsgType(msgType).Content(content).ReplyInThread(true).Build()).Build())
		if err != nil {
			return resp, err
		}
		if resp == nil {
			return nil, fmt.Errorf("topic reply returned no response")
		}
		if !resp.Success() {
			return resp, newAPIError("im.v1.message.reply", resp.ApiResp, resp.CodeError)
		}
		return resp, nil
	})
}

func (g *LiveGateway) topicTransportRef(surfaceID, chatID string) (feishuidentity.SurfaceRef, bool, error) {
	ref, ok := feishuidentity.ParseSurfaceRef(surfaceID)
	if !ok || !ref.IsTopic() {
		parts := strings.Split(strings.TrimSpace(surfaceID), ":")
		if len(parts) >= 3 && parts[0] == feishuidentity.PlatformFeishu && parts[2] == feishuidentity.ScopeKindTopic {
			return ref, true, fmt.Errorf("invalid topic surface identity")
		}
		return ref, false, nil
	}
	if ref.GatewayID != g.config.GatewayID || (strings.TrimSpace(chatID) != "" && ref.TopicChatID() != strings.TrimSpace(chatID)) {
		return ref, true, fmt.Errorf("topic delivery target does not match gateway/chat")
	}
	return ref, true, nil
}

func topicReplyReceipt(ref feishuidentity.SurfaceRef, resp *larkim.ReplyMessageResp) error {
	if resp == nil || !resp.Success() || resp.Data == nil {
		return fmt.Errorf("topic reply did not return a successful message receipt")
	}
	if strings.TrimSpace(xutil.StringValue(resp.Data.MessageId)) == "" || strings.TrimSpace(xutil.StringValue(resp.Data.ThreadId)) == "" {
		return fmt.Errorf("topic reply receipt missing message_id or native thread_id")
	}
	if root := strings.TrimSpace(xutil.StringValue(resp.Data.RootId)); root != "" && root != ref.TopicRootMessageID() {
		return fmt.Errorf("topic reply receipt root does not match captured root")
	}
	if chat := strings.TrimSpace(xutil.StringValue(resp.Data.ChatId)); chat != "" && chat != ref.TopicChatID() {
		return fmt.Errorf("topic reply receipt chat does not match captured chat")
	}
	return nil
}

func (g *LiveGateway) applyTopicOperation(ctx context.Context, op *Operation) (bool, error) {
	ref, topic, err := g.topicTransportRef(op.SurfaceSessionID, op.ChatID)
	if err != nil {
		return true, err
	}
	if !topic {
		if op.ReplyInThread {
			return true, fmt.Errorf("topic reply requires a validated topic surface")
		}
		return false, nil
	}
	if expectedRoot := strings.TrimSpace(op.FeishuTopicRootMessageID); expectedRoot != "" && expectedRoot != ref.TopicRootMessageID() {
		return true, fmt.Errorf("topic delivery root does not match surface")
	}
	switch op.Kind {
	case OperationSendText, OperationSendCard, OperationSendImage:
	default:
		// Patches and reactions already address a message in place. Reject cross-topic
		// message IDs rather than trusting a callback-provided update target.
		if op.MessageID != "" {
			owner := g.lookupSurfaceMessage(op.MessageID)
			if owner == "" && g.config.LookupTopicMessageSurface != nil {
				owner = g.config.LookupTopicMessageSurface(op.MessageID, ref.TopicChatID())
			}
			if owner != ref.SurfaceID() {
				return true, fmt.Errorf("topic operation message ownership could not be verified")
			}
		}
		return false, nil
	}
	if root := strings.TrimSpace(op.ReplyToMessageID); root != "" && root != ref.TopicRootMessageID() {
		// Always anchor output at the immutable root, not a potentially stale source.
		if g.lookupSurfaceMessage(root) != ref.SurfaceID() && root != ref.TopicRootMessageID() {
			return true, fmt.Errorf("topic reply source does not belong to captured topic")
		}
	}
	var msgType, content string
	switch op.Kind {
	case OperationSendText:
		msgType, content, err = sendTextPayload(*op)
	case OperationSendCard:
		var data []byte
		data, err = json.Marshal(trimCardPayloadForMessageTransport(renderOperationCard(*op, op.effectiveCardEnvelope())))
		msgType, content = "interactive", string(data)
	case OperationSendImage:
		var key string
		key, err = g.uploadOperationImage(ctx, *op)
		if err == nil {
			data, _ := json.Marshal(map[string]string{"image_key": key})
			msgType, content = "image", string(data)
		}
	}
	if err != nil {
		return true, err
	}
	op.ReplyToMessageID = ref.TopicRootMessageID()
	op.ReplyInThread = true
	if g.replyTopicMessageFn == nil {
		return true, fmt.Errorf("topic reply transport unavailable")
	}
	resp, err := g.replyTopicMessageFn(ctx, op.ReplyToMessageID, msgType, content)
	if err != nil {
		return true, err
	}
	if err = topicReplyReceipt(ref, resp); err != nil {
		return true, err
	}
	nativeTopicID := strings.TrimSpace(xutil.StringValue(resp.Data.ThreadId))
	if op.FeishuTopicID != "" && op.FeishuTopicID != nativeTopicID {
		return true, fmt.Errorf("topic reply receipt native topic changed")
	}
	op.MessageID = strings.TrimSpace(xutil.StringValue(resp.Data.MessageId))
	op.FeishuTopicID = nativeTopicID
	op.FeishuTopicRootMessageID = ref.TopicRootMessageID()
	op.FeishuTopicMessageURL = strings.TrimSpace(xutil.StringValue(resp.Data.MessageAppLink))
	g.recordSurfaceMessage(op.MessageID, ref.SurfaceID())
	return true, nil
}

// The direct file/image/video send APIs share the same fail-closed topic transport.
func (g *LiveGateway) sendSurfaceMedia(ctx context.Context, surfaceID, chatID, receiveIDType, receiveID, msgType, content string) (*larkim.CreateMessageResp, error) {
	ref, topic, err := g.topicTransportRef(surfaceID, chatID)
	if err != nil {
		return nil, err
	}
	if !topic {
		return g.createMessageFn(ctx, receiveIDType, receiveID, msgType, content)
	}
	if g.replyTopicMessageFn == nil {
		return nil, fmt.Errorf("topic reply transport unavailable")
	}
	reply, err := g.replyTopicMessageFn(ctx, ref.TopicRootMessageID(), msgType, content)
	if err != nil {
		return nil, err
	}
	if err = topicReplyReceipt(ref, reply); err != nil {
		return nil, err
	}
	data := reply.Data
	g.recordSurfaceMessage(strings.TrimSpace(xutil.StringValue(data.MessageId)), surfaceID)
	return &larkim.CreateMessageResp{ApiResp: reply.ApiResp, CodeError: reply.CodeError,
		Data: &larkim.CreateMessageRespData{MessageId: data.MessageId, RootId: data.RootId, ThreadId: data.ThreadId,
			ChatId: data.ChatId}}, nil
}

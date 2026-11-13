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

// The title is a bot-owned root, never a reply to the user's command. Only its
// confirmed receipt may become a topic route; ordinary sends cannot use this path.
func (g *LiveGateway) createNamedTopicRoot(ctx context.Context, op *Operation) error {
	ref, ok := feishuidentity.ParseSurfaceRef(op.SurfaceSessionID)
	if !ok || !ref.IsChat() || ref.GatewayID != g.config.GatewayID || ref.ScopeID != op.ChatID ||
		op.ReplyToMessageID != "" || op.ReplyInThread || op.FeishuTopicID != "" || op.FeishuTopicRootMessageID != "" ||
		strings.TrimSpace(op.Text) == "" || len(op.IdempotencyKey) != 32 {
		return fmt.Errorf("invalid named topic root target")
	}
	content, err := json.Marshal(feishuLocalizedPostContent{ZhCN: feishuPostContent{
		Title:   op.Text,
		Content: [][]feishuPostNode{{{Tag: "text", Text: "在本话题中发送 /use 选择项目和会话。"}}},
	}})
	if err != nil {
		return err
	}
	resp, err := DoSDK(ctx, g.broker, CallSpec{
		GatewayID: g.config.GatewayID, API: "im.v1.message.create", Class: CallClassIMSend,
		Priority: CallPriorityInteractive, ResourceKey: FeishuResourceKey{ReceiveTarget: joinReceiveTarget("chat_id", op.ChatID)},
		Retry: RetryRateLimitOnly, Permission: PermissionCooldownOnly,
	}, func(callCtx context.Context, client *lark.Client) (*larkim.CreateMessageResp, error) {
		resp, err := client.Im.V1.Message.Create(callCtx, larkim.NewCreateMessageReqBuilder().ReceiveIdType("chat_id").
			Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(op.ChatID).MsgType("post").Content(string(content)).Uuid(op.IdempotencyKey).Build()).Build())
		if err != nil {
			return resp, err
		}
		if resp == nil {
			return nil, fmt.Errorf("named topic root returned no receipt")
		}
		if !resp.Success() {
			return resp, newAPIError("im.v1.message.create", resp.ApiResp, resp.CodeError)
		}
		return resp, nil
	})
	if err != nil {
		return err
	}
	if resp.Data == nil || strings.TrimSpace(xutil.StringValue(resp.Data.MessageId)) == "" ||
		xutil.StringValue(resp.Data.ChatId) != op.ChatID {
		return fmt.Errorf("named topic root receipt missing message or mismatched chat")
	}
	root := strings.TrimSpace(xutil.StringValue(resp.Data.MessageId))
	if parent := strings.TrimSpace(xutil.StringValue(resp.Data.RootId)); parent != "" && parent != root {
		return fmt.Errorf("named topic root receipt belongs to another root")
	}
	op.MessageID = root
	// Register ownership as a topic immediately, not as the command's room.
	g.recordSurfaceMessage(root, feishuidentity.TopicRef(ref.GatewayID, op.ChatID, root).SurfaceID())
	return nil
}

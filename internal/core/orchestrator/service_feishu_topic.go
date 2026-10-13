package orchestrator

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

func surfaceIsFeishuTopic(surface *state.SurfaceConsoleRecord) bool {
	if surface == nil {
		return false
	}
	ref, ok := feishuidentity.ParseSurfaceRef(surface.SurfaceSessionID)
	return ok && ref.IsTopic()
}

// ValidateFeishuTopicAction exposes the same read-only gate used by ingress
// before daemon on-demand recovery can start a runtime for this topic.
func (s *Service) ValidateFeishuTopicAction(action control.Action) []eventcontract.Event {
	surface := s.root.Surfaces[strings.TrimSpace(action.SurfaceSessionID)]
	if blocked := s.blockTopicAction(surface, action); len(blocked) != 0 {
		return blocked
	}
	return s.rejectExpiredCommandEntry(surface, action)
}

func (s *Service) blockTopicAction(surface *state.SurfaceConsoleRecord, action control.Action) []eventcontract.Event {
	if !surfaceIsFeishuTopic(surface) {
		return nil
	}
	ref, _ := feishuidentity.ParseSurfaceRef(surface.SurfaceSessionID)
	if action.ChatID != "" && action.ChatID != ref.TopicChatID() || action.GatewayID != "" && action.GatewayID != ref.GatewayID ||
		action.FeishuTopicRootMessageID != "" && action.FeishuTopicRootMessageID != ref.TopicRootMessageID() ||
		action.FeishuTopicID != "" && surface.FeishuTopicID != "" && action.FeishuTopicID != surface.FeishuTopicID {
		return notice(surface, "topic_route_mismatch", "话题路由信息不匹配，未执行操作。")
	}
	if surface.FeishuTopicOwnerID == "" {
		return notice(surface, "topic_unknown", "这个话题尚未绑定。请在群主界面发送 `/topic new 名称` 创建入口。")
	}
	if surface.FeishuTopicClosed {
		return notice(surface, "topic_closed", "这个话题已关闭。请在群主界面使用 `/topic new 名称` 创建新话题。")
	}
	if action.ActorUserID == "" || action.ActorUserID != surface.FeishuTopicOwnerID {
		return notice(surface, "topic_owner_required", "这个话题由其他成员创建，只有创建者可以向其发送任务或操作会话。")
	}
	if surface.FeishuTopicID == "" {
		return notice(surface, "topic_starting", "话题入口仍在创建，请等待欢迎卡片发送完成后再操作。")
	}
	switch action.Kind {
	case control.ActionReviewCommand, control.ActionReviewStart, control.ActionReviewStartUncommitted, control.ActionReviewOpenCommitPicker, control.ActionReviewFollowUp:
		return notice(surface, "topic_review_unsupported", "本话题固定一个会话，暂不支持需要独立辅助会话的审阅。请通过机器人私聊执行审阅。")
	case control.ActionPlanProposalDecision:
		if action.OptionID == planProposalActionExecuteNew {
			return topicPinnedNotice(surface)
		}
	}
	if surface.FeishuTopicBoundThreadID != "" {
		switch action.Kind {
		case control.ActionNewThread, control.ActionDetach, control.ActionWorkspaceDetach, control.ActionFollowLocal, control.ActionModeCommand, control.ActionAttachWorkspace, control.ActionAttachInstance:
			return topicPinnedNotice(surface)
		}
	}
	return nil
}

func topicPinnedNotice(surface *state.SurfaceConsoleRecord) []eventcontract.Event {
	return notice(surface, "topic_conversation_pinned", "本话题已固定绑定一个会话。要使用其他会话，请在群主界面发送 `/topic new 名称`。")
}

func (s *Service) handleTopicCommand(surface *state.SurfaceConsoleRecord, action control.Action) []eventcontract.Event {
	ref, ok := feishuidentity.ParseSurfaceRef(surface.SurfaceSessionID)
	if !ok || !ref.IsGroup() {
		return notice(surface, "topic_group_required", "`/topic` 只能在飞书群中使用。")
	}
	argument := strings.TrimSpace(action.Value)
	if argument == "" {
		fields := strings.Fields(action.Text)
		if len(fields) > 1 {
			argument = strings.Join(fields[1:], " ")
		}
	}
	command, label, _ := strings.Cut(argument, " ")
	switch strings.ToLower(command) {
	case "new":
		if !ref.IsChat() || action.IsCardAction() || strings.TrimSpace(action.MessageID) == "" || strings.TrimSpace(action.ActorUserID) == "" {
			return notice(surface, "topic_new_root_required", "请在群主界面发送一条新的文字消息：`@机器人 /topic new 名称`。")
		}
		topicRef := feishuidentity.TopicRef(surface.GatewayID, surface.ChatID, action.MessageID)
		if topicRef.SurfaceID() == "" {
			return notice(surface, "topic_invalid_root", "无法验证话题根消息。")
		}
		topic := s.root.Surfaces[topicRef.SurfaceID()]
		if topic == nil {
			topic = s.ensureSurface(control.Action{SurfaceSessionID: topicRef.SurfaceID(), GatewayID: surface.GatewayID, ChatID: surface.ChatID, ActorUserID: action.ActorUserID})
			topic.ProductMode = state.ProductModeNormal
			topic.Backend = agentproto.BackendCodex
			topic.FeishuTopicRootMessageID = action.MessageID
			topic.FeishuTopicOwnerID = action.ActorUserID
			topic.FeishuTopicTitle = strings.TrimSpace(label)
			if topic.FeishuTopicTitle == "" {
				topic.FeishuTopicTitle = "新会话"
			}
			topic.FeishuTopicMessageIDs = []string{action.MessageID}
		}
		if topic.FeishuTopicClosed {
			return notice(surface, "topic_closed", "这条消息创建的话题已关闭，请发送新的 `/topic new 名称`。")
		}
		return notice(topic, "topic_created", fmt.Sprintf("话题：%s\n请在本话题中发送 `/use` 选择项目和已有会话，或从选择器新建会话。首次选定后固定绑定。\n并行运行可在群主界面设置 `/primary on`、`/coworkers 2`。", topic.FeishuTopicTitle))
	case "close":
		if !ref.IsTopic() {
			return notice(surface, "topic_close_target", "请在要关闭的话题中发送 `/topic close`。")
		}
		if s.surfaceHasLiveRemoteWork(surface) || surface.PendingHeadless != nil || activePendingRequest(surface) != nil || surface.ActiveRequestCapture != nil {
			return notice(surface, "topic_busy", "话题仍有运行、排队或待确认任务，请完成后再关闭。")
		}
		events := s.detach(surface)
		surface.FeishuTopicClosed = true
		return append(events, notice(surface, "topic_closed", "已关闭本话题，绑定记录保留。新任务请创建新话题。")...)
	case "", "list":
		chatID := surface.ChatID
		topics := []*state.SurfaceConsoleRecord{}
		for _, topic := range s.root.Surfaces {
			if surfaceIsFeishuTopic(topic) && topic.GatewayID == surface.GatewayID && topic.ChatID == chatID && topic.FeishuTopicOwnerID != "" {
				topics = append(topics, topic)
			}
		}
		sort.Slice(topics, func(i, j int) bool { return topics[i].SurfaceSessionID < topics[j].SurfaceSessionID })
		lines := []string{"在群主界面发送 `/topic new 名称` 创建话题，在话题内 `/use` 选择会话。每个话题固定一个会话；`/topic close` 关闭空闲话题。"}
		for _, topic := range topics {
			status := "待绑定"
			if topic.FeishuTopicBoundThreadID != "" {
				status = "已绑定"
			}
			if topic.FeishuTopicClosed {
				status = "已关闭"
			}
			line := fmt.Sprintf("%s · %s · %s", topic.FeishuTopicTitle, status, topic.FeishuTopicBoundThreadID)
			if topic.FeishuTopicMessageURL != "" {
				line += "\n" + topic.FeishuTopicMessageURL
			}
			lines = append(lines, line)
		}
		return notice(surface, "topic_list", strings.Join(lines, "\n\n"))
	default:
		return notice(surface, "topic_usage", "用法：`/topic list`、`/topic new 名称`、在话题内 `/topic close`。")
	}
}

// RejectTopicUnsentDispatch drops only a dispatch that has never reached the
// wrapper. A persistence/delivery failure must not interrupt an active turn.
func (s *Service) RejectTopicUnsentDispatch(surfaceID string) {
	surface := s.root.Surfaces[surfaceID]
	if !surfaceIsFeishuTopic(surface) {
		return
	}
	if item := activeQueueItem(surface); item != nil && item.Status == state.QueueItemDispatching {
		item.Status = state.QueueItemFailed
		item.TerminalCause = "dispatch_failed"
		item.ResultError = "topic route could not be persisted"
		s.clearSurfaceActiveQueueItem(surface, item.ID)
		if inst := s.root.Instances[surface.AttachedInstanceID]; inst != nil && inst.ActiveTurnID == "" {
			s.clearPendingRemoteTurn(inst.InstanceID)
		}
	}
}

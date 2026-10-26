package orchestrator

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

// TopicThreadRebindCommitter saves an explicitly authorized topic target before
// the live route changes. Failure must leave the previous persisted entry intact.
type TopicThreadRebindCommitter interface {
	CommitTopicThreadRebind(expectedThreadID string, next *state.SurfaceConsoleRecord) error
}

type topicThreadSwitch struct {
	fromThreadID string
	threadID     string
}

func (s *Service) SetTopicThreadRebindCommitter(committer TopicThreadRebindCommitter) {
	s.topicThreadRebindCommitter = committer
}

func (s *Service) topicThreadSwitch(surface *state.SurfaceConsoleRecord) *topicThreadSwitch {
	if surface == nil {
		return nil
	}
	intent := s.topicThreadSwitches[surface.SurfaceSessionID]
	if intent != nil && intent.fromThreadID == surface.FeishuTopicBoundThreadID {
		return intent
	}
	return nil
}

func (s *Service) topicThreadSwitchBusy(surface *state.SurfaceConsoleRecord, completing bool) bool {
	if surface == nil || surface.FeishuTopicClosed || surface.Abandoning ||
		s.surfaceHasLiveRemoteWork(surface) || surface.ActiveQueueItemID != "" ||
		len(surface.PendingRequests) != 0 || surface.ActiveRequestCapture != nil ||
		len(surface.StagedImages) != 0 || len(surface.StagedFiles) != 0 ||
		surface.PendingTextInput != nil || s.surfaceHasRouteMutationBlocker(surface) ||
		surface.AutoWhip.PendingReason != "" {
		return true
	}
	if pending := surface.PendingHeadless; pending != nil {
		intent := s.topicThreadSwitch(surface)
		if !completing || intent == nil || pending.ThreadID != intent.threadID ||
			pending.Purpose != state.HeadlessLaunchPurposeThreadRestore {
			return true
		}
	}
	if !completing && s.topicThreadSwitch(surface) != nil {
		return true
	}
	for _, inst := range s.root.Instances {
		if inst.Online && ((inst.ActiveTurnID != "" && inst.ActiveThreadID == surface.FeishuTopicBoundThreadID) ||
			threadRuntimeActive(inst.Threads[surface.FeishuTopicBoundThreadID])) {
			return true
		}
	}
	return false
}

func topicThreadSwitchBusyNotice(surface *state.SurfaceConsoleRecord) []eventcontract.Event {
	return notice(surface, "topic_switch_busy", "话题尚未空闲，暂时不能切换会话。请先完成运行、排队和待确认操作，并处理暂存输入后再用 /use 切换。")
}

// Explicit /use is the only authority to change an established topic target.
// The intent is deliberately transient: a restart retains the last committed
// binding and cannot turn an unfinished launch into an implicit rebind.
func (s *Service) beginTopicThreadSwitch(surface *state.SurfaceConsoleRecord, threadID string) []eventcontract.Event {
	if s.topicThreadSwitchBusy(surface, false) {
		return topicThreadSwitchBusyNotice(surface)
	}
	if s.topicThreadSwitches == nil {
		s.topicThreadSwitches = make(map[string]*topicThreadSwitch)
	}
	s.topicThreadSwitches[surface.SurfaceSessionID] = &topicThreadSwitch{
		fromThreadID: surface.FeishuTopicBoundThreadID, threadID: threadID,
	}
	return nil
}

func (s *Service) commitTopicThreadSwitch(surface *state.SurfaceConsoleRecord, inst *state.InstanceRecord, next surfaceRouteCoreState) bool {
	intent := s.topicThreadSwitch(surface)
	if intent == nil || next.SelectedThreadID != intent.threadID || next.RouteMode != state.RouteModePinned ||
		s.topicThreadSwitchBusy(surface, true) || inst == nil ||
		state.EffectiveInstanceBackend(inst) != s.surfaceBackend(surface) {
		return false
	}
	if pending := surface.PendingHeadless; pending != nil && pending.InstanceID != inst.InstanceID {
		return false
	}
	if s.topicThreadRebindCommitter != nil {
		candidate := *surface
		candidate.AttachedInstanceID = next.AttachedInstanceID
		candidate.ClaimedWorkspaceKey = next.WorkspaceKey
		candidate.RouteMode = next.RouteMode
		candidate.SelectedThreadID = next.SelectedThreadID
		candidate.PreparedThreadCWD = next.PreparedThreadCWD
		candidate.PreparedFromThreadID = next.PreparedFromThreadID
		candidate.FeishuTopicBoundThreadID = next.SelectedThreadID
		candidate.PendingHeadless = nil
		if err := s.topicThreadRebindCommitter.CommitTopicThreadRebind(intent.fromThreadID, &candidate); err != nil {
			return false
		}
	}
	return true
}

func (s *Service) finishTopicThreadSwitch(surface *state.SurfaceConsoleRecord, intent *topicThreadSwitch) {
	if surface == nil || intent == nil {
		return
	}
	if pending := surface.PendingHeadless; pending != nil && pending.ThreadID == intent.threadID {
		if inst := s.root.Instances[pending.InstanceID]; inst != nil {
			s.applyPendingHeadlessRuntimeToInstance(surface, inst, pending)
		}
		s.consumeSurfacePendingHeadlessLaunch(surface, pending.InstanceID)
	}
	delete(s.topicThreadSwitches, surface.SurfaceSessionID)
}

func topicThreadSwitchFailureNotice(surface *state.SurfaceConsoleRecord) []eventcontract.Event {
	return notice(surface, "topic_switch_failed", "会话切换未完成，已保留原会话绑定。请确认目标可用、话题空闲后重新发送 /use；保存失败时请检查本机状态目录。")
}

func (s *Service) abortTopicThreadSwitchOnDetach(surface *state.SurfaceConsoleRecord) []eventcontract.Event {
	intent := s.topicThreadSwitch(surface)
	pending := surface.PendingHeadless
	if intent == nil || pending == nil || pending.ThreadID != intent.threadID {
		return nil
	}
	events := s.maybeFinalizePendingTargetPicker(surface, nil, "原会话连接已断开，本次切换已取消；保留原绑定，请恢复连接后重新发送 /use。")
	return append(events, eventcontract.Event{
		Kind: eventcontract.KindDaemonCommand, SurfaceSessionID: surface.SurfaceSessionID,
		DaemonCommand: &control.DaemonCommand{Kind: control.DaemonCommandKillHeadless,
			SurfaceSessionID: surface.SurfaceSessionID, InstanceID: pending.InstanceID},
	})
}

func (s *Service) topicSwitchTargetBackendMatches(surface *state.SurfaceConsoleRecord, view *mergedThreadView) bool {
	backend := agentproto.NormalizeBackend(view.Backend)
	return backend == "" || backend == s.surfaceBackend(surface)
}

func topicSwitchChangesThread(surface *state.SurfaceConsoleRecord, threadID string) bool {
	return surfaceIsFeishuTopic(surface) && strings.TrimSpace(surface.FeishuTopicBoundThreadID) != "" &&
		strings.TrimSpace(threadID) != surface.FeishuTopicBoundThreadID
}

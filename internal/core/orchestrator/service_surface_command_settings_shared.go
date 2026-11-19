package orchestrator

import (
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type surfaceSettingFeedback struct {
	NoticeCode     string
	NoticeText     string
	CardStatusText string
}

func isPromptSettingAction(kind control.ActionKind) bool {
	switch kind {
	case control.ActionModelCommand, control.ActionReasoningCommand, control.ActionAccessCommand:
		return true
	default:
		return false
	}
}

func (s *Service) promptSettingRequiresAttachment(surface *state.SurfaceConsoleRecord, kind control.ActionKind) bool {
	return isPromptSettingAction(kind) && !s.promptSettingCanRunDetached(surface, kind)
}

func (s *Service) surfaceSettingFeedbackEvents(surface *state.SurfaceConsoleRecord, action control.Action, feedback surfaceSettingFeedback) []eventcontract.Event {
	if commandCardOwnsInlineResult(action) {
		return s.inlineCommandCardEvents(surface, action, control.FeishuCatalogConfigView{
			Sealed:     true,
			StatusKind: "success",
			StatusText: feedback.CardStatusText,
		})
	}
	return notice(surface, feedback.NoticeCode, feedback.NoticeText)
}

func (s *Service) applyPromptOverrideChange(surface *state.SurfaceConsoleRecord, action control.Action, inst *state.InstanceRecord, mutate func(*state.ModelConfigRecord), build func(control.PromptRouteSummary) surfaceSettingFeedback) []eventcontract.Event {
	if (action.Kind == control.ActionModelCommand || action.Kind == control.ActionReasoningCommand) && s.sharedThreadSettingsSelected(surface, inst) {
		if !s.sharedThreadSettingsEnabled(surface, inst) {
			text := "共享会话的连接或控制权尚未就绪，设置未修改。请重新连接并接管目标会话后再试。"
			if commandCardOwnsInlineResult(action) {
				return s.inlineCommandCardEvents(surface, action, control.FeishuCatalogConfigView{Sealed: true, StatusKind: "error", StatusText: text})
			}
			return notice(surface, "thread_settings_unavailable", text)
		}
		return s.requestSharedThreadSettings(surface, action, inst, mutate)
	}
	s.applySurfaceCapabilitySettingsMutation(surface, func(record *state.BotCapabilitySettingsRecord) {
		override := record.PromptOverride
		mutate(&override)
		record.PromptOverride = state.NormalizePromptOverrideForBackend(record.Backend, compactPromptOverride(override))
	}, func(local *state.SurfaceConsoleRecord) {
		override := local.PromptOverride
		mutate(&override)
		backend := agentproto.NormalizeBackend(state.SurfaceDesiredBackendContract(local).Backend)
		local.PromptOverride = state.NormalizePromptOverrideForBackend(backend, compactPromptOverride(override))
	})
	s.persistCurrentClaudeWorkspaceProfileSnapshot(surface)
	summary := s.resolveNextPromptSummary(inst, surface, "", "", state.ModelConfigRecord{})
	return s.surfaceSettingFeedbackEvents(surface, action, build(summary))
}

func (s *Service) promptSettingCanRunDetached(surface *state.SurfaceConsoleRecord, kind control.ActionKind) bool {
	if surface == nil || !state.IsHeadlessProductMode(s.normalizeSurfaceProductMode(surface)) || !s.surfaceCanWriteBotCapabilitySettings(surface) {
		return false
	}
	switch kind {
	case control.ActionModelCommand:
		return s.surfaceBackend(surface) == agentproto.BackendCodex
	case control.ActionReasoningCommand:
		backend := s.surfaceBackend(surface)
		return backend == agentproto.BackendCodex || backend == agentproto.BackendClaude
	case control.ActionAccessCommand:
		return true
	default:
		return false
	}
}

func (s *Service) instanceForPromptSettingCommand(surface *state.SurfaceConsoleRecord, action control.Action) (*state.InstanceRecord, []eventcontract.Event) {
	inst := s.root.Instances[surface.AttachedInstanceID]
	if inst != nil || s.promptSettingCanRunDetached(surface, action.Kind) {
		return inst, nil
	}
	text := s.notAttachedText(surface)
	if commandCardOwnsInlineResult(action) {
		return nil, s.inlineCommandCardEvents(surface, action, control.FeishuCatalogConfigView{
			StatusKind: "error",
			StatusText: text,
		})
	}
	return nil, notice(surface, "not_attached", text)
}

package orchestrator

import (
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

const threadSettingsCommandTimeout = 30 * time.Second

type threadSettingsCommandBinding struct {
	SurfaceID, InstanceID, ThreadID, ProfileID, ConnectionID string
	ConnectionGeneration                                     uint64
	ModelProviderID                                          string
	ExpiresAt                                                time.Time
}

// A selected shared chat remains native-owned while offline or held by another
// surface. Never turn a failed write eligibility check into a global override.
func (s *Service) sharedThreadSettingsSelected(surface *state.SurfaceConsoleRecord, inst *state.InstanceRecord) bool {
	if surface == nil || inst == nil || !inst.Capabilities.SharedThreadSettings ||
		!s.surfaceIsHeadless(surface) || s.surfaceBackend(surface) != agentproto.BackendCodex ||
		state.EffectiveInstanceBackend(inst) != agentproto.BackendCodex || surface.AttachedInstanceID != inst.InstanceID ||
		surface.RouteMode == state.RouteModeNewThreadReady || strings.TrimSpace(surface.SelectedThreadID) == "" {
		return false
	}
	if profile, ok := s.surfaceCodexProfileSummary(surface); ok {
		if _, fixed := fixedCodexAPIProfileModel(normalizeCodexProfileSummary(profile)); fixed {
			return false
		}
	}
	return true
}

func (s *Service) sharedThreadSettingsEnabled(surface *state.SurfaceConsoleRecord, inst *state.InstanceRecord) bool {
	if !s.sharedThreadSettingsSelected(surface, inst) || !inst.Online {
		return false
	}
	thread := inst.Threads[surface.SelectedThreadID]
	if thread == nil || !ordinaryThreadVisible(thread) || !s.surfaceOwnsThread(surface, surface.SelectedThreadID) {
		return false
	}
	owner := s.threadClaimSurface(surface.SelectedThreadID)
	return owner != nil && owner.SurfaceSessionID == surface.SurfaceSessionID && owner.AttachedInstanceID == inst.InstanceID
}

func threadSettingsConnectionID(inst *state.InstanceRecord) string {
	if inst != nil && inst.CodexConnectionContract != nil {
		return inst.CodexConnectionContract.ConnectionContractID
	}
	return ""
}

func (s *Service) ThreadSettingsCommandInstanceID(commandID string) string {
	return s.threadSettingsCommands[commandID].InstanceID
}

func (s *Service) requestSharedThreadSettings(surface *state.SurfaceConsoleRecord, action control.Action, inst *state.InstanceRecord, mutate func(*state.ModelConfigRecord)) []eventcontract.Event {
	parts := strings.Fields(action.Text)
	if action.Kind == control.ActionModelCommand && len(parts) == 2 && isClearCommand(parts[1]) {
		text := "当前共享会话直接使用 Codex 保存的模型设置，没有飞书临时覆盖；可用 /model <模型> 修改该会话。"
		return s.surfaceSettingFeedbackEvents(surface, action, surfaceSettingFeedback{NoticeCode: "thread_settings_native", NoticeText: text, CardStatusText: text})
	}
	if action.Kind == control.ActionReasoningCommand && len(parts) == 2 && isClearCommand(parts[1]) {
		text := "当前共享会话没有飞书临时推理强度覆盖；Codex 原生接口暂不支持清空已保存的推理强度。请用 /reasoning <具体档位> 修改该会话。"
		return s.surfaceSettingFeedbackEvents(surface, action, surfaceSettingFeedback{NoticeCode: "thread_settings_clear_unsupported", NoticeText: text, CardStatusText: text})
	}
	for _, pending := range s.threadSettingsCommands {
		if pending.InstanceID == inst.InstanceID && pending.ThreadID == surface.SelectedThreadID {
			return notice(surface, "thread_settings_pending", "该会话已有设置请求等待 Codex 确认，请稍后重试。")
		}
	}
	current := state.ModelConfigRecord{}
	if observed := inst.Threads[surface.SelectedThreadID].ThreadSettings; observed != nil {
		current.Model, current.ReasoningEffort = observed.Model, observed.ReasoningEffort
	}
	proposed := current
	mutate(&proposed)
	set := &agentproto.ThreadSettingsSetCommand{}
	if action.Kind == control.ActionModelCommand {
		model := strings.TrimSpace(proposed.Model)
		if model == "" {
			return notice(surface, "thread_settings_invalid", "模型不能为空，当前会话设置未修改。")
		}
		set.Model = &model
		if len(parts) == 3 || proposed.ReasoningEffort != current.ReasoningEffort {
			effort := proposed.ReasoningEffort
			if effort == "" {
				entry, found := modelCatalogEntryForModel(inst, model)
				defaultEffort := normalizeModelReasoningEffort(entry.DefaultReasoningEffort)
				if !found || defaultEffort == "" || s.checkModelReasoningSupport(inst, model, defaultEffort).Support != modelReasoningSupported {
					text := "新模型不支持当前推理强度，且没有已验证的默认档位；Codex 原生接口不能清空此设置。请显式发送 /model " + model + " <具体推理强度>。"
					if commandCardOwnsInlineResult(action) {
						return s.inlineCommandCardEvents(surface, action, control.FeishuCatalogConfigView{StatusKind: "error", StatusText: text, FormDefaultValue: actionCommandArgumentText(action)})
					}
					return notice(surface, "thread_settings_effort_required", text)
				}
				effort = defaultEffort
			}
			set.ReasoningEffort = &effort
		}
	} else {
		effort := proposed.ReasoningEffort
		if effort == "" {
			return notice(surface, "thread_settings_clear_unsupported", "Codex 原生接口暂不支持清空推理强度，请选择具体档位。")
		}
		set.ReasoningEffort = &effort
	}
	id := "relay-thread-settings-" + strings.TrimPrefix(s.nextAgentCommandID(), "relay-goal-")
	if s.threadSettingsCommands == nil {
		s.threadSettingsCommands = map[string]threadSettingsCommandBinding{}
	}
	binding := threadSettingsCommandBinding{
		SurfaceID: surface.SurfaceSessionID, InstanceID: inst.InstanceID, ThreadID: surface.SelectedThreadID,
		ProfileID: s.surfaceCodexProfileID(surface), ConnectionID: threadSettingsConnectionID(inst),
		ExpiresAt: s.now().Add(threadSettingsCommandTimeout),
	}
	if connection := inst.CodexConnectionContract; connection != nil {
		binding.ConnectionGeneration, binding.ModelProviderID = connection.ConnectionGeneration, connection.ModelProviderID
	}
	s.threadSettingsCommands[id] = binding
	text := "会话设置请求已发出，最终结果以随后 Codex 返回的确认消息为准。"
	var events []eventcontract.Event
	if commandCardOwnsInlineResult(action) {
		events = s.inlineCommandCardEvents(surface, action, control.FeishuCatalogConfigView{Sealed: true, StatusKind: "info", StatusText: text})
	} else {
		events = notice(surface, "thread_settings_requested", text)
	}
	return append(events, eventcontract.Event{Kind: eventcontract.KindAgentCommand, SurfaceSessionID: surface.SurfaceSessionID,
		Command: &agentproto.Command{CommandID: id, Kind: agentproto.CommandThreadSettingsSet,
			Target: agentproto.Target{ThreadID: surface.SelectedThreadID}, ThreadSettingsSet: set,
			Origin: agentproto.Origin{Surface: surface.SurfaceSessionID, UserID: surface.ActorUserID, ChatID: surface.ChatID, MessageID: action.MessageID}}})
}

func (s *Service) finishThreadSettingsCommand(commandID, code, text string) []eventcontract.Event {
	binding, ok := s.threadSettingsCommands[commandID]
	if !ok {
		return nil
	}
	delete(s.threadSettingsCommands, commandID)
	if surface := s.root.Surfaces[binding.SurfaceID]; surface != nil {
		return notice(surface, code, text)
	}
	return nil
}

func (s *Service) applyThreadSettingsCommandResult(instanceID string, event agentproto.Event) []eventcontract.Event {
	binding, ok := s.threadSettingsCommands[event.CommandID]
	if !ok || binding.InstanceID != instanceID || binding.ThreadID != event.ThreadID {
		return nil
	}
	if !s.now().Before(binding.ExpiresAt) {
		return s.finishThreadSettingsCommand(event.CommandID, "thread_settings_timeout", "等待设置确认超时；结果未确认，请重新打开模型设置核对。未自动重试。")
	}
	if event.ErrorMessage != "" {
		return s.finishThreadSettingsCommand(event.CommandID, "thread_settings_failed", "Codex 未能确认保存会话设置，原有飞书默认配置未修改。请重新打开模型设置核对。")
	}
	surface, inst := s.root.Surfaces[binding.SurfaceID], s.root.Instances[binding.InstanceID]
	connectionChanged := binding.ConnectionGeneration != 0 || binding.ModelProviderID != ""
	if inst != nil && inst.CodexConnectionContract != nil {
		connection := inst.CodexConnectionContract
		connectionChanged = connection.ConnectionGeneration != binding.ConnectionGeneration || connection.ModelProviderID != binding.ModelProviderID
	}
	if surface == nil || inst == nil || !s.sharedThreadSettingsEnabled(surface, inst) ||
		surface.SelectedThreadID != binding.ThreadID || s.surfaceCodexProfileID(surface) != binding.ProfileID ||
		threadSettingsConnectionID(inst) != binding.ConnectionID || connectionChanged {
		return s.finishThreadSettingsCommand(event.CommandID, "thread_settings_context_changed", "设置请求已返回，但当前连接或会话已切换；请在原会话核对保存结果。")
	}
	// Native settings notifications own the projection. Never apply the saved
	// request here: another client may already have committed a newer value.
	return s.finishThreadSettingsCommand(event.CommandID, "thread_settings_saved", "Codex 已确认保存该会话设置，连接端将同步后端最新值；已运行的一轮不切换模型。")
}

func (s *Service) tickThreadSettingsCommands(now time.Time) []eventcontract.Event {
	var events []eventcontract.Event
	for id, binding := range s.threadSettingsCommands {
		inst := s.root.Instances[binding.InstanceID]
		if s.root.Surfaces[binding.SurfaceID] == nil {
			delete(s.threadSettingsCommands, id)
		} else if inst == nil || !inst.Online {
			events = append(events, s.finishThreadSettingsCommand(id, "thread_settings_disconnected", "连接已中断，无法确认会话设置是否保存；请重连后核对。")...)
		} else if !now.Before(binding.ExpiresAt) {
			events = append(events, s.finishThreadSettingsCommand(id, "thread_settings_timeout", "等待设置确认超时；结果未确认，请重新打开模型设置核对。未自动重试。")...)
		}
	}
	return events
}

func (s *Service) failThreadSettingsCommandsForInstance(instanceID string) []eventcontract.Event {
	var events []eventcontract.Event
	for id, binding := range s.threadSettingsCommands {
		if binding.InstanceID == instanceID {
			events = append(events, s.finishThreadSettingsCommand(id, "thread_settings_disconnected", "连接已中断，无法确认会话设置是否保存；请重连后核对。")...)
		}
	}
	return events
}

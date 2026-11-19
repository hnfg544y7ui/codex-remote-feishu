package orchestrator

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func (s *Service) sharedThreadSettingsReadable(surface *state.SurfaceConsoleRecord, inst *state.InstanceRecord, threadID string) bool {
	return s.sharedThreadSettingsSelected(surface, inst) && inst.Online && strings.TrimSpace(threadID) == surface.SelectedThreadID && ordinaryThreadVisible(inst.Threads[threadID])
}

func (s *Service) sharedObservedThreadSettings(surface *state.SurfaceConsoleRecord, inst *state.InstanceRecord, threadID string) *agentproto.ThreadSettingsUpdate {
	if !s.sharedThreadSettingsReadable(surface, inst, threadID) {
		return nil
	}
	thread := inst.Threads[threadID]
	if thread == nil || thread.ThreadSettings == nil || strings.TrimSpace(thread.ThreadSettings.Model) == "" {
		return nil
	}
	return thread.ThreadSettings
}

func sharedThreadProjectionCapable(inst *state.InstanceRecord) bool {
	return inst != nil && inst.Online && agentproto.NormalizeBackend(state.EffectiveInstanceBackend(inst)) == agentproto.BackendCodex && inst.Capabilities.SharedThreadSettings
}

func mergeSharedThreadSettings(previous, update *agentproto.ThreadSettingsUpdate) *agentproto.ThreadSettingsUpdate {
	merged := agentproto.CloneThreadSettingsUpdate(update)
	if previous == nil {
		return merged
	}
	if merged.Model == "" {
		merged.Model = previous.Model
	}
	if !merged.ReasoningEffortSet && merged.ReasoningEffort == "" {
		merged.ReasoningEffort, merged.ReasoningEffortSet = previous.ReasoningEffort, previous.ReasoningEffortSet
	}
	if merged.ModelProviderID == "" {
		merged.ModelProviderID = previous.ModelProviderID
	}
	return merged
}

func sameSharedThreadConnection(source, target *state.InstanceRecord) bool {
	if !sharedThreadProjectionCapable(source) || !sharedThreadProjectionCapable(target) {
		return false
	}
	a, b := source.CodexConnectionContract, target.CodexConnectionContract
	return a != nil && b != nil && a.ConnectionContractID != "" && a.ConnectionContractID == b.ConnectionContractID &&
		a.ModelProviderID == b.ModelProviderID && a.ConnectionGeneration == b.ConnectionGeneration
}

func projectSharedObservedModel(thread *state.ThreadRecord, update *agentproto.ThreadSettingsUpdate) {
	if update.Model != "" {
		thread.ExplicitModel = update.Model
	}
	if update.ReasoningEffortSet || update.ReasoningEffort != "" {
		thread.ExplicitReasoningEffort = update.ReasoningEffort
	}
	if observed := thread.CodexEffectiveThread; observed != nil {
		if update.Model != "" {
			observed.Model = update.Model
			observed.ModelMode = agentproto.CodexThreadValuePreservedObserved
		}
		if update.ReasoningEffortSet || update.ReasoningEffort != "" {
			observed.ReasoningEffort = update.ReasoningEffort
			observed.ReasoningMode = agentproto.CodexThreadValuePreservedObserved
		}
	}
}

// Native broadcasts are scoped by both connection and thread. Never infer a
// shared authority from a coincidentally equal thread ID in another backend.
func (s *Service) projectSharedThreadModelSettings(source *state.InstanceRecord, sourceThread *state.ThreadRecord, update *agentproto.ThreadSettingsUpdate) {
	if !sharedThreadProjectionCapable(source) {
		return
	}
	projectSharedObservedModel(sourceThread, update)
	for _, target := range s.root.Instances {
		if target == source || !sameSharedThreadConnection(source, target) {
			continue
		}
		thread := target.Threads[update.ThreadID]
		if thread == nil {
			continue
		}
		settings := agentproto.CloneThreadSettingsUpdate(thread.ThreadSettings)
		if settings == nil {
			settings = &agentproto.ThreadSettingsUpdate{ThreadID: update.ThreadID}
		}
		if update.Model != "" {
			settings.Model = update.Model
		}
		if update.ReasoningEffortSet || update.ReasoningEffort != "" {
			settings.ReasoningEffort = update.ReasoningEffort
			settings.ReasoningEffortSet = update.ReasoningEffortSet
		}
		thread.ThreadSettings = settings
		projectSharedObservedModel(thread, update)
	}
}

// Only an exact existing-thread dispatch may defer model selection to native
// state. Forks, new conversations, and frozen connection changes keep their own
// initial configuration and routing semantics.
func (s *Service) queueItemUsesSharedThreadSettings(surface *state.SurfaceConsoleRecord, inst *state.InstanceRecord, item *state.QueueItemRecord, plan agentproto.PromptDispatchPlan) bool {
	plan = agentproto.NormalizePromptDispatchPlan(plan)
	if !s.sharedThreadSettingsEnabled(surface, inst) || plan.ExecutionMode != agentproto.PromptExecutionModeResumeExisting || plan.ExecutionThreadID != surface.SelectedThreadID {
		return false
	}
	if frozen := item.CodexConnectionContract; frozen != nil && strings.TrimSpace(frozen.ConnectionContractID) != "" {
		current := inst.CodexConnectionContract
		if current == nil || frozen.ConnectionContractID != current.ConnectionContractID || frozen.ModelProviderID != current.ModelProviderID || frozen.ConnectionGeneration != current.ConnectionGeneration {
			return false
		}
	}
	return true
}

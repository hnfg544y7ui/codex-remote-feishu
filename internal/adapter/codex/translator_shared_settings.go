package codex

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/xutil"
)

func (t *Translator) usesSharedThreadSettings(command agentproto.Command) bool {
	return t.shared != nil && command.Overrides.UseThreadSettings && strings.TrimSpace(command.Target.ThreadID) != ""
}

// Even a resume can mutate a thread that is not currently loaded. Preserve
// permissions and other policy fields, but leave model selection to the thread.
func stripThreadModelOverrides(params map[string]any) {
	delete(params, "model")
	delete(params, "effort")
	delete(params, "reasoningEffort")
	if config, ok := params["config"].(map[string]any); ok {
		delete(config, "model")
		delete(config, "model_reasoning_effort")
		delete(config, "reasoning_effort")
	}
	delete(params, "collaborationMode")
}

func applySharedThreadTurnSettings(params map[string]any, overrides agentproto.PromptOverrides) {
	stripThreadModelOverrides(params)
	if mode := normalizeOverridePlanMode(overrides.PlanMode); mode != "" {
		var effort any
		if overrides.ReasoningEffort != "" {
			effort = overrides.ReasoningEffort
		}
		// Model and effort are refreshed from this command's resume response,
		// never from a cached turn template or remote surface configuration.
		params["collaborationMode"] = map[string]any{"mode": mode, "settings": map[string]any{
			"model": overrides.Model, "reasoning_effort": effort, "developer_instructions": nil,
		}}
	}
}

func refreshSharedPromptSettings(command *agentproto.Command, message map[string]any) (*agentproto.ThreadSettingsUpdate, string) {
	update := sharedResumeThreadSettings(message)
	if lookupString(message, "result", "thread", "id") != command.Target.ThreadID {
		return nil, "thread settings refresh returned a different thread"
	}
	command.Overrides.Model, command.Overrides.ReasoningEffort = "", ""
	if update != nil {
		command.Overrides.Model, command.Overrides.ReasoningEffort = update.Model, update.ReasoningEffort
	}
	if normalizeOverridePlanMode(command.Overrides.PlanMode) != "" && command.Overrides.Model == "" {
		return update, "thread settings refresh did not return the model required for explicit plan mode"
	}
	return update, ""
}

// Read only the successful native resume response. Request arguments and cached
// surface values are never evidence of the thread's current configuration.
func sharedResumeThreadSettings(message map[string]any) *agentproto.ThreadSettingsUpdate {
	result := lookupMap(message, "result")
	threadID := lookupString(result, "thread", "id")
	settings := lookupMap(result, "threadSettings")
	if len(settings) == 0 {
		settings = result
	}
	effort, present := settings["effort"]
	if !present {
		effort, present = settings["reasoningEffort"]
	}
	if xutil.LookupStringFromAny(settings["model"]) == "" && !present {
		return nil
	}
	return agentproto.NormalizeThreadSettingsUpdate(&agentproto.ThreadSettingsUpdate{
		ThreadID: threadID, Model: xutil.LookupStringFromAny(settings["model"]),
		ModelProviderID: xutil.LookupStringFromAny(settings["modelProvider"]),
		ReasoningEffort: xutil.LookupStringFromAny(effort), ReasoningEffortSet: present,
	})
}

func (t *Translator) projectSharedResumeSettings(message map[string]any) []agentproto.Event {
	update := sharedResumeThreadSettings(message)
	if update == nil {
		return nil
	}
	t.mergeObservedThread(update.ThreadID, update.ModelProviderID, update.Model, update.ReasoningEffort)
	if update.ReasoningEffortSet {
		observed := t.observedThreads[update.ThreadID]
		observed.ReasoningEffort = update.ReasoningEffort
		t.observedThreads[update.ThreadID] = observed
	}
	return []agentproto.Event{{Kind: agentproto.EventThreadSettingsUpdated, ThreadID: update.ThreadID, ThreadSettings: update}}
}

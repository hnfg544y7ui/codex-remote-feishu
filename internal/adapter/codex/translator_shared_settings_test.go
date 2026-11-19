package codex

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestSharedThreadSettingsPromptRefreshesWithoutRestoringStaleOverrides(t *testing.T) {
	for _, plan := range []string{"", "on", "off"} {
		t.Run("plan="+plan, func(t *testing.T) {
			tr := NewTranslator("instance")
			tr.SetSharedBackend(true)
			sharedResume(t, tr, "target")
			tr.observedThreads["target"] = codexObservedThread{Model: "stale-observed", ReasoningEffort: "xhigh"}
			tr.latestTurnStartTemplate = map[string]any{"model": "cached-model", "effort": "high", "collaborationMode": map[string]any{"mode": "plan", "settings": map[string]any{"model": "cached-plan", "reasoning_effort": "xhigh"}}}
			command := agentproto.Command{Kind: agentproto.CommandPromptSend, CommandID: "prompt", Target: agentproto.Target{ThreadID: "target"},
				Overrides:   agentproto.PromptOverrides{UseThreadSettings: true, Model: "surface-model", ReasoningEffort: "low", PlanMode: plan},
				CodexResume: &agentproto.CodexResumePolicy{Model: "policy-model", ModelMode: agentproto.CodexThreadValueExplicit, ReasoningEffort: "medium", ReasoningMode: agentproto.CodexThreadValueExplicit}}
			frames, err := tr.TranslateCommand(command)
			if err != nil || len(frames) != 1 {
				t.Fatalf("translate: %v", err)
			}
			var resume map[string]any
			_ = json.Unmarshal(frames[0], &resume)
			params := resume["params"].(map[string]any)
			if resume["method"] != "thread/resume" {
				t.Fatalf("must refresh native settings even for current thread: %s", frames[0])
			}
			assertNoThreadModelOverrides(t, params)
			response := fmt.Sprintf(`{"id":%q,"result":{"thread":{"id":"target"},"model":"fresh-native","reasoningEffort":null}}`, resume["id"])
			result := sharedObserve(t, tr, response)
			if len(result.OutboundToCodex) != 1 {
				t.Fatalf("no followup: %#v", result)
			}
			var turn map[string]any
			_ = json.Unmarshal(result.OutboundToCodex[0], &turn)
			params = turn["params"].(map[string]any)
			if _, ok := params["model"]; ok {
				t.Fatal("turn restored model override")
			}
			if _, ok := params["effort"]; ok {
				t.Fatal("turn restored effort override")
			}
			if plan == "" {
				if _, ok := params["collaborationMode"]; ok {
					t.Fatal("turn restored cached collaboration mode")
				}
			} else {
				mode := params["collaborationMode"].(map[string]any)
				settings := mode["settings"].(map[string]any)
				if mode["mode"] != normalizeOverridePlanMode(plan) || settings["model"] != "fresh-native" || settings["reasoning_effort"] != nil {
					t.Fatalf("plan did not use refreshed settings: %#v", mode)
				}
			}
			if len(result.Events) != 1 || result.Events[0].ThreadSettings.Model != "fresh-native" || !result.Events[0].ThreadSettings.ReasoningEffortSet {
				t.Fatalf("missing fresh snapshot projection: %#v", result.Events)
			}
			if cached := tr.observedThreads["target"]; cached.Model != "fresh-native" || cached.ReasoningEffort != "" {
				t.Fatalf("fresh snapshot did not replace cached effective settings: %#v", cached)
			}
		})
	}
}

func assertNoThreadModelOverrides(t *testing.T, params map[string]any) {
	t.Helper()
	for _, key := range []string{"model", "effort", "collaborationMode"} {
		if _, exists := params[key]; exists {
			t.Fatalf("stale %s present: %#v", key, params)
		}
	}
	if config, ok := params["config"].(map[string]any); ok {
		for _, key := range []string{"model", "model_reasoning_effort", "reasoning_effort"} {
			if _, exists := config[key]; exists {
				t.Fatalf("stale config %s present", key)
			}
		}
	}
}

func TestSharedThreadSettingsPlanRefreshRejectsMissingModel(t *testing.T) {
	tr := NewTranslator("i")
	tr.SetSharedBackend(true)
	sharedResume(t, tr, "target")
	frames, err := tr.TranslateCommand(agentproto.Command{Kind: agentproto.CommandPromptSend, Target: agentproto.Target{ThreadID: "target"}, Overrides: agentproto.PromptOverrides{UseThreadSettings: true, PlanMode: "on", Model: "stale"}})
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	_ = json.Unmarshal(frames[0], &request)
	result := sharedObserve(t, tr, fmt.Sprintf(`{"id":%q,"result":{"thread":{"id":"target"}}}`, request["id"]))
	if len(result.OutboundToCodex) != 0 || len(result.Events) != 1 || result.Events[0].Status != "failed" {
		t.Fatalf("missing snapshot silently discarded explicit plan: %#v", result)
	}
}

func TestSharedExplicitResumeProjectsOnlyActualNativeSnapshot(t *testing.T) {
	tr := NewTranslator("i")
	tr.SetSharedBackend(true)
	if _, err := tr.ObserveClient([]byte(`{"id":"native-resume","method":"thread/resume","params":{"threadId":"target","model":"stale-request","config":{"model_reasoning_effort":"xhigh"}}}`)); err != nil {
		t.Fatal(err)
	}
	result := sharedObserve(t, tr, `{"id":"native-resume","result":{"thread":{"id":"target"},"model":"fresh-native","reasoningEffort":null}}`)
	if len(result.OutboundToCodex) != 0 || len(result.Events) != 1 || result.Events[0].ThreadSettings.Model != "fresh-native" || !result.Events[0].ThreadSettings.ReasoningEffortSet || result.Events[0].ThreadSettings.ReasoningEffort != "" {
		t.Fatalf("resume did not project actual settings read-only: %#v", result)
	}
	if _, err := tr.ObserveClient([]byte(`{"id":"legacy-resume","method":"thread/resume","params":{"threadId":"target","model":"stale-request"}}`)); err != nil {
		t.Fatal(err)
	}
	result = sharedObserve(t, tr, `{"id":"legacy-resume","result":{"thread":{"id":"target"}}}`)
	if len(result.Events) != 0 {
		t.Fatalf("legacy resume fabricated settings from request/cache: %#v", result.Events)
	}
}

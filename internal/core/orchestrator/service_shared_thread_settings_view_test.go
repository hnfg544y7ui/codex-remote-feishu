package orchestrator

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestSharedThreadSettingsMenuObservesNativeValueWithoutExecutionClaim(t *testing.T) {
	svc, inst, owner := sharedSettingsProjectionFixture(t)
	reader := *owner
	reader.SurfaceSessionID = "read-only-surface"
	svc.root.Surfaces[reader.SurfaceSessionID] = &reader
	inst.Threads["thread-1"].ThreadSettings = &agentproto.ThreadSettingsUpdate{
		ThreadID: "thread-1", Model: "native-model", ReasoningEffortSet: true,
	}
	if svc.sharedThreadSettingsEnabled(&reader, inst) {
		t.Fatal("read-only surface acquired the execution owner's settings write access")
	}
	for _, command := range []string{control.FeishuCommandModel, control.FeishuCommandReasoning} {
		view := svc.buildConfigCommandView(&reader, command).Config
		if view == nil || !view.SharedThreadSettings || view.SharedThreadSettingsPending || view.OverrideValue != "" {
			t.Fatalf("shared read-only card regressed to local overrides: %#v", view)
		}
		if command == control.FeishuCommandModel && view.EffectiveValue != "native-model" {
			t.Fatalf("model card ignored native settings: %#v", view)
		}
		if command == control.FeishuCommandReasoning && view.EffectiveValue != "" {
			t.Fatalf("reasoning auto was replaced by stale local effort: %#v", view)
		}
	}
	inst.Threads["thread-1"].ThreadSettings = nil
	view := svc.buildConfigCommandView(&reader, control.FeishuCommandModel).Config
	if view == nil || !view.SharedThreadSettingsPending || view.EffectiveValue != "" {
		t.Fatalf("missing snapshot must show unknown, not old defaults: %#v", view)
	}
	owner.RouteMode, owner.PreparedThreadCWD = state.RouteModeNewThreadReady, "/new-project"
	if svc.sharedThreadSettingsEnabled(owner, inst) {
		t.Fatal("preparing a new chat must not write settings to the previous selection")
	}
	view = svc.buildConfigCommandView(owner, control.FeishuCommandModel).Config
	if view == nil || view.SharedThreadSettings {
		t.Fatal("new-chat settings card must keep initial prompt configuration semantics")
	}
}

func TestSharedThreadSettingsUnwritableContextCannotFallBackToGlobalDefaults(t *testing.T) {
	for _, reason := range []string{"another-owner", "offline"} {
		t.Run(reason, func(t *testing.T) {
			svc, sid, iid := sharedThreadSettingsTestSetup(t)
			surface, inst := svc.root.Surfaces[sid], svc.root.Instances[iid]
			if reason == "another-owner" {
				reader := *surface
				reader.SurfaceSessionID = "reader-surface"
				svc.root.Surfaces[reader.SurfaceSessionID] = &reader
				surface = &reader
			} else {
				inst.Online = false
			}
			before := surface.PromptOverride
			events := svc.ApplySurfaceAction(control.Action{Kind: control.ActionModelCommand, SurfaceSessionID: surface.SurfaceSessionID, Text: "/model model-b"})
			if surface.PromptOverride != before || findAgentCommand(events, agentproto.CommandThreadSettingsSet) != nil || len(svc.threadSettingsCommands) != 0 {
				t.Fatal("unwritable shared context mutated defaults or issued a native setting")
			}
		})
	}
}

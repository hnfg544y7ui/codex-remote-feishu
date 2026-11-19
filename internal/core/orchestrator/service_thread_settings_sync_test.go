package orchestrator

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func sharedThreadSettingsTestSetup(t *testing.T) (*Service, string, string) {
	t.Helper()
	svc, sid, iid := goalInterlockTestSetup(t)
	surface, inst := svc.root.Surfaces[sid], svc.root.Instances[iid]
	surface.ProductMode = state.ProductModeNormal
	inst.Capabilities.SharedThreadSettings = true
	inst.Threads["thread-1"].ThreadSettings = &agentproto.ThreadSettingsUpdate{ThreadID: "thread-1", Model: "model-a", ReasoningEffort: "high", ReasoningEffortSet: true}
	surface.PromptOverride = state.ModelConfigRecord{Model: "obsolete", ReasoningEffort: "low"}
	if !svc.sharedThreadSettingsEnabled(surface, inst) {
		t.Fatal("fixture must own an eligible shared Codex thread")
	}
	return svc, sid, iid
}

func sharedSettingsIssue(t *testing.T, svc *Service, sid string, kind control.ActionKind, text string) (*agentproto.Command, []eventcontract.Event) {
	t.Helper()
	events := svc.ApplySurfaceAction(control.Action{Kind: kind, SurfaceSessionID: sid, Text: text})
	command := findAgentCommand(events, agentproto.CommandThreadSettingsSet)
	if command == nil {
		t.Fatalf("missing native settings command: %#v", events)
	}
	for _, event := range events {
		if event.Command != nil && event.Command.Kind != agentproto.CommandThreadSettingsSet {
			t.Fatalf("configuration must not create a prompt/queue command: %#v", event.Command)
		}
	}
	return command, events
}

func TestSharedThreadSettingsPayloadPresenceAndNoGlobalMutation(t *testing.T) {
	for _, tc := range []struct {
		text          string
		kind          control.ActionKind
		model, effort *string
	}{
		{"/model model-b", control.ActionModelCommand, settingsTestString("model-b"), nil},
		{"/model model-b medium", control.ActionModelCommand, settingsTestString("model-b"), settingsTestString("medium")},
		{"/reasoning medium", control.ActionReasoningCommand, nil, settingsTestString("medium")},
	} {
		t.Run(tc.text, func(t *testing.T) {
			svc, sid, iid := sharedThreadSettingsTestSetup(t)
			surface := svc.root.Surfaces[sid]
			before := surface.PromptOverride
			global := make(map[string]state.BotCapabilitySettingsRecord)
			for key, record := range svc.root.BotCapabilitySettings {
				global[key] = record
			}
			command, _ := sharedSettingsIssue(t, svc, sid, tc.kind, tc.text)
			if !reflect.DeepEqual(command.ThreadSettingsSet.Model, tc.model) || !reflect.DeepEqual(command.ThreadSettingsSet.ReasoningEffort, tc.effort) {
				t.Fatalf("unexpected field presence: %#v", command.ThreadSettingsSet)
			}
			if command.Target.ThreadID != "thread-1" || svc.ThreadSettingsCommandInstanceID(command.CommandID) != iid || !NonTurnAgentCommand(command.Kind) {
				t.Fatalf("request lost captured non-turn target: %#v", command)
			}
			if surface.PromptOverride != before || len(global) != len(svc.root.BotCapabilitySettings) || (len(global) > 0 && !reflect.DeepEqual(global, svc.root.BotCapabilitySettings)) || len(surface.QueueItems) != 0 {
				t.Fatal("native settings write changed local defaults or queued a prompt")
			}
			if got := svc.root.Instances[iid].Threads["thread-1"].ThreadSettings.Model; got != "model-a" {
				t.Fatalf("request optimistically mutated backend projection: %q", got)
			}
		})
	}
}

func settingsTestString(value string) *string { return &value }

func TestSharedThreadSettingsModelIncompatibleEffortRequiresVerifiedDefault(t *testing.T) {
	for _, defaultEffort := range []string{"", "unsupported", "low"} {
		t.Run(defaultEffort, func(t *testing.T) {
			svc, sid, iid := sharedThreadSettingsTestSetup(t)
			svc.root.Instances[iid].ModelCatalog = &agentproto.ModelCatalogSnapshot{Entries: []agentproto.ModelCatalogEntry{{Model: "model-b", DefaultReasoningEffort: defaultEffort, SupportedReasoningEfforts: []agentproto.ReasoningEffortOption{{ReasoningEffort: "low"}}}}}
			events := svc.ApplySurfaceAction(control.Action{Kind: control.ActionModelCommand, SurfaceSessionID: sid, Text: "/model model-b"})
			command := findAgentCommand(events, agentproto.CommandThreadSettingsSet)
			if defaultEffort == "low" {
				if command == nil || command.ThreadSettingsSet.ReasoningEffort == nil || *command.ThreadSettingsSet.ReasoningEffort != "low" {
					t.Fatalf("must use catalog's explicitly supported default: %#v", command)
				}
			} else if command != nil || !settingsHasNotice(events, "thread_settings_effort_required") || len(svc.threadSettingsCommands) != 0 {
				t.Fatalf("unknown or unsupported default must reject without empty effort: %#v", events)
			}
			if svc.root.Instances[iid].Threads["thread-1"].ThreadSettings.ReasoningEffort != "high" || svc.root.Surfaces[sid].PromptOverride.Model != "obsolete" {
				t.Fatal("request must not optimistically mutate native or local settings")
			}
		})
	}
}

func TestSharedThreadSettingsAckIsNotSavedAndResultNeverReplaysOldValue(t *testing.T) {
	svc, sid, iid := sharedThreadSettingsTestSetup(t)
	command, _ := sharedSettingsIssue(t, svc, sid, control.ActionModelCommand, "/model model-b")
	if events := svc.HandleCommandAccepted(iid, agentproto.CommandAck{CommandID: command.CommandID}); len(events) != 0 || len(svc.threadSettingsCommands) != 1 {
		t.Fatal("transport ACK must not acknowledge native persistence")
	}
	svc.root.Instances[iid].Threads["thread-1"].ThreadSettings.Model = "newer-app-value"
	events := svc.ApplyAgentEvent(iid, agentproto.Event{Kind: agentproto.EventThreadSettingsSetResult, CommandID: command.CommandID, ThreadID: "thread-1"})
	if len(svc.threadSettingsCommands) != 0 || !settingsHasNotice(events, "thread_settings_saved") || svc.root.Instances[iid].Threads["thread-1"].ThreadSettings.Model != "newer-app-value" {
		t.Fatalf("RPC result must only confirm save, never replay stale requested settings: %#v", events)
	}
}

func settingsHasNotice(events []eventcontract.Event, code string) bool {
	for _, event := range events {
		if event.Notice != nil && event.Notice.Code == code {
			return true
		}
	}
	return false
}

func TestSharedThreadSettingsPendingFailureLifecycle(t *testing.T) {
	for _, scenario := range []string{"dispatch", "reject", "native", "timeout", "disconnect", "degraded"} {
		t.Run(scenario, func(t *testing.T) {
			svc, sid, iid := sharedThreadSettingsTestSetup(t)
			before := svc.root.Surfaces[sid].PromptOverride
			command, _ := sharedSettingsIssue(t, svc, sid, control.ActionReasoningCommand, "/reasoning medium")
			var events []eventcontract.Event
			switch scenario {
			case "dispatch":
				events = svc.HandleCommandDispatchFailure(sid, command.CommandID, errors.New("closed"))
			case "reject":
				events = svc.HandleCommandRejected(iid, agentproto.CommandAck{CommandID: command.CommandID})
			case "native":
				events = svc.ApplyAgentEvent(iid, agentproto.Event{Kind: agentproto.EventThreadSettingsSetResult, CommandID: command.CommandID, ThreadID: "thread-1", ErrorMessage: "denied"})
			case "timeout":
				events = svc.tickThreadSettingsCommands(svc.now().Add(31 * time.Second))
			case "disconnect":
				events = svc.ApplyInstanceDisconnected(iid)
			case "degraded":
				events = svc.ApplyInstanceTransportDegraded(iid, false)
			}
			// Existing disconnect lifecycle clears surface runtime overrides independently.
			unchanged := svc.root.Surfaces[sid].PromptOverride == before || scenario == "disconnect" || scenario == "degraded"
			if len(svc.threadSettingsCommands) != 0 || len(events) == 0 || !unchanged || findAgentCommand(events, agentproto.CommandThreadSettingsSet) != nil || svc.root.Instances[iid].Threads["thread-1"].ThreadSettings.Model != "model-a" {
				t.Fatalf("failed request must terminate without retry or changing defaults: %#v", events)
			}
		})
	}
}

func TestSharedThreadSettingsPendingContextAndSerialization(t *testing.T) {
	svc, sid, iid := sharedThreadSettingsTestSetup(t)
	command, _ := sharedSettingsIssue(t, svc, sid, control.ActionModelCommand, "/model model-b")
	duplicate := svc.ApplySurfaceAction(control.Action{Kind: control.ActionReasoningCommand, SurfaceSessionID: sid, Text: "/reasoning medium"})
	if findAgentCommand(duplicate, agentproto.CommandThreadSettingsSet) != nil || !settingsHasNotice(duplicate, "thread_settings_pending") {
		t.Fatal("must serialize same-thread writes")
	}
	for _, event := range []agentproto.Event{
		{Kind: agentproto.EventThreadSettingsSetResult, CommandID: command.CommandID, ThreadID: "same-name-other-id"},
		{Kind: agentproto.EventThreadSettingsSetResult, CommandID: "unrelated-command", ThreadID: "thread-1"},
	} {
		svc.ApplyAgentEvent(iid, event)
	}
	svc.applyThreadSettingsCommandResult("wrong-instance", agentproto.Event{CommandID: command.CommandID, ThreadID: "thread-1"})
	if len(svc.threadSettingsCommands) != 1 {
		t.Fatal("unrelated results must not complete pending write")
	}
	surface := svc.root.Surfaces[sid]
	surface.SelectedThreadID, surface.AttachedInstanceID = "other-thread", "other-instance"
	if svc.ThreadSettingsCommandInstanceID(command.CommandID) != iid {
		t.Fatal("surface switch must not retarget pending dispatch")
	}
	events := svc.ApplyAgentEvent(iid, agentproto.Event{Kind: agentproto.EventThreadSettingsSetResult, CommandID: command.CommandID, ThreadID: "thread-1"})
	if !settingsHasNotice(events, "thread_settings_context_changed") || len(svc.threadSettingsCommands) != 0 {
		t.Fatalf("old context must not claim current setting saved: %#v", events)
	}
}

func TestSharedThreadSettingsClearAndOldCapabilityCompatibility(t *testing.T) {
	svc, sid, iid := sharedThreadSettingsTestSetup(t)
	before := svc.root.Surfaces[sid].PromptOverride
	events := svc.ApplySurfaceAction(control.Action{Kind: control.ActionModelCommand, SurfaceSessionID: sid, Text: "/model clear"})
	if findAgentCommand(events, agentproto.CommandThreadSettingsSet) != nil || !settingsHasNotice(events, "thread_settings_native") || svc.root.Surfaces[sid].PromptOverride != before {
		t.Fatal("model clear must explain native settings without empty model writes")
	}
	events = svc.ApplySurfaceAction(control.Action{Kind: control.ActionReasoningCommand, SurfaceSessionID: sid, Text: "/reasoning clear"})
	if findAgentCommand(events, agentproto.CommandThreadSettingsSet) != nil || !settingsHasNotice(events, "thread_settings_clear_unsupported") || len(svc.threadSettingsCommands) != 0 || svc.root.Surfaces[sid].PromptOverride != before || svc.root.Instances[iid].Threads["thread-1"].ThreadSettings.ReasoningEffort != "high" {
		t.Fatal("native reasoning clear must explain unsupported reset without a no-op request")
	}
	svc.root.Instances[iid].Capabilities.SharedThreadSettings = false
	events = svc.ApplySurfaceAction(control.Action{Kind: control.ActionModelCommand, SurfaceSessionID: sid, Text: "/model old-standalone"})
	if findAgentCommand(events, agentproto.CommandThreadSettingsSet) != nil || svc.root.Surfaces[sid].PromptOverride.Model != "old-standalone" {
		t.Fatal("legacy backend must retain standalone override semantics")
	}
}

func TestSharedThreadSettingsLateResultAndConnectionGeneration(t *testing.T) {
	for _, scenario := range []string{"expired", "generation", "profile"} {
		t.Run(scenario, func(t *testing.T) {
			svc, sid, iid := sharedThreadSettingsTestSetup(t)
			inst := svc.root.Instances[iid]
			inst.CodexConnectionContract = &state.CodexConnectionContract{ConnectionContractID: "connection", ConnectionGeneration: 1}
			command, _ := sharedSettingsIssue(t, svc, sid, control.ActionModelCommand, "/model model-b")
			expected := "thread_settings_context_changed"
			switch scenario {
			case "expired":
				binding := svc.threadSettingsCommands[command.CommandID]
				binding.ExpiresAt = svc.now().Add(-time.Second)
				svc.threadSettingsCommands[command.CommandID] = binding
				expected = "thread_settings_timeout"
			case "generation":
				inst.CodexConnectionContract.ConnectionGeneration++
			case "profile":
				svc.root.Surfaces[sid].CodexProfileID = "different-profile"
			}
			events := svc.ApplyAgentEvent(iid, agentproto.Event{Kind: agentproto.EventThreadSettingsSetResult, CommandID: command.CommandID, ThreadID: "thread-1"})
			if !settingsHasNotice(events, expected) || len(svc.threadSettingsCommands) != 0 {
				t.Fatalf("late/context-switched result must remain unconfirmed: %#v", events)
			}
		})
	}
}

func TestSharedThreadSettingsMenuSealsRequestWithoutPrematureSuccess(t *testing.T) {
	svc, sid, _ := sharedThreadSettingsTestSetup(t)
	action := control.Action{Kind: control.ActionModelCommand, SurfaceSessionID: sid, Text: "/model model-b", MessageID: "model-form-owner", Inbound: &control.ActionInboundMeta{CardDaemonLifecycleID: "current-lifecycle"}}
	resolved, ok := control.ResolveFeishuActionCatalog(svc.buildCatalogContext(svc.root.Surfaces[sid]), action)
	if !ok {
		t.Fatal("missing command catalog provenance")
	}
	events := svc.ApplySurfaceAction(resolved.Action)
	if findAgentCommand(events, agentproto.CommandThreadSettingsSet) == nil {
		t.Fatalf("missing setting request: %#v", events)
	}
	for _, event := range events {
		if settingsHasNotice([]eventcontract.Event{event}, "thread_settings_saved") {
			t.Fatal("request receipt must not claim saved")
		}
		if event.PageView != nil {
			if !event.PageView.Sealed {
				t.Fatal("request receipt must seal old form")
			}
			return
		}
	}
	t.Fatalf("missing sealed request receipt: %#v", events)
}

func TestSharedThreadSettingsSameNameOtherThreadWritesIndependently(t *testing.T) {
	svc, sid, iid := sharedThreadSettingsTestSetup(t)
	first, _ := sharedSettingsIssue(t, svc, sid, control.ActionModelCommand, "/model model-b")
	inst := svc.root.Instances[iid]
	inst.Threads["thread-2"] = &state.ThreadRecord{ThreadID: "thread-2", Name: inst.Threads["thread-1"].Name, Loaded: true, ThreadSettings: &agentproto.ThreadSettingsUpdate{ThreadID: "thread-2", Model: "model-a"}}
	secondSurface := *svc.root.Surfaces[sid]
	secondSurface.SurfaceSessionID, secondSurface.SelectedThreadID = "surface-2", "thread-2"
	svc.root.Surfaces[secondSurface.SurfaceSessionID] = &secondSurface
	svc.bindThreadClaim(&secondSurface, iid, "thread-2")
	second, _ := sharedSettingsIssue(t, svc, secondSurface.SurfaceSessionID, control.ActionModelCommand, "/model model-c")
	if len(svc.threadSettingsCommands) != 2 || first.Target.ThreadID == second.Target.ThreadID {
		t.Fatal("same display name must not serialize distinct threads")
	}
	svc.ApplyAgentEvent(iid, agentproto.Event{Kind: agentproto.EventThreadSettingsSetResult, CommandID: second.CommandID, ThreadID: "thread-2"})
	if len(svc.threadSettingsCommands) != 1 || svc.ThreadSettingsCommandInstanceID(first.CommandID) != iid {
		t.Fatal("second thread result must preserve first pending request")
	}
}

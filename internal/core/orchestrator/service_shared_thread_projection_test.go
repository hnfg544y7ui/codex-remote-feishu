package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func sharedSettingsProjectionFixture(t *testing.T) (*Service, *state.InstanceRecord, *state.SurfaceConsoleRecord) {
	t.Helper()
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	inst := &state.InstanceRecord{InstanceID: "shared-1", Backend: agentproto.BackendCodex, Online: true,
		WorkspaceRoot: "/repo", WorkspaceKey: "/repo", Capabilities: agentproto.Capabilities{SharedThreadSettings: true},
		Threads: map[string]*state.ThreadRecord{
			"thread-1": {ThreadID: "thread-1", CWD: "/repo", Loaded: true, ExplicitModel: "old-model", ExplicitReasoningEffort: "high", ObservedAccessMode: agentproto.AccessModeFullAccess, ObservedPlanMode: state.PlanModeSettingOn},
			"thread-2": {ThreadID: "thread-2", CWD: "/repo", Loaded: true, ExplicitModel: "other-thread-model"},
		}}
	svc.root.Instances[inst.InstanceID] = inst
	surface := svc.ensureSurface(control.Action{SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1"})
	surface.ProductMode, surface.Backend = state.ProductModeNormal, agentproto.BackendCodex
	surface.AttachedInstanceID, surface.SelectedThreadID = inst.InstanceID, "thread-1"
	surface.PromptOverride = state.ModelConfigRecord{Model: "old-bot-model", ReasoningEffort: "xhigh", AccessMode: agentproto.AccessModeFullAccess}
	svc.bindThreadClaim(surface, inst.InstanceID, "thread-1")
	return svc, inst, surface
}

func TestSharedThreadSettingsObservedProjectionWinsForEverySurface(t *testing.T) {
	svc, inst, surface := sharedSettingsProjectionFixture(t)
	second := svc.ensureSurface(control.Action{SurfaceSessionID: "surface-2", ChatID: "chat-2", ActorUserID: "user-2"})
	second.ProductMode, second.Backend = state.ProductModeNormal, agentproto.BackendCodex
	second.AttachedInstanceID, second.SelectedThreadID = inst.InstanceID, "thread-1"
	second.PromptOverride = state.ModelConfigRecord{Model: "second-old-model", ReasoningEffort: "high"}
	svc.ApplyAgentEvent(inst.InstanceID, agentproto.Event{Kind: agentproto.EventThreadSettingsUpdated,
		ThreadSettings: &agentproto.ThreadSettingsUpdate{ThreadID: "thread-1", Model: "native-model", ReasoningEffort: "low", ReasoningEffortSet: true}})
	for _, target := range []*state.SurfaceConsoleRecord{surface, second} {
		resolved := svc.resolvePromptConfig(inst, target, "thread-1", "/repo", state.ModelConfigRecord{})
		if resolved.EffectiveModel.Value != "native-model" || resolved.EffectiveReasoningEffort.Value != "low" || resolved.Override.Model != "" || resolved.Override.ReasoningEffort != "" {
			t.Fatalf("surface %s retained stale settings: %#v", target.SurfaceSessionID, resolved)
		}
	}
	if surface.PromptOverride.Model != "old-bot-model" || second.PromptOverride.Model != "second-old-model" || inst.Threads["thread-2"].ExplicitModel != "other-thread-model" {
		t.Fatal("projection changed persistent defaults or another thread")
	}
	thread := inst.Threads["thread-1"]
	if thread.ObservedAccessMode != agentproto.AccessModeFullAccess || thread.ObservedPlanMode != state.PlanModeSettingOn {
		t.Fatal("model projection changed permission or plan state")
	}
	// Native effort:null is an intentional automatic setting, not an invitation
	// to recover the previous bot default or explicit effort.
	svc.ApplyAgentEvent(inst.InstanceID, agentproto.Event{Kind: agentproto.EventThreadSettingsUpdated,
		ThreadSettings: &agentproto.ThreadSettingsUpdate{ThreadID: "thread-1", Model: "native-model", ReasoningEffortSet: true}})
	resolved := svc.resolvePromptConfig(inst, surface, "thread-1", "/repo", state.ModelConfigRecord{})
	if resolved.EffectiveReasoningEffort.Value != "" || thread.ExplicitReasoningEffort != "" || resolved.EffectiveReasoningEffort.Source != "thread" {
		t.Fatalf("native automatic effort was overwritten: %#v, %#v", resolved, thread)
	}
	svc.ApplyAgentEvent(inst.InstanceID, agentproto.Event{Kind: agentproto.EventThreadSettingsUpdated,
		ThreadSettings: &agentproto.ThreadSettingsUpdate{ThreadID: "thread-1", ReasoningEffort: "medium", ReasoningEffortSet: true}})
	resolved = svc.resolvePromptConfig(inst, surface, "thread-1", "/repo", state.ModelConfigRecord{})
	if resolved.EffectiveModel.Value != "native-model" || resolved.EffectiveReasoningEffort.Value != "medium" {
		t.Fatalf("partial native update discarded an observed field: %#v", resolved)
	}
}

func TestSharedThreadSettingsProjectionIsolatesConnectionsAndThreads(t *testing.T) {
	svc, source, surface := sharedSettingsProjectionFixture(t)
	source.CodexConnectionContract = &state.CodexConnectionContract{ConnectionContractID: "connection-1", ModelProviderID: "provider", ConnectionGeneration: 2}
	for _, id := range []string{"same", "different", "missing", "generation"} {
		inst := &state.InstanceRecord{InstanceID: id, Backend: agentproto.BackendCodex, Online: true, Capabilities: agentproto.Capabilities{SharedThreadSettings: true},
			CodexConnectionContract: state.CloneCodexConnectionContract(source.CodexConnectionContract),
			Threads:                 map[string]*state.ThreadRecord{"thread-1": {ThreadID: "thread-1", ExplicitModel: "unrelated", ThreadSettings: &agentproto.ThreadSettingsUpdate{ThreadID: "thread-1", ApprovalPolicy: "preserve"}}, "thread-2": {ThreadID: "thread-2", ExplicitModel: "second"}}}
		if id == "different" {
			inst.CodexConnectionContract.ConnectionContractID = "connection-2"
		}
		if id == "missing" {
			inst.CodexConnectionContract = nil
		}
		if id == "generation" {
			inst.CodexConnectionContract.ConnectionGeneration++
		}
		svc.root.Instances[id] = inst
	}
	svc.ApplyAgentEvent(source.InstanceID, agentproto.Event{Kind: agentproto.EventThreadSettingsUpdated,
		ThreadSettings: &agentproto.ThreadSettingsUpdate{ThreadID: "thread-1", Model: "updated", ReasoningEffort: "medium", ApprovalPolicy: "never", Sandbox: "danger-full-access"}})
	for _, id := range []string{"same", "different", "missing", "generation"} {
		thread := svc.root.Instances[id].Threads["thread-1"]
		want := "unrelated"
		if id == "same" {
			want = "updated"
		}
		if thread.ExplicitModel != want || thread.ThreadSettings.ApprovalPolicy != "preserve" || svc.root.Instances[id].Threads["thread-2"].ExplicitModel != "second" {
			t.Fatalf("connection/thread scope violated for %s: %#v", id, thread)
		}
	}
	if got := svc.resolvePromptConfig(source, surface, "thread-1", "/repo", state.ModelConfigRecord{}).EffectiveModel.Value; got != "updated" {
		t.Fatalf("source projection = %q", got)
	}
}

func TestSharedThreadPromptDispatchDoesNotReplayFrozenModel(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_first_snapshot", true: "observed"}[observed], func(t *testing.T) {
			svc, inst, surface := sharedSettingsProjectionFixture(t)
			if observed {
				inst.Threads["thread-1"].ThreadSettings = &agentproto.ThreadSettingsUpdate{ThreadID: "thread-1", Model: "latest", ReasoningEffort: "low"}
			}
			item := &state.QueueItemRecord{SurfaceSessionID: surface.SurfaceSessionID, FrozenOverride: state.ModelConfigRecord{Model: "frozen-old", ReasoningEffort: "high", AccessMode: agentproto.AccessModeFullAccess},
				FrozenPlanMode: state.PlanModeSettingOn, FrozenDispatchPlan: agentproto.DefaultPromptDispatchPlanForExecutionThread("thread-1"), Inputs: []agentproto.Input{{Type: "text", Text: "hello"}}}
			command := svc.promptSendCommandFromQueueItem(surface, item, "user-1", "message-1")
			if !command.Overrides.UseThreadSettings || command.Overrides.Model != "" || command.Overrides.ReasoningEffort != "" || command.Overrides.AccessMode != agentproto.AccessModeFullAccess || command.Overrides.PlanMode != "on" || command.Target.ThreadID != "thread-1" || command.Prompt.Inputs[0].Text != "hello" {
				t.Fatalf("unexpected shared dispatch: %#v", command)
			}
			if item.FrozenOverride.Model != "frozen-old" {
				t.Fatal("dispatch mutated frozen queue record")
			}
		})
	}
}

func TestSharedThreadPromptDispatchKeepsFrozenNonModelPolicy(t *testing.T) {
	svc, inst, surface := sharedSettingsProjectionFixture(t)
	inst.CodexConnectionContract = &state.CodexConnectionContract{ConnectionContractID: "conn", ModelProviderID: "provider"}
	inst.Threads["thread-1"].CodexEffectiveThread = &agentproto.CodexEffectiveThreadContract{ConnectionContractID: "conn", ModelProviderID: "provider", Model: "old-native", ReasoningEffort: "high"}
	item := &state.QueueItemRecord{CodexConnectionContract: state.CloneCodexConnectionContract(inst.CodexConnectionContract), CodexThreadPolicy: &state.CodexThreadPolicy{ThreadPolicyID: "policy-1", DeveloperInstruction: "keep", ContextMode: state.CodexContextModeExtended, ContextWindow: 1000000, AutoCompactLimit: 900000},
		FrozenOverride: state.ModelConfigRecord{Model: "frozen", ReasoningEffort: "high"}, FrozenDispatchPlan: agentproto.DefaultPromptDispatchPlanForExecutionThread("thread-1")}
	command := svc.promptSendCommandFromQueueItem(surface, item, "user", "message")
	if !command.Overrides.UseThreadSettings || command.CodexResume.Model != "" || command.CodexResume.ReasoningEffort != "" || command.CodexResume.ModelMode != agentproto.CodexThreadValueDefault || command.CodexResume.DeveloperInstruction != "keep" || command.CodexResume.ContextWindow != 1000000 {
		t.Fatalf("stale model replay or nonmodel policy loss: command=%#v resume=%#v", command, command.CodexResume)
	}
}

func TestSharedThreadPromptDispatchPreservesNewForkAndConnectionMismatch(t *testing.T) {
	for _, variant := range []string{"new", "fork", "another-thread", "legacy-cli", "connection-mismatch"} {
		t.Run(variant, func(t *testing.T) {
			svc, inst, surface := sharedSettingsProjectionFixture(t)
			item := &state.QueueItemRecord{FrozenOverride: state.ModelConfigRecord{Model: "frozen", ReasoningEffort: "high"}, FrozenDispatchPlan: agentproto.DefaultPromptDispatchPlanForExecutionThread("thread-1")}
			switch variant {
			case "new":
				item.FrozenDispatchPlan = agentproto.DefaultPromptDispatchPlanForExecutionThread("")
			case "fork":
				item.FrozenDispatchPlan.ExecutionMode = agentproto.PromptExecutionModeForkEphemeral
				item.FrozenDispatchPlan.SourceThreadID = "thread-1"
			case "another-thread":
				item.FrozenDispatchPlan.ExecutionThreadID = "thread-2"
			case "legacy-cli":
				inst.Capabilities.SharedThreadSettings = false
			case "connection-mismatch":
				item.CodexConnectionContract = &state.CodexConnectionContract{ConnectionContractID: "old", ModelProviderID: "old-provider"}
			}
			command := svc.promptSendCommandFromQueueItem(surface, item, "user", "message")
			if command.Overrides.UseThreadSettings || command.Overrides.Model != "frozen" || command.Overrides.ReasoningEffort != "high" {
				t.Fatalf("unexpected native-settings dispatch for %s: %#v", variant, command)
			}
		})
	}
}

func TestSharedThreadSettingsWithoutSnapshotStayUnknown(t *testing.T) {
	svc, inst, surface := sharedSettingsProjectionFixture(t)
	resolved := svc.resolvePromptConfig(inst, surface, "thread-1", "/repo", state.ModelConfigRecord{})
	if resolved.EffectiveModel.Value != "" || resolved.EffectiveReasoningEffort.Value != "" || resolved.EffectiveAccessMode != agentproto.AccessModeFullAccess {
		t.Fatalf("unobserved shared settings reused stale cache: %#v", resolved)
	}
	inst.Capabilities.SharedThreadSettings = false
	resolved = svc.resolvePromptConfig(inst, surface, "thread-1", "/repo", state.ModelConfigRecord{})
	if resolved.EffectiveModel.Value != "old-bot-model" || resolved.EffectiveReasoningEffort.Value != "xhigh" {
		t.Fatalf("legacy behavior changed: %#v", resolved)
	}
}

func TestSharedThreadEnqueueDoesNotApplyOldProfileModelGroup(t *testing.T) {
	svc, inst, surface := sharedSettingsProjectionFixture(t)
	inst.Threads["thread-1"].ExplicitModel = "gpt-5.4"
	inst.Threads["thread-1"].ThreadSettings = &agentproto.ThreadSettingsUpdate{ThreadID: "thread-1", Model: "gpt-5.4"}
	surface.CodexThreadPolicy = &state.CodexThreadPolicy{ThreadPolicyID: "old-policy", ModelMode: state.CodexThreadValueExplicit, Model: "claude-sonnet-4"}
	svc.enqueueQueueItem(surface, "msg", "hello", nil, []agentproto.Input{{Type: "text", Text: "hello"}}, "thread-1", "/repo", state.RouteModePinned, state.ModelConfigRecord{}, false)
	if len(surface.QueueItems) == 0 {
		t.Fatal("expected a queued item")
	}
	for _, item := range surface.QueueItems {
		plan := queuedItemPromptDispatchPlan(item)
		if plan.ExecutionMode != agentproto.PromptExecutionModeResumeExisting || plan.ExecutionThreadID != "thread-1" {
			t.Fatalf("old profile model changed shared route: %#v", plan)
		}
	}
}

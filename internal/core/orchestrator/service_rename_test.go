package orchestrator

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/frontstagecontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func issueRenameForTest(t *testing.T, svc *Service, surfaceID, text string) *agentproto.Command {
	t.Helper()
	events := svc.ApplySurfaceAction(control.Action{Kind: control.ActionRenameCommand, SurfaceSessionID: surfaceID, Text: text})
	command := findAgentCommand(events, agentproto.CommandThreadNameSet)
	if command == nil {
		t.Fatalf("expected rename command, got %#v", events)
	}
	page := renamePageForTest(events)
	if page == nil || page.Sealed || page.TrackingKey != command.CommandID || !page.Patchable {
		t.Fatalf("expected pending rename owner card, got %#v", page)
	}
	return command
}

func renamePageForTest(events []eventcontract.Event) *control.FeishuPageView {
	for _, event := range events {
		if event.PageView != nil && event.PageView.CommandID == control.FeishuCommandRename {
			return event.PageView
		}
	}
	return nil
}

func renameFormActionForTest(t *testing.T, svc *Service, surfaceID, text string, submit bool) control.Action {
	t.Helper()
	action := control.Action{
		Kind: control.ActionRenameCommand, SurfaceSessionID: surfaceID, Text: text,
		MessageID: "rename-form-owner", LocalPageAction: !submit,
		Inbound: &control.ActionInboundMeta{CardDaemonLifecycleID: "current-lifecycle"},
	}
	if submit {
		action.FieldName = "command_args"
		action.InstanceID = "inst-1"
		action.ThreadID = "thread-1"
	}
	resolved, ok := control.ResolveFeishuActionCatalog(svc.buildCatalogContext(svc.root.Surfaces[surfaceID]), action)
	if !ok {
		t.Fatal("rename action missing catalog provenance")
	}
	return resolved.Action
}

func renameFormForTest(page *control.FeishuPageView) *control.CommandCatalogForm {
	if page == nil {
		return nil
	}
	for _, section := range page.Sections {
		for _, entry := range section.Entries {
			if entry.Form != nil {
				return entry.Form
			}
		}
	}
	return nil
}

func TestRenameBareMenuOpensFormWithCurrentName(t *testing.T) {
	svc, sid, iid := goalInterlockTestSetup(t)
	thread := svc.root.Instances[iid].Threads["thread-1"]
	thread.Name = "原会话  中文名称"
	events := svc.ApplySurfaceAction(renameFormActionForTest(t, svc, sid, "/rename", false))
	page := renamePageForTest(events)
	form := renameFormForTest(page)
	if page == nil || !page.Interactive || page.Sealed || page.MessageID != "" || form == nil {
		t.Fatalf("expected editable form on original menu card: %#v", page)
	}
	if !events[0].InlineReplaceCurrentCard {
		t.Fatal("form must be eligible for synchronous current-card replacement")
	}
	if form.Field.DefaultValue != thread.Name || form.Field.Name != "command_args" {
		t.Fatalf("form lost current formal name or argument field: %#v", form)
	}
	if form.SubmitValue[frontstagecontract.CardActionPayloadKeyInstanceID] != iid ||
		form.SubmitValue[frontstagecontract.CardActionPayloadKeyThreadID] != "thread-1" ||
		form.SubmitValue[frontstagecontract.CardActionPayloadKeyKind] != frontstagecontract.CardActionKindPageSubmit {
		t.Fatalf("form submit must carry its exact target: %#v", form.SubmitValue)
	}
	if form.CatalogFamilyID != control.FeishuCommandRename || findAgentCommand(events, agentproto.CommandThreadNameSet) != nil ||
		len(svc.renameCommands) != 0 || thread.Name != "原会话  中文名称" {
		t.Fatal("opening form must not issue rename or mutate the name")
	}
}

func TestRenameFormSubmitKeepsOriginalOwnerCard(t *testing.T) {
	svc, sid, iid := goalInterlockTestSetup(t)
	svc.ApplySurfaceAction(renameFormActionForTest(t, svc, sid, "/rename", false))
	events := svc.ApplySurfaceAction(renameFormActionForTest(t, svc, sid, "/rename 新会话  中文名称", true))
	command := findAgentCommand(events, agentproto.CommandThreadNameSet)
	page := renamePageForTest(events)
	if command == nil || command.Name != "新会话  中文名称" || command.Target.ThreadID != "thread-1" ||
		page == nil || page.Sealed || page.MessageID != "rename-form-owner" || renameFormForTest(page) != nil {
		t.Fatalf("submit must enter waiting state on original owner card: %#v, %#v", command, page)
	}
	events = svc.ApplyAgentEvent(iid, agentproto.Event{
		Kind: agentproto.EventThreadNameCommandResult, CommandID: command.CommandID,
		ThreadID: command.Target.ThreadID, Name: command.Name,
	})
	page = renamePageForTest(events)
	if page == nil || !page.Sealed || page.MessageID != "rename-form-owner" ||
		svc.root.Instances[iid].Threads["thread-1"].Name != command.Name {
		t.Fatalf("result must seal original form card: %#v", page)
	}
}

func TestRenameLocalMenuWithoutProvenanceBuildsStrictFormSubmission(t *testing.T) {
	for _, scenario := range []string{"success", "blank", "missing provenance"} {
		t.Run(scenario, func(t *testing.T) {
			svc, sid, iid := goalInterlockTestSetup(t)
			launcher := control.Action{
				Kind: control.ActionRenameCommand, SurfaceSessionID: sid, Text: "/rename",
				MessageID: "real-menu-owner", LocalPageAction: true,
				Inbound: &control.ActionInboundMeta{CardCallback: true, CardDaemonLifecycleID: "current-lifecycle"},
			}
			if control.HasStrictFeishuCommandCatalogProvenance(launcher) {
				t.Fatal("real local menu launcher must not be implicitly test-stamped")
			}
			events := svc.ApplySurfaceAction(launcher)
			form := renameFormForTest(renamePageForTest(events))
			if form == nil || findAgentCommand(events, agentproto.CommandThreadNameSet) != nil {
				t.Fatalf("local launcher must open form without issuing a command: %#v", events)
			}
			payload := form.SubmitValue
			submit := control.Action{
				Kind: control.ActionRenameCommand, SurfaceSessionID: sid, Text: "/rename 中文  名称",
				MessageID: "real-menu-owner", FieldName: "command_args",
				InstanceID:       payload[frontstagecontract.CardActionPayloadKeyInstanceID].(string),
				ThreadID:         payload[frontstagecontract.CardActionPayloadKeyThreadID].(string),
				CatalogFamilyID:  payload[frontstagecontract.CardActionPayloadKeyCatalogFamilyID].(string),
				CatalogVariantID: payload[frontstagecontract.CardActionPayloadKeyCatalogVariantID].(string),
				CatalogBackend:   agentproto.Backend(payload[frontstagecontract.CardActionPayloadKeyCatalogBackend].(string)),
				Inbound:          &control.ActionInboundMeta{CardCallback: true, CardDaemonLifecycleID: "current-lifecycle"},
			}
			if !control.HasStrictFeishuCommandCatalogProvenance(submit) ||
				!control.MatchesFeishuCommandCatalogContext(submit, svc.buildCatalogContext(svc.root.Surfaces[sid])) ||
				submit.InstanceID != iid || submit.ThreadID != "thread-1" {
				t.Fatalf("form must stamp current strict provenance and exact target: %#v", payload)
			}
			if scenario == "blank" {
				submit.Text = "/rename    "
			}
			if scenario == "missing provenance" {
				submit.CatalogFamilyID, submit.CatalogVariantID, submit.CatalogBackend = "", "", ""
			}
			events = svc.ApplySurfaceAction(submit)
			command := findAgentCommand(events, agentproto.CommandThreadNameSet)
			switch scenario {
			case "success":
				if command == nil || command.Name != "中文  名称" || command.Target.ThreadID != "thread-1" {
					t.Fatalf("real form provenance must permit submission: %#v", events)
				}
			case "blank":
				page := renamePageForTest(events)
				if command != nil || page == nil || page.Sealed || renameFormForTest(page) == nil ||
					len(page.NoticeSections) == 0 || !strings.Contains(strings.Join(page.NoticeSections[0].Lines, ""), "名称不能为空") {
					t.Fatalf("blank submission must retain editable validation form: %#v", events)
				}
			case "missing provenance":
				if command != nil || len(events) != 1 || events[0].Notice == nil || events[0].Notice.Code != "command_entry_expired" {
					t.Fatalf("untrusted form submit must retain strict provenance rejection: %#v", events)
				}
			}
		})
	}
}

func TestRenameBlankFormSubmitShowsEditableErrorWithoutRenaming(t *testing.T) {
	svc, sid, iid := goalInterlockTestSetup(t)
	thread := svc.root.Instances[iid].Threads["thread-1"]
	thread.Name = "原名称"
	events := svc.ApplySurfaceAction(renameFormActionForTest(t, svc, sid, "/rename    ", true))
	page := renamePageForTest(events)
	if page == nil || page.Sealed || !page.Interactive || renameFormForTest(page) == nil ||
		page.MessageID != "" || len(page.NoticeSections) == 0 ||
		!strings.Contains(strings.Join(page.NoticeSections[0].Lines, ""), "名称不能为空") {
		t.Fatalf("blank submission needs editable validation error: %#v", page)
	}
	if !events[0].InlineReplaceCurrentCard {
		t.Fatal("blank form error must replace the current card synchronously")
	}
	if findAgentCommand(events, agentproto.CommandThreadNameSet) != nil || len(svc.renameCommands) != 0 || thread.Name != "原名称" {
		t.Fatal("blank form submit must not rename")
	}
	events = svc.ApplySurfaceAction(control.Action{Kind: control.ActionRenameCommand, SurfaceSessionID: sid, Text: "/rename"})
	page = renamePageForTest(events)
	if page == nil || !page.Sealed || renameFormForTest(page) != nil ||
		!strings.Contains(strings.Join(page.NoticeSections[0].Lines, ""), "/rename 新名称") {
		t.Fatalf("direct bare slash should retain usage: %#v", page)
	}
}

func TestRenameFormRejectsChangedOrMissingExactTarget(t *testing.T) {
	for _, target := range []string{"thread", "instance", "missing"} {
		t.Run(target, func(t *testing.T) {
			svc, sid, iid := goalInterlockTestSetup(t)
			action := renameFormActionForTest(t, svc, sid, "/rename 新名称", true)
			switch target {
			case "thread":
				thread := *svc.root.Instances[iid].Threads["thread-1"]
				thread.ThreadID = "thread-2"
				svc.root.Instances[iid].Threads["thread-2"] = &thread
				svc.ApplySurfaceAction(control.Action{Kind: control.ActionUseThread, SurfaceSessionID: sid, ThreadID: "thread-2"})
			case "instance":
				action.InstanceID = "other-instance"
			case "missing":
				action.InstanceID, action.ThreadID = "", ""
			}
			events := svc.ApplySurfaceAction(action)
			page := renamePageForTest(events)
			if findAgentCommand(events, agentproto.CommandThreadNameSet) != nil || len(svc.renameCommands) != 0 ||
				page == nil || !page.Sealed || renameFormForTest(page) != nil {
				t.Fatalf("stale or unbound form must not rename current selection: %#v", events)
			}
		})
	}
}

func TestRenameTargetsExactSelectionAndPreservesName(t *testing.T) {
	svc, surfaceID, instanceID := goalInterlockTestSetup(t)
	inst := svc.root.Instances[instanceID]
	inst.ActiveThreadID = "other-thread"
	inst.ActiveTurnID = "local-turn"
	surface := svc.root.Surfaces[surfaceID]
	surface.QueuedQueueItemIDs = []string{"queued-existing"}
	surface.ActiveQueueItemID = "existing"
	surface.QueueItems["existing"] = &state.QueueItemRecord{ID: "existing", Status: state.QueueItemRunning}
	command := issueRenameForTest(t, svc, surfaceID, "/RENAME\t NanoPi  中文检查  ")
	if command.Target.ThreadID != "thread-1" || command.Name != "NanoPi  中文检查" || command.Target.CreateThreadIfMissing {
		t.Fatalf("unexpected rename request: %#v", command)
	}
	if !NonTurnAgentCommand(command.Kind) || svc.RenameCommandInstanceID(command.CommandID) != instanceID {
		t.Fatal("rename must be a non-turn command bound to its original instance")
	}
	if surface.SelectedThreadID != "thread-1" || surface.RouteMode != state.RouteModePinned ||
		surface.ActiveQueueItemID != "existing" || len(surface.QueuedQueueItemIDs) != 1 || inst.ActiveTurnID != "local-turn" {
		t.Fatal("rename changed route, queue or current turn")
	}
	svc.BindPendingRemoteCommand(surfaceID, command.CommandID)
	if binding := svc.pendingRemoteBindingByCommandForInstance(instanceID, command.CommandID); binding != nil {
		t.Fatalf("rename must not be bound as a prompt turn: %#v", binding)
	}
	if got := svc.HandleCommandAccepted(instanceID, agentproto.CommandAck{CommandID: command.CommandID, Accepted: true}); len(got) != 0 {
		t.Fatalf("transport acceptance must not report rename success: %#v", got)
	}
}

func TestRenameRejectsMissingTargetOrBlankName(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Service, string, string)
		text   string
	}{
		{name: "blank", text: "/rename \t  "},
		{name: "unselected", mutate: func(s *Service, sid, _ string) { s.root.Surfaces[sid].SelectedThreadID = "" }},
		{name: "not owner", mutate: func(s *Service, _, _ string) { delete(s.threadClaims, "thread-1") }},
		{name: "missing", mutate: func(s *Service, _, iid string) { delete(s.root.Instances[iid].Threads, "thread-1") }},
		{name: "archived", mutate: func(s *Service, _, iid string) { s.root.Instances[iid].Threads["thread-1"].Archived = true }},
		{name: "helper", mutate: func(s *Service, _, iid string) {
			s.root.Instances[iid].Threads["thread-1"].TrafficClass = agentproto.TrafficClassInternalHelper
		}},
		{name: "offline", mutate: func(s *Service, _, iid string) { s.root.Instances[iid].Online = false }},
		{name: "unattached", mutate: func(s *Service, sid, _ string) { s.root.Surfaces[sid].AttachedInstanceID = "" }},
		{name: "vscode", mutate: func(s *Service, sid, _ string) { s.root.Surfaces[sid].ProductMode = state.ProductModeVSCode }},
		{name: "claude", mutate: func(s *Service, _, iid string) { s.root.Instances[iid].Backend = agentproto.BackendClaude }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, sid, iid := goalInterlockTestSetup(t)
			if tc.mutate != nil {
				tc.mutate(svc, sid, iid)
			}
			text := tc.text
			if text == "" {
				text = "/rename 新名称"
			}
			events := svc.handleRenameCommand(svc.root.Surfaces[sid], control.Action{Text: text})
			if findAgentCommand(events, agentproto.CommandThreadNameSet) != nil || len(svc.renameCommands) != 0 {
				t.Fatal("rejected rename issued a command")
			}
			page := renamePageForTest(events)
			if page == nil || !page.Sealed || page.Phase != "failed" {
				t.Fatalf("missing error card: %#v", page)
			}
			if tc.name == "blank" && !strings.Contains(strings.Join(page.NoticeSections[0].Lines, ""), "/rename 新名称") {
				t.Fatal("blank name needs usage")
			}
		})
	}
}

func TestRenameResultStaysWithOriginalThreadAfterSwitch(t *testing.T) {
	svc, sid, iid := goalInterlockTestSetup(t)
	inst := svc.root.Instances[iid]
	oldName := inst.Threads["thread-1"].Name
	inst.Threads["thread-2"] = &state.ThreadRecord{ThreadID: "thread-2", Name: "另一个会话", CWD: inst.WorkspaceRoot, Loaded: true}
	command := issueRenameForTest(t, svc, sid, "/rename 新名字  中文")
	svc.RecordPageTrackingMessage(sid, command.CommandID, "om-rename-owner")
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionUseThread, SurfaceSessionID: sid, ThreadID: "thread-2"})
	surfaceBefore, _ := json.Marshal(svc.root.Surfaces[sid])
	for _, event := range []agentproto.Event{
		{Kind: agentproto.EventThreadNameCommandResult, CommandID: "unknown", ThreadID: "thread-1", Name: command.Name},
		{Kind: agentproto.EventThreadNameCommandResult, CommandID: command.CommandID, ThreadID: "thread-2", Name: command.Name},
	} {
		if got := svc.ApplyAgentEvent(iid, event); len(got) != 0 {
			t.Fatalf("mismatched result emitted events: %#v", got)
		}
	}
	svc.UpsertInstance(&state.InstanceRecord{InstanceID: "wrong", Online: true})
	if got := svc.ApplyAgentEvent("wrong", agentproto.Event{Kind: agentproto.EventThreadNameCommandResult, CommandID: command.CommandID, ThreadID: "thread-1", Name: command.Name}); len(got) != 0 {
		t.Fatal("wrong instance accepted")
	}
	if inst.Threads["thread-1"].Name != oldName {
		t.Fatal("unmatched result changed name")
	}
	result := agentproto.Event{Kind: agentproto.EventThreadNameCommandResult, CommandID: command.CommandID, ThreadID: "thread-1", Name: command.Name}
	page := renamePageForTest(svc.ApplyAgentEvent(iid, result))
	if page == nil || !page.Sealed || page.MessageID != "om-rename-owner" || page.Phase != "succeeded" {
		t.Fatalf("expected sealed original owner card: %#v", page)
	}
	if inst.Threads["thread-1"].Name != command.Name || inst.Threads["thread-2"].Name != "另一个会话" {
		t.Fatal("renamed wrong thread")
	}
	surfaceAfter, _ := json.Marshal(svc.root.Surfaces[sid])
	if string(surfaceBefore) != string(surfaceAfter) {
		t.Fatal("result changed surface route/queue/turn state")
	}
	result.Name = "duplicate must be ignored"
	if got := svc.ApplyAgentEvent(iid, result); len(got) != 0 || inst.Threads["thread-1"].Name != command.Name {
		t.Fatal("duplicate result changed name")
	}
}

func TestRenameFailuresSealOwnerAndKeepCachedName(t *testing.T) {
	for _, kind := range []string{"dispatch", "rejected", "result", "wrong name", "timeout", "late without tick", "disconnect", "degraded"} {
		t.Run(kind, func(t *testing.T) {
			svc, sid, iid := goalInterlockTestSetup(t)
			before := svc.root.Instances[iid].Threads["thread-1"].Name
			command := issueRenameForTest(t, svc, sid, "/rename 新名称")
			svc.RecordPageTrackingMessage(sid, command.CommandID, "owner-message")
			var events []eventcontract.Event
			switch kind {
			case "dispatch":
				events = svc.HandleCommandDispatchFailure(sid, command.CommandID, errors.New("send failed"))
			case "rejected":
				events = svc.HandleCommandRejected(iid, agentproto.CommandAck{CommandID: command.CommandID, Error: "unsupported"})
			case "result":
				events = svc.ApplyAgentEvent(iid, agentproto.Event{Kind: agentproto.EventThreadNameCommandResult, CommandID: command.CommandID, ThreadID: "thread-1", ErrorMessage: "denied"})
			case "wrong name":
				events = svc.ApplyAgentEvent(iid, agentproto.Event{Kind: agentproto.EventThreadNameCommandResult, CommandID: command.CommandID, ThreadID: "thread-1", Name: "unexpected"})
			case "timeout":
				events = svc.Tick(svc.now().Add(renameCommandTimeout))
			case "late without tick":
				later := svc.now().Add(renameCommandTimeout)
				svc.now = func() time.Time { return later }
				events = svc.ApplyAgentEvent(iid, agentproto.Event{Kind: agentproto.EventThreadNameCommandResult, CommandID: command.CommandID, ThreadID: "thread-1", Name: command.Name})
			case "disconnect":
				events = svc.ApplyInstanceDisconnected(iid)
			case "degraded":
				events = svc.ApplyInstanceTransportDegraded(iid, false)
			}
			page := renamePageForTest(events)
			if page == nil || !page.Sealed || page.Phase != "failed" || page.MessageID != "owner-message" || len(svc.renameCommands) != 0 {
				t.Fatalf("missing terminal failure: %#v", page)
			}
			if svc.root.Instances[iid].Threads["thread-1"].Name != before {
				t.Fatal("failure mutated name")
			}
		})
	}
}

func TestRenameDoesNotOverlapSameThread(t *testing.T) {
	svc, sid, _ := goalInterlockTestSetup(t)
	issueRenameForTest(t, svc, sid, "/rename first")
	events := svc.ApplySurfaceAction(control.Action{Kind: control.ActionRenameCommand, SurfaceSessionID: sid, Text: "/rename second"})
	if findAgentCommand(events, agentproto.CommandThreadNameSet) != nil || len(svc.renameCommands) != 1 {
		t.Fatal("overlapping rename issued")
	}
}

func TestRenameRemovedInstanceFailsOnNextTick(t *testing.T) {
	svc, sid, iid := goalInterlockTestSetup(t)
	command := issueRenameForTest(t, svc, sid, "/rename 新名称")
	svc.RecordPageTrackingMessage(sid, command.CommandID, "removed-owner")
	svc.RemoveInstance(iid)
	page := renamePageForTest(svc.Tick(svc.now()))
	if page == nil || !page.Sealed || page.Phase != "failed" || page.MessageID != "removed-owner" || len(svc.renameCommands) != 0 {
		t.Fatalf("removed instance must finish its owner card promptly: %#v", page)
	}
}

func TestRenameRemovedSurfaceCleansPendingOnTick(t *testing.T) {
	svc, sid, _ := goalInterlockTestSetup(t)
	issueRenameForTest(t, svc, sid, "/rename 新名称")
	delete(svc.root.Surfaces, sid)
	if events := svc.Tick(svc.now()); renamePageForTest(events) != nil || len(svc.renameCommands) != 0 {
		t.Fatal("removed surface left pending rename state")
	}
}

func TestRenameStampedCardUsesOriginalMessage(t *testing.T) {
	svc, sid, iid := goalInterlockTestSetup(t)
	action := control.Action{
		Kind: control.ActionRenameCommand, SurfaceSessionID: sid, Text: "/rename 新名称", MessageID: "menu-owner",
		Inbound: &control.ActionInboundMeta{CardDaemonLifecycleID: "current-lifecycle"},
	}
	resolved, ok := control.ResolveFeishuActionCatalog(svc.buildCatalogContext(svc.root.Surfaces[sid]), action)
	if !ok {
		t.Fatal("rename catalog not resolved")
	}
	events := svc.ApplySurfaceAction(resolved.Action)
	command := findAgentCommand(events, agentproto.CommandThreadNameSet)
	page := renamePageForTest(events)
	if command == nil || page == nil || page.MessageID != "menu-owner" || page.TrackingKey != "" || page.Sealed {
		t.Fatalf("menu handoff must replace its original card: %#v", page)
	}
	page = renamePageForTest(svc.ApplyAgentEvent(iid, agentproto.Event{
		Kind: agentproto.EventThreadNameCommandResult, CommandID: command.CommandID, ThreadID: command.Target.ThreadID, Name: command.Name,
	}))
	if page == nil || page.MessageID != "menu-owner" || !page.Sealed || page.Phase != "succeeded" {
		t.Fatalf("success must stay on original owner card: %#v", page)
	}
}

func TestRenameCardBudgetDoesNotTruncateNativeName(t *testing.T) {
	svc, sid, iid := goalInterlockTestSetup(t)
	name := strings.Repeat("中文名称", 1500)
	command := issueRenameForTest(t, svc, sid, "/rename "+name)
	if command.Name != name || svc.renameCommands[command.CommandID].Name != name {
		t.Fatal("display budget changed the native name")
	}
	page := renamePageForTest(svc.ApplyAgentEvent(iid, agentproto.Event{
		Kind: agentproto.EventThreadNameCommandResult, CommandID: command.CommandID, ThreadID: command.Target.ThreadID,
		ErrorMessage: strings.Repeat("服务返回错误", 2000),
	}))
	if page == nil {
		t.Fatal("missing error card")
	}
	encoded, err := json.Marshal(page)
	if err != nil || len(encoded) >= 30000 {
		t.Fatalf("rename view exceeds card budget: bytes=%d err=%v", len(encoded), err)
	}
	if len([]rune(page.BodySections[1].Lines[0])) > 300 || len([]rune(page.NoticeSections[0].Lines[0])) > 2000 {
		t.Fatal("rename card text exceeded display limits")
	}
}

func TestRenameLateFailureDoesNotFailUnrelatedQueuedTurn(t *testing.T) {
	svc, sid, iid := goalInterlockTestSetup(t)
	command := issueRenameForTest(t, svc, sid, "/rename 新名称")
	svc.Tick(svc.now().Add(renameCommandTimeout))
	surface := svc.root.Surfaces[sid]
	surface.ActiveQueueItemID = "other-dispatch"
	surface.QueueItems["other-dispatch"] = &state.QueueItemRecord{ID: "other-dispatch", Status: state.QueueItemDispatching}
	surface.QueuedQueueItemIDs = []string{"queued"}
	before, _ := json.Marshal(surface)
	if got := svc.HandleCommandDispatchFailure(sid, command.CommandID, errors.New("late send error")); len(got) != 0 {
		t.Fatal("late rename failure emitted unrelated feedback")
	}
	if got := svc.HandleCommandRejected(iid, agentproto.CommandAck{CommandID: command.CommandID, Error: "late reject"}); len(got) != 0 {
		t.Fatal("late rename rejection emitted unrelated feedback")
	}
	svc.BindPendingRemoteCommand(sid, command.CommandID)
	after, _ := json.Marshal(surface)
	if string(before) != string(after) {
		t.Fatal("late rename event mutated unrelated queue or turn")
	}
}

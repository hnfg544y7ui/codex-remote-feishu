package orchestrator

import (
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/core/threadcatalogcontract"
)

func TestBoundTopicPickerAllowsOnlyExistingThreadOptions(t *testing.T) {
	for _, source := range []control.TargetPickerRequestSource{control.TargetPickerRequestSourceUse, control.TargetPickerRequestSourceUseAll, control.TargetPickerRequestSourceList, control.TargetPickerRequestSourceWorkspace} {
		t.Run(string(source), func(t *testing.T) {
			svc, topic, _, cwd := topicOccupiedProjectPickerFixture(t)
			topic.FeishuTopicBoundThreadID, topic.SelectedThreadID = "free-thread", "free-thread"
			topic.ClaimedWorkspaceKey = cwd
			record, err := svc.newTargetPickerRecord(topic, source, targetPickerOpenOptions{AllowNewThread: true, PreferredWorkspaceKey: cwd})
			if err != nil || record.AllowNewThread {
				t.Fatalf("bound topic inherited source new-thread default: %#v, %v", record, err)
			}
			entry := workspaceSelectionEntry{workspaceKey: cwd, worktreeBase: true}
			options := svc.targetPickerSessionOptionsWithViews(topic, entry, source, true, svc.mergedThreadViews(topic))
			if len(options) == 0 {
				t.Fatal("existing conversation disappeared")
			}
			for _, option := range options {
				if option.Kind != control.FeishuTargetPickerSessionThread {
					t.Fatalf("bound topic exposed a create action: %#v", option)
				}
			}
			view, err := svc.buildTargetPickerView(topic, record)
			if err != nil || view.AllowNewThread || view.SelectedSessionValue != targetPickerThreadValue("free-thread") || !view.CanConfirm {
				t.Fatalf("bound topic should default to its existing conversation: %#v, %v", view, err)
			}
		})
	}
}

func TestBoundTopicProjectPickerExcludesCreationAndHandlesEmptyProject(t *testing.T) {
	svc, _, catalog, paths := projectPickerFixture(t)
	topic := topicTestCreate(t, svc, "om_bound_project")
	topic.FeishuTopicID, topic.FeishuTopicBoundThreadID = "omt_bound_project", "a"
	topic.SelectedThreadID, topic.ClaimedWorkspaceKey = "a", paths["a"]
	record, err := svc.newTargetPickerRecord(topic, control.TargetPickerRequestSourceUse, targetPickerOpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	record.SelectedProjectID = "p1"
	entries := svc.targetPickerWorkspaceEntriesForRecord(topic, record)
	for i := range entries {
		entries[i].worktreeBase = true
	}
	view, enabled, err := svc.buildDesktopProjectPickerView(topic, record, entries)
	if err != nil || !enabled || view.AllowNewThread || !view.CanConfirm {
		t.Fatalf("existing project picker unavailable: %#v, %v", view, err)
	}
	for _, option := range view.SessionOptions {
		if option.Kind != control.FeishuTargetPickerSessionThread {
			t.Fatalf("bound project exposed creation action: %#v", option)
		}
	}
	if kind, _ := parseTargetPickerSessionValue(view.SelectedSessionValue); kind != control.FeishuTargetPickerSessionThread {
		t.Fatalf("project default was not an existing thread: %#v", view)
	}
	catalog.snapshot.Projects = append(catalog.snapshot.Projects, threadcatalogcontract.DesktopProject{ID: "empty", Name: "空项目"})
	record.SelectedProjectID = "empty"
	view, _, err = svc.buildDesktopProjectPickerView(topic, record, entries)
	if err != nil || len(view.SessionOptions) != 0 || view.CanConfirm || view.SelectedSessionValue != "" {
		t.Fatalf("empty project retained actionable target: %#v, %v", view, err)
	}
	if len(view.Messages) == 0 || !strings.Contains(view.Messages[len(view.Messages)-1].Text, "请选择其他项目") || strings.Contains(view.Messages[len(view.Messages)-1].Text, "新建") {
		t.Fatalf("bound topic suggested creating a target: %#v", view.Messages)
	}
}

func TestBoundTopicPickerRejectsForgedCreationBeforeDispatch(t *testing.T) {
	for _, view := range []control.FeishuTargetPickerView{
		{Page: control.FeishuTargetPickerPageTarget, SelectedSessionValue: targetPickerNewThreadValue},
		{Page: control.FeishuTargetPickerPageTarget, SelectedSessionValue: targetPickerWorktreeCreateValue},
		{Page: control.FeishuTargetPickerPageLocalDirectory},
		{Page: control.FeishuTargetPickerPageGit},
		{Page: control.FeishuTargetPickerPageWorktree},
	} {
		t.Run(string(view.Page)+view.SelectedSessionValue, func(t *testing.T) {
			svc, topic, _, cwd := topicOccupiedProjectPickerFixture(t)
			topic.FeishuTopicBoundThreadID, topic.SelectedThreadID = "free-thread", "free-thread"
			topic.ClaimedWorkspaceKey = cwd
			view.SelectedWorkspaceKey = cwd
			events := svc.dispatchTargetPickerConfirmed(topic, nil, &activeTargetPickerRecord{}, view)
			if noticeByCode(events, "topic_conversation_pinned") == nil || topic.PendingHeadless != nil || topic.SelectedThreadID != "free-thread" || topic.FeishuTopicBoundThreadID != "free-thread" || topic.RouteMode == state.RouteModeNewThreadReady {
				t.Fatalf("forged create action changed bound topic: %#v / %#v", topic, events)
			}
		})
	}
}

func TestBoundTopicPickerCrossWorkspaceSwitchCompletesAfterWorkerConnects(t *testing.T) {
	svc, topic, oldInst, committer := topicSwitchFixture(t)
	oldInst.Managed = false
	cwd := state.ResolveWorkspaceClaimKey(t.TempDir())
	target := state.ThreadRecord{ThreadID: "c", Name: "其他目录已有会话", CWD: cwd, WorkspaceKey: cwd, Loaded: true}
	svc.SetPersistedThreadCatalog(&fakePersistedThreadCatalog{recent: []state.ThreadRecord{target}, byID: map[string]state.ThreadRecord{"c": target}})
	view := singleTargetPickerEvent(t, svc.openTargetPicker(topic, control.TargetPickerRequestSourceUse, "", nil, "", false))
	view = singleTargetPickerEvent(t, svc.handleTargetPickerSelectWorkspace(topic, view.PickerID, cwd, "owner", nil))
	view = singleTargetPickerEvent(t, svc.handleTargetPickerSelectSession(topic, view.PickerID, targetPickerThreadValue("c"), "owner", nil))
	if !view.CanConfirm || view.SelectedWorkspaceKey != cwd {
		t.Fatalf("existing target was not confirmable: %#v", view)
	}
	events := svc.handleTargetPickerConfirm(topic, view.PickerID, "owner", cwd, targetPickerThreadValue("c"), nil)
	pending := topic.PendingHeadless
	if pending == nil || pending.ThreadID != "c" || committer.calls != 0 {
		t.Fatalf("picker did not prepare candidate worker: pending=%#v commits=%d", pending, committer.calls)
	}
	assertTopicOriginalRoute(t, svc, topic, oldInst)
	processing := false
	for _, event := range events {
		if event.TargetPickerView != nil && event.TargetPickerView.PickerID == view.PickerID && event.TargetPickerView.Stage == control.FeishuTargetPickerStageProcessing {
			processing = true
		}
	}
	if !processing {
		t.Fatal("picker did not remain in processing during candidate launch")
	}
	svc.UpsertInstance(&state.InstanceRecord{InstanceID: pending.InstanceID, WorkspaceRoot: cwd, WorkspaceKey: cwd, Backend: agentproto.BackendCodex, Source: "headless", Managed: true, Threads: map[string]*state.ThreadRecord{"c": &target}})
	events = svc.ApplyInstanceConnected(pending.InstanceID)
	if topic.SelectedThreadID != "c" || topic.FeishuTopicBoundThreadID != "c" || topic.AttachedInstanceID != pending.InstanceID || topic.PendingHeadless != nil || committer.calls != 1 || committer.expected != "a" || committer.next.FeishuTopicBoundThreadID != "c" {
		t.Fatal("connected picker target did not commit route and persistence together")
	}
	completed := false
	for _, event := range events {
		if event.TargetPickerView != nil && event.TargetPickerView.PickerID == view.PickerID && event.TargetPickerView.Stage == control.FeishuTargetPickerStageSucceeded {
			completed = true
		}
	}
	if !completed || svc.activeTargetPicker(topic) != nil {
		t.Fatal("committed switch did not complete and release its original picker")
	}
}

func TestBoundTopicPickerRechecksIdleAtConfirmation(t *testing.T) {
	for _, reason := range []string{"queued", "native-turn"} {
		t.Run(reason, func(t *testing.T) {
			svc, topic, inst, committer := topicSwitchFixture(t)
			view := singleTargetPickerEvent(t, svc.openTargetPicker(topic, control.TargetPickerRequestSourceUse, "", nil, "", false))
			view = singleTargetPickerEvent(t, svc.handleTargetPickerSelectSession(topic, view.PickerID, targetPickerThreadValue("b"), "owner", nil))
			if !view.CanConfirm {
				t.Fatal("idle picker could not select an existing conversation")
			}
			if reason == "queued" {
				topic.QueuedQueueItemIDs = []string{"queued"}
				topic.QueueItems["queued"] = &state.QueueItemRecord{ID: "queued", Status: state.QueueItemQueued}
			} else {
				inst.ActiveThreadID, inst.ActiveTurnID = "a", "native-turn"
			}
			events := svc.handleTargetPickerConfirm(topic, view.PickerID, "owner", inst.WorkspaceKey, targetPickerThreadValue("b"), nil)
			assertTopicOriginalRoute(t, svc, topic, inst)
			if committer.calls != 0 || topic.PendingHeadless != nil {
				t.Fatal("busy confirmation started or persisted a switch")
			}
			failed := false
			for _, event := range events {
				if event.TargetPickerView != nil && event.TargetPickerView.Stage == control.FeishuTargetPickerStageFailed && strings.Contains(event.TargetPickerView.StatusText, "空闲") {
					failed = true
				}
			}
			if !failed {
				t.Fatal("busy confirmation did not return its failure on the picker card")
			}
		})
	}
}

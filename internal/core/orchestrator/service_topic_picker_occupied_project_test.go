package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func topicOccupiedProjectPickerFixture(t *testing.T) (*Service, *state.SurfaceConsoleRecord, *state.SurfaceConsoleRecord, string) {
	t.Helper()
	now := time.Now()
	svc := newServiceForTest(&now)
	svc.config.GitAvailable = false
	cwd := state.ResolveWorkspaceClaimKey(t.TempDir())
	inst := &state.InstanceRecord{InstanceID: "private-worker", WorkspaceRoot: cwd, WorkspaceKey: cwd, Online: true, Backend: agentproto.BackendCodex, Source: "headless", Managed: true,
		Threads: map[string]*state.ThreadRecord{
			"private-thread": {ThreadID: "private-thread", Name: "私聊会话", CWD: cwd, Loaded: true, LastUsedAt: now},
			"free-thread":    {ThreadID: "free-thread", Name: "未占用会话", CWD: cwd, Loaded: true, LastUsedAt: now},
		}}
	svc.UpsertInstance(inst)
	private := svc.ensureSurface(control.Action{SurfaceSessionID: "feishu:bot:user:owner", GatewayID: "bot", ChatID: "oc_private", ActorUserID: "owner"})
	if !svc.transitionSurfaceRouteCore(private, inst, surfaceRouteCoreState{AttachedInstanceID: inst.InstanceID, WorkspaceKey: cwd, RouteMode: state.RouteModePinned, SelectedThreadID: "private-thread", ThreadClaimPolicy: surfaceRouteThreadClaimKnown}) {
		t.Fatal("private fixture could not claim workspace/instance/thread")
	}
	topic := topicTestCreate(t, svc, "om_picker")
	topic.FeishuTopicID = "omt_picker"
	return svc, topic, private, cwd
}

func TestTopicPickerKeepsNonGitProjectAndNewSessionWithPrivateOwner(t *testing.T) {
	svc, topic, private, cwd := topicOccupiedProjectPickerFixture(t)
	events := svc.ApplySurfaceAction(control.Action{Kind: control.ActionShowThreads, SurfaceSessionID: topic.SurfaceSessionID, GatewayID: topic.GatewayID, ChatID: topic.ChatID, ActorUserID: "owner", Text: "/use"})
	view := singleTargetPickerEvent(t, events)
	if len(view.WorkspaceOptions) != 1 {
		t.Fatalf("occupied non-Git project disappeared: %#v", view.WorkspaceOptions)
	}
	if _, ok := targetPickerSessionOption(view, targetPickerNewThreadValue); !ok {
		t.Fatalf("new session disappeared: %#v", view.SessionOptions)
	}
	if _, ok := targetPickerSessionOption(view, targetPickerThreadValue("free-thread")); !ok {
		t.Fatalf("free conversation disappeared: %#v", view.SessionOptions)
	}
	if _, ok := targetPickerSessionOption(view, targetPickerThreadValue("private-thread")); ok {
		t.Fatal("private conversation should remain exclusively owned")
	}
	if private.SelectedThreadID != "private-thread" || private.ClaimedWorkspaceKey != cwd {
		t.Fatal("opening topic picker changed private context")
	}
	record := svc.activeTargetPicker(topic)
	svc.handleTargetPickerConfirm(topic, record.PickerID, "owner", cwd, targetPickerNewThreadValue, nil)
	if topic.PendingHeadless == nil || !topic.PendingHeadless.PrepareNewThread {
		t.Fatal("new session must allocate independent worker when only worker is claimed")
	}
	if svc.instanceClaimSurface("private-worker") != private {
		t.Fatal("topic stole private worker")
	}
}

func TestTopicFreeConversationUsesIndependentWorkerInOccupiedProject(t *testing.T) {
	svc, topic, private, _ := topicOccupiedProjectPickerFixture(t)
	target := svc.resolveThreadTargetWithScope(topic, "free-thread", true)
	if target.Mode != threadAttachCreateHeadless {
		t.Fatalf("free conversation should allocate a separate worker, got %s %s", target.Mode, target.NoticeCode)
	}
	events := svc.useThread(topic, "free-thread", true)
	if topic.PendingHeadless == nil || topic.PendingHeadless.ThreadID != "free-thread" {
		t.Fatalf("free conversation attach did not start independent context: %#v", events)
	}
	if private.SelectedThreadID != "private-thread" || svc.instanceClaimSurface("private-worker") != private {
		t.Fatal("private attachment changed")
	}
}

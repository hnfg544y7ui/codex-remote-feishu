package orchestrator

import (
	"errors"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type recordingTopicRebindCommitter struct {
	calls    int
	err      error
	expected string
	next     state.SurfaceConsoleRecord
}

func (r *recordingTopicRebindCommitter) CommitTopicThreadRebind(expected string, next *state.SurfaceConsoleRecord) error {
	r.calls++
	r.expected, r.next = expected, *next
	return r.err
}

func topicSwitchFixture(t *testing.T) (*Service, *state.SurfaceConsoleRecord, *state.InstanceRecord, *recordingTopicRebindCommitter) {
	t.Helper()
	svc := topicTestService()
	cwd := state.ResolveWorkspaceClaimKey(t.TempDir())
	inst := &state.InstanceRecord{InstanceID: "switch-worker", WorkspaceRoot: cwd, WorkspaceKey: cwd, Online: true, Source: "headless", Managed: true,
		Backend: agentproto.BackendCodex, Threads: map[string]*state.ThreadRecord{
			"a": {ThreadID: "a", Name: "原会话", CWD: cwd, Loaded: true},
			"b": {ThreadID: "b", Name: "新会话", CWD: cwd, Loaded: true},
		}}
	svc.UpsertInstance(inst)
	topic := topicTestCreate(t, svc, "om_switch")
	topic.FeishuTopicID, topic.FeishuTopicMessageURL = "omt_switch", "https://example.test/topic"
	if !svc.transitionSurfaceRouteCore(topic, inst, surfaceRouteCoreState{AttachedInstanceID: inst.InstanceID, WorkspaceKey: cwd, RouteMode: state.RouteModePinned, SelectedThreadID: "a", ThreadClaimPolicy: surfaceRouteThreadClaimKnown}) {
		t.Fatal("could not bind fixture topic")
	}
	committer := &recordingTopicRebindCommitter{}
	svc.SetTopicThreadRebindCommitter(committer)
	return svc, topic, inst, committer
}

func assertTopicOriginalRoute(t *testing.T, svc *Service, topic *state.SurfaceConsoleRecord, inst *state.InstanceRecord) {
	t.Helper()
	if topic.SelectedThreadID != "a" || topic.FeishuTopicBoundThreadID != "a" || topic.AttachedInstanceID != inst.InstanceID || topic.RouteMode != state.RouteModePinned || topic.ClaimedWorkspaceKey != inst.WorkspaceKey {
		t.Fatalf("original topic route changed: selected=%q bound=%q attached=%q route=%q workspace=%q; expected instance=%q workspace=%q", topic.SelectedThreadID, topic.FeishuTopicBoundThreadID, topic.AttachedInstanceID, topic.RouteMode, topic.ClaimedWorkspaceKey, inst.InstanceID, inst.WorkspaceKey)
	}
	if svc.threadClaimSurface("a") != topic || svc.instanceClaimSurface(inst.InstanceID) != topic {
		t.Fatal("original thread/instance claim was released")
	}
}

func TestFeishuTopicIdleSwitchCommitsTargetAndKeepsRoutingIdentity(t *testing.T) {
	svc, topic, inst, committer := topicSwitchFixture(t)
	sibling := topicTestCreate(t, svc, "om_sibling")
	sibling.FeishuTopicID, sibling.FeishuTopicBoundThreadID, sibling.SelectedThreadID = "omt_sibling", "sibling-thread", "sibling-thread"
	group := svc.root.Surfaces[topicTestGroupAction("unused").SurfaceSessionID]
	group.SelectedThreadID = "main-group-thread"
	events := svc.useThread(topic, "b", true)
	if topic.SelectedThreadID != "b" || topic.FeishuTopicBoundThreadID != "b" || topic.AttachedInstanceID != inst.InstanceID || svc.topicThreadSwitch(topic) != nil {
		t.Fatalf("idle switch did not complete: %#v / %#v", topic, events)
	}
	if committer.calls != 1 || committer.expected != "a" || committer.next.SelectedThreadID != "b" || committer.next.FeishuTopicBoundThreadID != "b" || committer.next.PendingHeadless != nil {
		t.Fatalf("wrong persisted candidate: %#v", committer)
	}
	if svc.threadClaimSurface("a") != nil || svc.threadClaimSurface("b") != topic || svc.instanceClaimSurface(inst.InstanceID) != topic {
		t.Fatal("successful switch left stale or missing claims")
	}
	if topic.FeishuTopicID != "omt_switch" || topic.FeishuTopicRootMessageID != "om_switch" || topic.FeishuTopicOwnerID != "owner" || topic.FeishuTopicMessageURL != "https://example.test/topic" || topic.FeishuTopicClosed {
		t.Fatal("conversation switch changed native topic identity")
	}
	if sibling.SelectedThreadID != "sibling-thread" || sibling.FeishuTopicBoundThreadID != "sibling-thread" || group.SelectedThreadID != "main-group-thread" {
		t.Fatal("switch changed sibling or main group context")
	}
}

func TestFeishuTopicSwitchRefusesUnfinishedWorkWithoutClearingInputs(t *testing.T) {
	for _, reason := range []string{"running", "queued", "approval", "capture", "text", "file", "image", "local-turn", "local-status", "pending-launch"} {
		t.Run(reason, func(t *testing.T) {
			svc, topic, inst, committer := topicSwitchFixture(t)
			switch reason {
			case "running":
				topic.ActiveQueueItemID = "running"
				topic.QueueItems["running"] = &state.QueueItemRecord{ID: "running", Status: state.QueueItemRunning}
			case "queued":
				topic.QueuedQueueItemIDs = []string{"queued"}
				topic.QueueItems["queued"] = &state.QueueItemRecord{ID: "queued", Status: state.QueueItemQueued}
			case "approval":
				topic.PendingRequests["approval"] = &state.RequestPromptRecord{RequestID: "approval", RequestType: "approval"}
			case "capture":
				topic.ActiveRequestCapture = &state.RequestCaptureRecord{}
			case "text":
				topic.PendingTextInput = &state.PendingTextInputRecord{Text: "pending input"}
			case "file":
				topic.StagedFiles["file"] = &state.StagedFileRecord{FileID: "file"}
			case "image":
				topic.StagedImages["image"] = &state.StagedImageRecord{ImageID: "image"}
			case "local-turn":
				inst.ActiveThreadID, inst.ActiveTurnID = "a", "app-turn"
			case "local-status":
				inst.Threads["a"].RuntimeStatus = &agentproto.ThreadRuntimeStatus{Type: agentproto.ThreadRuntimeStatusTypeActive}
			case "pending-launch":
				topic.PendingHeadless = &state.HeadlessLaunchRecord{InstanceID: "restore-a", ThreadID: "a"}
			}
			oldText, oldCapture, oldPending := topic.PendingTextInput, topic.ActiveRequestCapture, topic.PendingHeadless
			oldFiles, oldImages, oldRequests, oldQueued := len(topic.StagedFiles), len(topic.StagedImages), len(topic.PendingRequests), len(topic.QueuedQueueItemIDs)
			events := svc.useThread(topic, "b", true)
			if noticeByCode(events, "topic_switch_busy") == nil || committer.calls != 0 || svc.topicThreadSwitch(topic) != nil {
				t.Fatalf("unfinished work admitted a switch: %#v", events)
			}
			assertTopicOriginalRoute(t, svc, topic, inst)
			if topic.PendingTextInput != oldText || topic.ActiveRequestCapture != oldCapture || topic.PendingHeadless != oldPending || len(topic.StagedFiles) != oldFiles || len(topic.StagedImages) != oldImages || len(topic.PendingRequests) != oldRequests || len(topic.QueuedQueueItemIDs) != oldQueued {
				t.Fatal("rejected switch consumed unfinished input")
			}
		})
	}
}

func TestFeishuTopicSwitchFailureKeepsCommittedRouteAndClaims(t *testing.T) {
	for _, reason := range []string{"missing", "busy", "persist"} {
		t.Run(reason, func(t *testing.T) {
			svc, topic, inst, committer := topicSwitchFixture(t)
			target := "b"
			if reason == "missing" {
				target = "missing-thread"
			} else if reason == "busy" {
				other := svc.ensureSurface(control.Action{SurfaceSessionID: "other", ActorUserID: "other"})
				other.AttachedInstanceID, other.SelectedThreadID = inst.InstanceID, "b"
				other.ActiveQueueItemID = "running"
				other.QueueItems["running"] = &state.QueueItemRecord{ID: "running", Status: state.QueueItemRunning}
				svc.bindThreadClaim(other, inst.InstanceID, "b")
			} else {
				committer.err = errors.New("test persistence refused")
			}
			events := svc.useThread(topic, target, true)
			assertTopicOriginalRoute(t, svc, topic, inst)
			if svc.topicThreadSwitch(topic) != nil {
				t.Fatal("failed switch left a reusable authorization intent")
			}
			if reason == "persist" {
				if committer.calls != 1 || noticeByCode(events, "topic_switch_failed") == nil || svc.threadClaimSurface("b") != nil {
					t.Fatalf("persistence error was not surfaced safely: %#v", events)
				}
			} else if committer.calls != 0 {
				t.Fatal("unavailable target reached persistence")
			}
		})
	}
}

func TestFeishuTopicSwitchAsyncLaunchKeepsOldRouteUntilCommit(t *testing.T) {
	for _, result := range []string{"launch-failed", "timeout", "connected", "persist-failed"} {
		t.Run(result, func(t *testing.T) {
			svc, topic, oldInst, committer := topicSwitchFixture(t)
			// Even a managed old worker must retain its workspace until commit;
			// a cross-workspace target needs an independent candidate worker.
			oldRoot, oldKey, oldName := oldInst.WorkspaceRoot, oldInst.WorkspaceKey, oldInst.DisplayName
			cwd := state.ResolveWorkspaceClaimKey(t.TempDir())
			target := state.ThreadRecord{ThreadID: "c", Name: "其他目录已有会话", CWD: cwd, WorkspaceKey: cwd, Loaded: true}
			svc.SetPersistedThreadCatalog(&fakePersistedThreadCatalog{recent: []state.ThreadRecord{target}, byID: map[string]state.ThreadRecord{"c": target}})
			events := svc.useThread(topic, "c", true)
			pending := topic.PendingHeadless
			if pending == nil || pending.ThreadID != "c" || pending.WorkspaceKey != cwd || svc.topicThreadSwitch(topic) == nil || committer.calls != 0 {
				t.Fatalf("switch did not prepare independent launch: %#v / %#v", pending, events)
			}
			oldAdmission := &state.CodexAdmissionRef{ProfileRef: state.CodexProfileRef{ID: state.NativeCodexProfileID, Revision: 1}, ContextPreferenceRef: state.CodexContextPreferenceRef{ProfileID: state.NativeCodexProfileID, Revision: 1}}
			oldConnection := &state.CodexConnectionContract{ConnectionContractID: "old-connection", ConnectionGeneration: 1}
			oldPolicy := &state.CodexThreadPolicy{ThreadPolicyID: "old-policy"}
			topic.CodexAdmissionRef, topic.CodexConnectionContract, topic.CodexThreadPolicy = oldAdmission, oldConnection, oldPolicy
			pending.CodexAdmissionRef = &state.CodexAdmissionRef{ProfileRef: state.CodexProfileRef{ID: state.NativeCodexProfileID, Revision: 2}, ContextPreferenceRef: state.CodexContextPreferenceRef{ProfileID: state.NativeCodexProfileID, Revision: 2}}
			pending.CodexConnectionContract = &state.CodexConnectionContract{ConnectionContractID: "candidate-connection", ConnectionGeneration: 2}
			pending.CodexThreadPolicy = &state.CodexThreadPolicy{ThreadPolicyID: "candidate-policy"}
			assertTopicOriginalRoute(t, svc, topic, oldInst)
			if result == "launch-failed" {
				svc.HandleHeadlessLaunchFailed(topic.SurfaceSessionID, pending.InstanceID, errors.New("test launch failure"))
			} else if result == "timeout" {
				svc.expirePendingHeadless(topic, pending)
			} else {
				if result == "persist-failed" {
					committer.err = errors.New("test persistence failed after launch")
				}
				svc.UpsertInstance(&state.InstanceRecord{InstanceID: pending.InstanceID, WorkspaceRoot: cwd, WorkspaceKey: cwd, Backend: agentproto.BackendCodex, Source: "headless", Managed: true, Threads: map[string]*state.ThreadRecord{"c": &target}})
				svc.ApplyInstanceConnected(pending.InstanceID)
			}
			if result != "connected" {
				assertTopicOriginalRoute(t, svc, topic, oldInst)
				if topic.CodexAdmissionRef != oldAdmission || topic.CodexConnectionContract != oldConnection || topic.CodexThreadPolicy != oldPolicy {
					t.Fatal("failed asynchronous switch changed original runtime references")
				}
				if topic.PendingHeadless != nil || svc.topicThreadSwitch(topic) != nil || svc.threadClaimSurface("c") != nil {
					t.Fatal("failed asynchronous switch retained pending target or authority")
				}
				if oldInst.WorkspaceRoot != oldRoot || oldInst.WorkspaceKey != oldKey || oldInst.DisplayName != oldName {
					t.Fatal("failed switch changed the old managed worker's workspace")
				}
				return
			}
			if topic.SelectedThreadID != "c" || topic.FeishuTopicBoundThreadID != "c" || topic.AttachedInstanceID != pending.InstanceID || topic.ClaimedWorkspaceKey != cwd || topic.PendingHeadless != nil || svc.topicThreadSwitch(topic) != nil || committer.calls != 1 {
				t.Fatalf("connected target did not commit atomically: %#v", topic)
			}
			if topic.CodexAdmissionRef == nil || topic.CodexAdmissionRef.ProfileRef.Revision != 2 || topic.CodexConnectionContract == nil || topic.CodexConnectionContract.ConnectionContractID != "candidate-connection" || topic.CodexThreadPolicy == nil || topic.CodexThreadPolicy.ThreadPolicyID != "candidate-policy" {
				t.Fatal("committed asynchronous switch did not adopt the candidate runtime references")
			}
			if svc.threadClaimSurface("a") != nil || svc.threadClaimSurface("c") != topic || svc.instanceClaimSurface(oldInst.InstanceID) != nil || svc.instanceClaimSurface(pending.InstanceID) != topic {
				t.Fatal("committed asynchronous switch leaked old or lost new claims")
			}
		})
	}
}
func TestFeishuTopicOldSelectionKeepsPendingSwitch(t *testing.T) {
	svc, topic, old, _ := topicSwitchFixture(t)
	old.Managed = false
	cwd := state.ResolveWorkspaceClaimKey(t.TempDir())
	target := state.ThreadRecord{ThreadID: "c", Name: "candidate", CWD: cwd, WorkspaceKey: cwd, Loaded: true}
	svc.SetPersistedThreadCatalog(&fakePersistedThreadCatalog{recent: []state.ThreadRecord{target}, byID: map[string]state.ThreadRecord{"c": target}})
	svc.useThread(topic, "c", true)
	pending := topic.PendingHeadless
	if pending == nil {
		t.Fatal("fixture did not launch")
	}
	events := svc.useThread(topic, "a", true)
	if topic.PendingHeadless != pending || svc.topicThreadSwitch(topic) == nil {
		t.Fatalf("same old selection consumed in-flight switch: pending=%v intent=%v events=%#v", topic.PendingHeadless, svc.topicThreadSwitch(topic), events)
	}
}
func TestFeishuTopicFailedSwitchKeepsAutoContinueEpisode(t *testing.T) {
	svc, topic, _, committer := topicSwitchFixture(t)
	episode := &state.PendingAutoContinueEpisodeRecord{EpisodeID: "old-episode", State: state.AutoContinueEpisodeFailed}
	topic.AutoContinue.Episode = episode
	committer.err = errors.New("disk refused")
	svc.useThread(topic, "b", true)
	if topic.AutoContinue.Episode != episode {
		t.Fatal("failed CAS cleared old AutoContinue episode")
	}
}

func TestFeishuTopicOldDisconnectCancelsCandidateAndRejectsLateConnect(t *testing.T) {
	svc, topic, old, committer := topicSwitchFixture(t)
	cwd := state.ResolveWorkspaceClaimKey(t.TempDir())
	target := state.ThreadRecord{ThreadID: "c", Name: "candidate", CWD: cwd, WorkspaceKey: cwd, Loaded: true}
	svc.SetPersistedThreadCatalog(&fakePersistedThreadCatalog{recent: []state.ThreadRecord{target}, byID: map[string]state.ThreadRecord{"c": target}})
	svc.useThread(topic, "c", true)
	pending := topic.PendingHeadless
	if pending == nil {
		t.Fatal("fixture did not launch")
	}
	events := svc.ApplyInstanceDisconnected(old.InstanceID)
	killed := false
	for _, event := range events {
		if event.DaemonCommand != nil && event.DaemonCommand.Kind == control.DaemonCommandKillHeadless && event.DaemonCommand.InstanceID == pending.InstanceID {
			killed = true
		}
	}
	if !killed || topic.PendingHeadless != nil || svc.topicThreadSwitch(topic) != nil || topic.FeishuTopicBoundThreadID != "a" {
		t.Fatal("disconnect did not cancel candidate while retaining committed binding")
	}
	svc.UpsertInstance(&state.InstanceRecord{InstanceID: pending.InstanceID, WorkspaceRoot: cwd, WorkspaceKey: cwd, Backend: agentproto.BackendCodex, Source: "headless", Managed: true, Threads: map[string]*state.ThreadRecord{"c": &target}})
	svc.ApplyInstanceConnected(pending.InstanceID)
	if topic.FeishuTopicBoundThreadID != "a" || committer.calls != 0 || svc.threadClaimSurface("c") != nil {
		t.Fatal("late candidate connected after the switch was cancelled")
	}
}

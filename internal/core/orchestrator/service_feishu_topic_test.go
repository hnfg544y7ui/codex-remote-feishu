package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

func topicTestService() *Service {
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	return newServiceForTest(&now)
}

func topicTestGroupAction(rootID string) control.Action {
	ref := feishuidentity.SurfaceRef{Platform: "feishu", GatewayID: "bot", ScopeKind: "chat", ScopeID: "oc_test"}
	return control.Action{Kind: control.ActionTopicCommand, GatewayID: ref.GatewayID, SurfaceSessionID: ref.SurfaceID(), ChatID: ref.ScopeID, ActorUserID: "owner", MessageID: rootID, Text: "/topic new 开发任务", Value: "new 开发任务"}
}

func topicTestCreate(t *testing.T, svc *Service, rootID string) *state.SurfaceConsoleRecord {
	t.Helper()
	events := svc.ApplySurfaceAction(topicTestGroupAction(rootID))
	if noticeByCode(events, "topic_created") == nil {
		t.Fatalf("topic creation failed: %#v", events)
	}
	topic := svc.root.Surfaces[feishuidentity.TopicRef("bot", "oc_test", rootID).SurfaceID()]
	if topic == nil {
		t.Fatal("topic surface missing")
	}
	return topic
}

func TestFeishuTopicCreatesIndependentSurfaceAndWelcome(t *testing.T) {
	svc := topicTestService()
	group := svc.ensureSurface(topicTestGroupAction("om_one"))
	group.SelectedThreadID = "group-thread"
	group.ClaimedWorkspaceKey = "/group-workspace"
	group.QueuedQueueItemIDs = []string{"group-queue"}
	group.QueueItems["group-queue"] = &state.QueueItemRecord{ID: "group-queue", Status: state.QueueItemQueued}
	events := svc.ApplySurfaceAction(topicTestGroupAction("om_one"))
	topicID := feishuidentity.TopicRef("bot", "oc_test", "om_one").SurfaceID()
	topic := svc.root.Surfaces[topicID]
	if topic == nil || topic.FeishuTopicRootMessageID != "om_one" || topic.FeishuTopicOwnerID != "owner" || topic.FeishuTopicTitle != "开发任务" {
		t.Fatalf("topic metadata: %#v", topic)
	}
	if topic.SelectedThreadID != "" || topic.ClaimedWorkspaceKey != "" || topic.AttachedInstanceID != "" || len(topic.QueueItems) != 0 || len(topic.QueuedQueueItemIDs) != 0 {
		t.Fatalf("topic inherited room conversation, workspace or queue: %#v", topic)
	}
	created := noticeByCode(events, "topic_created")
	if created == nil {
		t.Fatalf("missing welcome: %#v", events)
	}
	for _, event := range events {
		if event.Notice == created && event.SurfaceSessionID != topicID {
			t.Fatalf("welcome was sent to group surface %q", event.SurfaceSessionID)
		}
	}
	second := topicTestCreate(t, svc, "om_two")
	second.QueueItems["second-only"] = &state.QueueItemRecord{ID: "second-only", Status: state.QueueItemQueued}
	if _, shared := topic.QueueItems["second-only"]; shared || group.SelectedThreadID != "group-thread" || group.ClaimedWorkspaceKey != "/group-workspace" {
		t.Fatal("topic creation or queue mutation changed its sibling/group")
	}
}

func TestFeishuTopicDuplicateRootIsIdempotent(t *testing.T) {
	svc := topicTestService()
	topic := topicTestCreate(t, svc, "om_same")
	topic.FeishuTopicID = "omt_native"
	topic.FeishuTopicBoundThreadID = "thread-original"
	topic.SelectedThreadID = "thread-original"
	count := len(svc.root.Surfaces)
	action := topicTestGroupAction("om_same")
	action.Value = "new Changed label"
	events := svc.ApplySurfaceAction(action)
	if noticeByCode(events, "topic_created") == nil || len(svc.root.Surfaces) != count || topic.FeishuTopicTitle != "开发任务" || topic.FeishuTopicID != "omt_native" || topic.FeishuTopicBoundThreadID != "thread-original" {
		t.Fatal("repeated root created/reset topic state")
	}
}

func TestFeishuTopicCreationRequiresFreshMainGroupText(t *testing.T) {
	for _, mode := range []string{"private", "card", "inside-topic", "missing-root", "missing-actor"} {
		t.Run(mode, func(t *testing.T) {
			svc := topicTestService()
			action := topicTestGroupAction("om_new")
			want := "topic_new_root_required"
			switch mode {
			case "private":
				action.SurfaceSessionID = (feishuidentity.SurfaceRef{Platform: "feishu", GatewayID: "bot", ScopeKind: "user", ScopeID: "owner"}).SurfaceID()
				want = "topic_group_required"
			case "card":
				action.Inbound = &control.ActionInboundMeta{CardDaemonLifecycleID: "current"}
			case "inside-topic":
				topic := topicTestCreate(t, svc, "om_existing")
				action.SurfaceSessionID = topic.SurfaceSessionID
			case "missing-root":
				action.MessageID = ""
			case "missing-actor":
				action.ActorUserID = ""
			}
			surface := svc.ensureSurface(action)
			before := len(svc.root.Surfaces)
			events := svc.handleTopicCommand(surface, action)
			if noticeByCode(events, want) == nil || len(svc.root.Surfaces) != before {
				t.Fatalf("unsafe creation mode %s accepted: %#v", mode, events)
			}
		})
	}
}

func TestFeishuTopicRejectsCrossOwnerAndRouteMetadata(t *testing.T) {
	for _, mismatch := range []string{"owner", "missing-owner", "chat", "gateway", "root", "native"} {
		t.Run(mismatch, func(t *testing.T) {
			svc := topicTestService()
			topic := topicTestCreate(t, svc, "om_secure")
			topic.FeishuTopicID = "omt_secure"
			action := control.Action{Kind: control.ActionTextMessage, GatewayID: "bot", SurfaceSessionID: topic.SurfaceSessionID, ChatID: "oc_test", ActorUserID: "owner", FeishuTopicID: "omt_secure", FeishuTopicRootMessageID: "om_secure", Text: "run"}
			want := "topic_route_mismatch"
			switch mismatch {
			case "owner":
				action.ActorUserID = "another-member"
				want = "topic_owner_required"
			case "missing-owner":
				action.ActorUserID = ""
				want = "topic_owner_required"
			case "chat":
				action.ChatID = "oc_forwarded"
			case "gateway":
				action.GatewayID = "another-bot"
			case "root":
				action.FeishuTopicRootMessageID = "om_other"
			case "native":
				action.FeishuTopicID = "omt_other"
			}
			events := svc.ApplySurfaceAction(action)
			if noticeByCode(events, want) == nil || topic.GatewayID != "bot" || topic.ChatID != "oc_test" || topic.FeishuTopicOwnerID != "owner" || topic.FeishuTopicID != "omt_secure" || len(topic.QueueItems) != 0 {
				t.Fatalf("mismatched route mutated topic: %#v, events %#v", topic, events)
			}
		})
	}
}

func TestFeishuTopicPinnedConversationBlocksUseNewAndCoreRetarget(t *testing.T) {
	svc := topicTestService()
	topic := topicTestCreate(t, svc, "om_pinned")
	topic.FeishuTopicID = "omt_pinned"
	topic.FeishuTopicBoundThreadID = "original"
	topic.SelectedThreadID = "original"
	if events := svc.useThread(topic, "other", true); noticeByCode(events, "topic_conversation_pinned") == nil {
		t.Fatalf("pinned use accepted: %#v", events)
	}
	for _, kind := range []control.ActionKind{control.ActionNewThread, control.ActionDetach, control.ActionWorkspaceDetach, control.ActionFollowLocal, control.ActionModeCommand} {
		events := svc.ApplySurfaceAction(control.Action{Kind: kind, SurfaceSessionID: topic.SurfaceSessionID, GatewayID: "bot", ChatID: "oc_test", ActorUserID: "owner"})
		if noticeByCode(events, "topic_conversation_pinned") == nil {
			t.Fatalf("pinned action %s accepted: %#v", kind, events)
		}
	}
	for _, next := range []surfaceRouteCoreState{{SelectedThreadID: "other"}, {RouteMode: state.RouteModeNewThreadReady}} {
		if svc.transitionSurfaceRouteCore(topic, nil, next) || topic.SelectedThreadID != "original" || topic.FeishuTopicBoundThreadID != "original" {
			t.Fatal("core transition retargeted a pinned conversation")
		}
	}
	svc.bindThreadClaim(topic, "instance", "other")
	if topic.FeishuTopicBoundThreadID != "original" || svc.threadClaims["other"] != nil {
		t.Fatal("direct thread claim bypassed topic pin")
	}
}

func TestFeishuTopicPendingWelcomeDoesNotAcceptTaskOrPicker(t *testing.T) {
	svc := topicTestService()
	topic := topicTestCreate(t, svc, "om_pending")
	for _, kind := range []control.ActionKind{control.ActionTextMessage, control.ActionUseThread, control.ActionNewThread, control.ActionTopicCommand} {
		action := control.Action{Kind: kind, SurfaceSessionID: topic.SurfaceSessionID, GatewayID: "bot", ChatID: "oc_test", ActorUserID: "owner", Text: "task", ThreadID: "target"}
		events := svc.ApplySurfaceAction(action)
		if noticeByCode(events, "topic_starting") == nil || len(topic.QueueItems) != 0 || topic.FeishuTopicBoundThreadID != "" || topic.FeishuTopicID != "" {
			t.Fatalf("pending welcome admitted %s: %#v", kind, events)
		}
	}
	unknown := svc.ensureSurface(control.Action{SurfaceSessionID: feishuidentity.TopicRef("bot", "oc_test", "om_unknown").SurfaceID(), GatewayID: "bot", ChatID: "oc_test", ActorUserID: "owner"})
	if events := svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: unknown.SurfaceSessionID, GatewayID: "bot", ChatID: "oc_test", ActorUserID: "owner", Text: "task"}); noticeByCode(events, "topic_unknown") == nil || len(unknown.QueueItems) != 0 {
		t.Fatalf("unregistered topic admitted task: %#v", events)
	}
}

func TestFeishuTopicCloseRefusesBusyAndRetainsIdleTombstone(t *testing.T) {
	for _, busy := range []string{"queued", "running", "headless", "capture", "approval", "idle"} {
		t.Run(busy, func(t *testing.T) {
			svc := topicTestService()
			topic := topicTestCreate(t, svc, "om_close")
			topic.FeishuTopicBoundThreadID = "original"
			topic.SelectedThreadID = "original"
			switch busy {
			case "queued":
				topic.QueuedQueueItemIDs = []string{"queue"}
			case "running":
				topic.ActiveQueueItemID = "queue"
				topic.QueueItems["queue"] = &state.QueueItemRecord{ID: "queue", Status: state.QueueItemRunning}
			case "headless":
				topic.PendingHeadless = &state.HeadlessLaunchRecord{}
			case "capture":
				topic.ActiveRequestCapture = &state.RequestCaptureRecord{}
			case "approval":
				topic.PendingRequests["approval"] = &state.RequestPromptRecord{RequestID: "approval", RequestType: "approval"}
				topic.PendingRequestOrder = []string{"approval"}
			}
			action := control.Action{Kind: control.ActionTopicCommand, SurfaceSessionID: topic.SurfaceSessionID, GatewayID: "bot", ChatID: "oc_test", ActorUserID: "owner", Text: "/topic close", Value: "close"}
			events := svc.handleTopicCommand(topic, action)
			if busy != "idle" {
				if noticeByCode(events, "topic_busy") == nil || topic.FeishuTopicClosed || topic.SelectedThreadID != "original" {
					t.Fatalf("busy close altered route: %#v", events)
				}
				return
			}
			if noticeByCode(events, "topic_closed") == nil || !topic.FeishuTopicClosed || topic.FeishuTopicBoundThreadID != "original" || svc.root.Surfaces[topic.SurfaceSessionID] != topic {
				t.Fatal("idle close did not retain bound tombstone")
			}
			later := svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: topic.SurfaceSessionID, GatewayID: "bot", ChatID: "oc_test", ActorUserID: "owner", Text: "new task"})
			if noticeByCode(later, "topic_closed") == nil || len(topic.QueueItems) != 0 {
				t.Fatalf("closed topic accepted a later task: %#v", later)
			}
			duplicate := svc.ApplySurfaceAction(topicTestGroupAction("om_close"))
			if noticeByCode(duplicate, "topic_closed") == nil || !topic.FeishuTopicClosed {
				t.Fatal("duplicate root reopened closed topic")
			}
		})
	}
}

func TestFeishuTopicRejectedActorCannotReplaceCurrentActor(t *testing.T) {
	svc := topicTestService()
	topic := topicTestCreate(t, svc, "om_acl")
	topic.FeishuTopicID = "omt_acl"
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: topic.SurfaceSessionID, GatewayID: topic.GatewayID, ChatID: topic.ChatID, ActorUserID: "another-user", Text: "task"})
	if topic.ActorUserID != "owner" {
		t.Fatal("rejected actor replaced topic actor")
	}
}

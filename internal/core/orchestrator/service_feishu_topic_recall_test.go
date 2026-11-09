package orchestrator

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestFeishuTopicRecallWithoutTaskActorCancelsQueuedMessage(t *testing.T) {
	for _, actor := range []string{"", "recall-operator"} {
		t.Run("actor="+actor, func(t *testing.T) {
			svc := topicTestService()
			topic := topicTestCreate(t, svc, "om_recall")
			topic.FeishuTopicID = "omt_recall"
			topic.QueueItems["queued"] = &state.QueueItemRecord{ID: "queued", SourceMessageID: "message", Status: state.QueueItemQueued}
			topic.QueuedQueueItemIDs = []string{"queued"}
			action := control.Action{Kind: control.ActionMessageRecalled, SurfaceSessionID: topic.SurfaceSessionID,
				GatewayID: "bot", ChatID: "oc_test", ActorUserID: actor, TargetMessageID: "message"}
			if events := svc.ValidateFeishuTopicAction(action); len(events) != 0 {
				t.Fatalf("platform recall was rejected before dispatch: %#v", events)
			}
			events := svc.ApplySurfaceAction(action)
			if noticeByCode(events, "topic_owner_required") != nil || topic.QueueItems["queued"].Status != state.QueueItemDiscarded || len(topic.QueuedQueueItemIDs) != 0 {
				t.Fatalf("recall did not discard its queued message: %#v, events %#v", topic.QueueItems["queued"], events)
			}
			if topic.ActorUserID != "owner" || topic.FeishuTopicOwnerID != "owner" {
				t.Fatal("platform recall replaced the task actor or topic owner")
			}
		})
	}
}

func TestFeishuTopicRecallKeepsRouteAndClosedTopicGuards(t *testing.T) {
	for _, mismatch := range []string{"gateway", "chat", "root", "native", "closed"} {
		t.Run(mismatch, func(t *testing.T) {
			svc := topicTestService()
			topic := topicTestCreate(t, svc, "om_secure_recall")
			topic.FeishuTopicID = "omt_secure_recall"
			topic.QueueItems["queued"] = &state.QueueItemRecord{ID: "queued", SourceMessageID: "message", Status: state.QueueItemQueued}
			topic.QueuedQueueItemIDs = []string{"queued"}
			action := control.Action{Kind: control.ActionMessageRecalled, SurfaceSessionID: topic.SurfaceSessionID,
				GatewayID: "bot", ChatID: "oc_test", TargetMessageID: "message",
				FeishuTopicRootMessageID: "om_secure_recall", FeishuTopicID: "omt_secure_recall"}
			want := "topic_route_mismatch"
			switch mismatch {
			case "gateway":
				action.GatewayID = "another-bot"
			case "chat":
				action.ChatID = "another-chat"
			case "root":
				action.FeishuTopicRootMessageID = "another-root"
			case "native":
				action.FeishuTopicID = "another-topic"
			case "closed":
				topic.FeishuTopicClosed = true
				want = "topic_closed"
			}
			if events := svc.ValidateFeishuTopicAction(action); noticeByCode(events, want) == nil {
				t.Fatalf("ingress accepted an invalid recall: %#v", events)
			}
			if events := svc.ApplySurfaceAction(action); noticeByCode(events, want) == nil {
				t.Fatalf("core accepted an invalid recall: %#v", events)
			}
			if topic.QueueItems["queued"].Status != state.QueueItemQueued || topic.ActorUserID != "owner" {
				t.Fatal("rejected recall changed the queue or task actor")
			}
		})
	}
}

func TestFeishuTopicRecallOnlyChangesItsOwnPendingInput(t *testing.T) {
	for _, target := range []string{"image", "file", "running", "completed", "other-topic", "unknown"} {
		t.Run(target, func(t *testing.T) {
			svc := topicTestService()
			topic := topicTestCreate(t, svc, "om_pending_recall")
			topic.FeishuTopicID = "omt_pending_recall"
			topic.StagedImages["image"] = &state.StagedImageRecord{ImageID: "image", SourceMessageID: "image", State: state.ImageStaged}
			topic.StagedFiles["file"] = &state.StagedFileRecord{FileID: "file", SourceMessageID: "file", State: state.FileStaged}
			topic.QueueItems["running"] = &state.QueueItemRecord{ID: "running", SourceMessageID: "running", Status: state.QueueItemRunning}
			topic.QueueItems["completed"] = &state.QueueItemRecord{ID: "completed", SourceMessageID: "completed", Status: state.QueueItemCompleted}
			topic.ActiveQueueItemID = "running"
			other := topicTestCreate(t, svc, "om_other_recall")
			other.QueueItems["other"] = &state.QueueItemRecord{ID: "other", SourceMessageID: "other-topic", Status: state.QueueItemQueued}
			other.QueuedQueueItemIDs = []string{"other"}
			events := svc.ApplySurfaceAction(control.Action{Kind: control.ActionMessageRecalled,
				SurfaceSessionID: topic.SurfaceSessionID, GatewayID: "bot", ChatID: "oc_test", TargetMessageID: target})
			if noticeByCode(events, "topic_owner_required") != nil {
				t.Fatalf("recall produced a task owner error: %#v", events)
			}
			if target == "image" && topic.StagedImages["image"].State != state.ImageCancelled ||
				target != "image" && topic.StagedImages["image"].State != state.ImageStaged ||
				target == "file" && topic.StagedFiles["file"].State != state.FileCancelled ||
				target != "file" && topic.StagedFiles["file"].State != state.FileStaged {
				t.Fatal("recall did not isolate its pending attachment")
			}
			if target == "running" && noticeByCode(events, "message_recall_too_late") == nil {
				t.Fatalf("running message lost its existing recall response: %#v", events)
			}
			if topic.ActiveQueueItemID != "running" || topic.QueueItems["running"].Status != state.QueueItemRunning {
				t.Fatal("recall interrupted an already running task")
			}
			if topic.QueueItems["completed"].Status != state.QueueItemCompleted || other.QueueItems["other"].Status != state.QueueItemQueued {
				t.Fatal("recall changed a completed task or another topic")
			}
			if (target == "completed" || target == "other-topic" || target == "unknown") && len(events) != 0 {
				t.Fatalf("unmatched or completed recall should be silent: %#v", events)
			}
		})
	}
}

func TestFeishuTopicRecallExceptionDoesNotAuthorizeUserActions(t *testing.T) {
	for _, actor := range []string{"", "another-member"} {
		for _, kind := range []control.ActionKind{control.ActionTextMessage, control.ActionStop, control.ActionShowHistory, control.ActionReactionCreated} {
			svc := topicTestService()
			topic := topicTestCreate(t, svc, "om_actor")
			topic.FeishuTopicID = "omt_actor"
			action := control.Action{Kind: kind, SurfaceSessionID: topic.SurfaceSessionID, ActorUserID: actor}
			if events := svc.ApplySurfaceAction(action); noticeByCode(events, "topic_owner_required") == nil {
				t.Fatalf("actor %q action %q bypassed task ownership: %#v", actor, kind, events)
			}
			if topic.ActorUserID != "owner" {
				t.Fatal("rejected task actor replaced the topic actor")
			}
		}
	}
}

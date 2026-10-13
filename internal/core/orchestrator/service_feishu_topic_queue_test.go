package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/feishuidentity"
)

func topicQueueSurfaceForTest(s *Service, root, instance, thread, workspace string) *state.SurfaceConsoleRecord {
	id := feishuidentity.TopicRef("app-1", "oc_room", root).SurfaceID()
	surface := s.ensureSurface(control.Action{SurfaceSessionID: id, GatewayID: "app-1", ChatID: "oc_room", ActorUserID: "ou_owner"})
	surface.FeishuTopicRootMessageID = root
	surface.FeishuTopicID = "omt_" + root
	surface.FeishuTopicOwnerID = "ou_owner"
	surface.FeishuTopicBoundThreadID = thread
	surface.AttachedInstanceID = instance
	surface.SelectedThreadID = thread
	surface.RouteMode = state.RouteModePinned
	surface.DispatchMode = state.DispatchModeNormal
	surface.ClaimedWorkspaceKey = workspace
	return surface
}

func TestTopicOwnWorkspaceIgnoresRoomBindingAndReset(t *testing.T) {
	s := newRoomWorkspaceTestService(t)
	a := topicQueueSurfaceForTest(s, "om_a", "inst-droid-a", "thread-droid-a", "/data/dl/droid")
	b := topicQueueSurfaceForTest(s, "om_b", "inst-web", "thread-web", "/data/dl/web")
	room := s.ensureFeishuRoomContextForSurface(a)
	room.WorkspaceKey = "/data/dl/legacy"
	limit := 1
	room.ConcurrencyLimit = &limit
	s.root.Instances[a.AttachedInstanceID].ActiveTurnID = "active-a"
	s.root.Instances[a.AttachedInstanceID].ActiveThreadID = a.SelectedThreadID
	s.reconcileFeishuRoomActiveReservations(room)
	if surfaceFeishuRoomID(a) != surfaceFeishuRoomID(b) {
		t.Fatal("topics lack shared concurrency room")
	}
	if blocked := s.blockFeishuRoomNoWorkspaceDataPlane(b, control.Action{Kind: control.ActionTextMessage}); blocked != nil {
		t.Fatal("topic gated by legacy room workspace")
	}
	if blocked := s.prepareFeishuRoomWorkspaceChange(b, "/data/dl/web"); blocked != nil {
		t.Fatal("topic workspace change gated by room")
	}
	s.syncFeishuRoomWorkspaceBinding(b, "/data/dl/web")
	if room.WorkspaceKey != "/data/dl/legacy" {
		t.Fatal("topic changed global room workspace")
	}
	if !s.sameRoomWorkspaceIndependentContextAllowed(b, "/data/dl/web") {
		t.Fatal("full room blocked independent topic workspace")
	}
	if blocked := s.blockUnsafeFeishuRoomWorkspaceReset(room); blocked != "" {
		t.Fatalf("topic activity prevented legacy workspace reset: %s", blocked)
	}
	s.resetFeishuRoomWorkspaceSurfaces(room, nil)
	if a.AttachedInstanceID != "inst-droid-a" || b.AttachedInstanceID != "inst-web" || a.SelectedThreadID != "thread-droid-a" {
		t.Fatal("room reset detached topic bindings")
	}
	if len(room.ActiveReservations) != 1 {
		t.Fatal("room reset erased topic active reservation")
	}
	if ok, _ := s.reserveFeishuRoomGroupOnDemandSlot(b); !ok {
		t.Fatal("topic preparation rejected by full room")
	}
	if len(room.ActiveReservations) != 1 {
		t.Fatal("topic preparation consumed active execution slot")
	}
}

func TestTopicQueuesWhenRoomFullAndTickWakesAfterOtherTopicCompletes(t *testing.T) {
	s := newRoomWorkspaceTestService(t)
	a := topicQueueSurfaceForTest(s, "om_a", "inst-droid-a", "thread-droid-a", "/data/dl/droid")
	b := topicQueueSurfaceForTest(s, "om_b", "inst-web", "thread-web", "/data/dl/web")
	room := s.ensureFeishuRoomContextForSurface(a)
	limit := 1
	room.ConcurrencyLimit = &limit
	instA := s.root.Instances[a.AttachedInstanceID]
	instA.ActiveTurnID, instA.ActiveThreadID = "active-a", a.SelectedThreadID
	s.reconcileFeishuRoomActiveReservations(room)
	events := s.enqueueQueueItem(b, "om_b_message", "检查网页", nil,
		[]agentproto.Input{{Type: agentproto.InputText, Text: "检查网页"}}, b.SelectedThreadID, "/data/dl/web", state.RouteModePinned, state.ModelConfigRecord{}, false)
	if noticeCode(events, "room_workspace_active") != "" {
		t.Fatal("full room rejected topic message instead of queueing")
	}
	if len(b.QueuedQueueItemIDs) != 1 || b.ActiveQueueItemID != "" {
		t.Fatal("full-room topic message was not retained in queue")
	}
	item := b.QueueItems[b.QueuedQueueItemIDs[0]]
	if item.Status != state.QueueItemQueued || promptSendCommand(events) != nil {
		t.Fatal("topic dispatched above room concurrency limit")
	}
	if events := s.dispatchNext(b); noticeCode(events, "room_workspace_active") != "" {
		t.Fatal("queued topic repeatedly emitted room busy warning")
	}
	instA.ActiveTurnID, instA.ActiveThreadID = "", ""
	s.reconcileFeishuRoomActiveReservations(room)
	events = s.Tick(time.Now().Add(time.Second))
	command := promptSendCommand(events)
	if command == nil || b.ActiveQueueItemID != item.ID {
		t.Fatalf("released room slot did not wake topic queue: commands=%v active=%q", command != nil, b.ActiveQueueItemID)
	}
	if item.Status != state.QueueItemDispatching || command.Target.ThreadID != "thread-web" {
		t.Fatal("queued topic changed frozen destination")
	}
}

func TestTopicDetachedInputDoesNotInheritRoomProjectOrGeneration(t *testing.T) {
	s := newRoomWorkspaceTestService(t)
	legacy := s.ensureSurface(control.Action{SurfaceSessionID: "feishu:app-1:chat:oc_room", GatewayID: "app-1", ChatID: "oc_room"})
	room := s.ensureFeishuRoomContextForSurface(legacy)
	room.WorkspaceKey = "/data/dl/droid"
	room.WorkspaceResetGeneration = 41
	topic := topicQueueSurfaceForTest(s, "om_detached", "", "", "")
	if s.surfaceRoomWorkspaceBindingKey(topic) != "" || s.surfaceCurrentWorkspaceKey(topic) != "" || s.SurfaceSnapshot(topic.SurfaceSessionID).RoomWorkspaceKey != "" {
		t.Fatal("detached topic inherited the group's project")
	}
	if events, inst, handled := s.prepareFeishuRoomWorkspaceForDetachedInput(topic); handled || inst != nil || len(events) != 0 {
		t.Fatal("topic attachment handled by room-global project path")
	}
	events := s.ApplySurfaceAction(control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: topic.SurfaceSessionID,
		GatewayID: "app-1", ChatID: "oc_room", ActorUserID: "ou_owner", MessageID: "om_input", Text: "检查独立项目"})
	if !hasTargetPicker(events) || topic.PendingTextInput == nil || topic.AttachedInstanceID != "" {
		t.Fatalf("unbound topic failed: events=%#v pending=%v attach=%q", events, topic.PendingTextInput != nil, topic.AttachedInstanceID)
	}
	if room.WorkspaceKey != "/data/dl/droid" || room.WorkspaceResetGeneration != 41 {
		t.Fatal("topic input mutated the room generation")
	}
	room.WorkspaceKey = "/data/dl/web"
	room.WorkspaceResetGeneration++
	if s.surfaceCurrentWorkspaceKey(topic) != "" || topic.PendingTextInput == nil {
		t.Fatal("global project change altered pending topic selection")
	}
}

func TestTopicNewConversationFirstInputWaitsForRoomSlot(t *testing.T) {
	s := newRoomWorkspaceTestService(t)
	a := topicQueueSurfaceForTest(s, "om_active", "inst-droid-a", "thread-droid-a", "/data/dl/droid")
	b := topicQueueSurfaceForTest(s, "om_new", "inst-web", "", "/data/dl/web")
	b.RouteMode, b.PreparedThreadCWD = state.RouteModeNewThreadReady, "/data/dl/web"
	room := s.ensureFeishuRoomContextForSurface(a)
	limit := 1
	room.ConcurrencyLimit = &limit
	instA := s.root.Instances[a.AttachedInstanceID]
	instA.ActiveTurnID, instA.ActiveThreadID = "running", a.SelectedThreadID
	action := control.Action{Kind: control.ActionTextMessage, SurfaceSessionID: b.SurfaceSessionID, GatewayID: "app-1",
		ChatID: "oc_room", ActorUserID: "ou_owner", MessageID: "om_first", Text: "新建后检查网页"}
	events := s.ApplySurfaceAction(action)
	if promptSendCommand(events) != nil || len(b.QueuedQueueItemIDs) != 1 {
		t.Fatalf("first input failed: events=%#v queued=%v", events, b.QueuedQueueItemIDs)
	}
	item := b.QueueItems[b.QueuedQueueItemIDs[0]]
	if queuedItemExecutionThreadID(item) != "" || item.RouteModeAtEnqueue != state.RouteModeNewThreadReady || queueItemFrozenCWD(item) != "/data/dl/web" {
		t.Fatal("first creation input lost frozen new-conversation destination")
	}
	action.MessageID, action.Text = "om_second", "第二条"
	if noticeCode(s.ApplySurfaceAction(action), "new_thread_first_input_pending") == "" || len(b.QueuedQueueItemIDs) != 1 {
		t.Fatal("second creation input duplicated queued first turn")
	}
	instA.ActiveTurnID, instA.ActiveThreadID = "", ""
	events = s.Tick(time.Now().Add(time.Second))
	command := promptSendCommand(events)
	if command == nil || !command.Target.CreateThreadIfMissing || command.Target.CWD != "/data/dl/web" || b.ActiveQueueItemID != item.ID {
		t.Fatal("free room slot did not dispatch the original creation request")
	}
}

func TestTopicTwoQueuedCreationPlansContinueFirstCommittedConversation(t *testing.T) {
	s := newRoomWorkspaceTestService(t)
	topic := topicQueueSurfaceForTest(s, "om_two", "inst-web", "", "/data/dl/web")
	topic.RouteMode, topic.PreparedThreadCWD = state.RouteModeNewThreadReady, "/data/dl/web"
	topic.DispatchMode = state.DispatchModePausedForLocal
	for _, id := range []string{"om_first", "om_second"} {
		s.enqueueQueueItem(topic, id, id, nil, []agentproto.Input{{Type: agentproto.InputText, Text: id}},
			"", "/data/dl/web", state.RouteModeNewThreadReady, state.ModelConfigRecord{}, false)
	}
	if len(topic.QueuedQueueItemIDs) != 2 {
		t.Fatal("two creation plans not queued")
	}
	second := topic.QueueItems[topic.QueuedQueueItemIDs[1]]
	topic.DispatchMode = state.DispatchModeNormal
	firstCommand := promptSendCommand(s.dispatchNext(topic))
	if firstCommand == nil || !firstCommand.Target.CreateThreadIfMissing {
		t.Fatal("initial creation was not dispatched")
	}
	first := topic.QueueItems[topic.ActiveQueueItemID]
	inst := s.root.Instances[topic.AttachedInstanceID]
	binding := &remoteTurnBinding{BootstrapNewThread: true, SurfaceSessionID: topic.SurfaceSessionID, QueueItemID: first.ID}
	s.maybeCommitBootstrapSurfaceBinding(topic, inst, first, binding, "created-thread")
	if topic.FeishuTopicBoundThreadID != "created-thread" {
		t.Fatal("first created thread was not fixed to topic")
	}
	first.Status = state.QueueItemCompleted
	topic.ActiveQueueItemID = ""
	s.clearPendingRemoteTurn(inst.InstanceID)
	s.releaseFeishuRoomActiveReservation(topic, first.ID)
	secondCommand := promptSendCommand(s.dispatchNext(topic))
	if secondCommand == nil || secondCommand.Target.CreateThreadIfMissing || secondCommand.Target.ThreadID != "created-thread" || topic.ActiveQueueItemID != second.ID {
		t.Fatal("later creation plan did not continue the first fixed conversation")
	}
	if second.RouteModeAtEnqueue != state.RouteModePinned || second.FrozenDispatchPlan.ExecutionMode != agentproto.PromptExecutionModeResumeExisting {
		t.Fatal("later queued creation plan was not canonicalized")
	}
}

func TestTopicDispatchRejectsExplicitOtherConversationAndStopsClosedQueue(t *testing.T) {
	for _, testCase := range []string{"other-thread", "closed", "helper-new", "ephemeral", "scheduled-new"} {
		t.Run(testCase, func(t *testing.T) {
			s := newRoomWorkspaceTestService(t)
			topic := topicQueueSurfaceForTest(s, "om_guard", "inst-web", "thread-web", "/data/dl/web")
			topic.DispatchMode = state.DispatchModePausedForLocal
			target := "thread-web"
			if testCase == "other-thread" {
				target = "explicit-other"
			}
			s.enqueueQueueItem(topic, "om_message", "输入", nil, []agentproto.Input{{Type: agentproto.InputText, Text: "输入"}},
				target, "/data/dl/web", state.RouteModePinned, state.ModelConfigRecord{}, false)
			item := topic.QueueItems[topic.QueuedQueueItemIDs[0]]
			if testCase == "helper-new" {
				item.FrozenDispatchPlan.ExecutionMode = agentproto.PromptExecutionModeStartNew
				item.FrozenDispatchPlan.ExecutionThreadID = ""
				item.FrozenDispatchPlan.Purpose = agentproto.PromptPurposeReview
				item.FrozenDispatchPlan.InternalHelper = true
			}
			if testCase == "ephemeral" {
				item.FrozenDispatchPlan.ExecutionMode = agentproto.PromptExecutionModeForkEphemeral
				item.FrozenDispatchPlan.ExecutionThreadID = ""
				item.FrozenDispatchPlan.SourceThreadID = "thread-web"
			}
			if testCase == "scheduled-new" {
				item.ScheduledRunID = "scheduled-new-run"
				item.FrozenDispatchPlan.ExecutionMode = agentproto.PromptExecutionModeStartNew
				item.FrozenDispatchPlan.ExecutionThreadID = ""
			}
			originalPlan := item.FrozenDispatchPlan
			topic.DispatchMode = state.DispatchModeNormal
			topic.FeishuTopicClosed = testCase == "closed"
			events := s.dispatchNext(topic)
			if promptSendCommand(events) != nil || topic.ActiveQueueItemID != "" {
				t.Fatal("unsafe topic queue dispatched")
			}
			if testCase != "closed" {
				if item.Status != state.QueueItemFailed || item.ResultError != "topic_bound_thread_mismatch" || item.FrozenDispatchPlan != originalPlan || len(topic.QueuedQueueItemIDs) != 0 {
					t.Fatal("explicit mismatched target was silently rewritten")
				}
			} else if item.Status != state.QueueItemQueued || len(topic.QueuedQueueItemIDs) != 1 {
				t.Fatal("closed queue was consumed")
			}
		})
	}
}

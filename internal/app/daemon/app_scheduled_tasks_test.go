package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/app/scheduledtasks"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/orchestrator"
)

type scheduledReportGateway struct {
	fail    bool
	reports []feishu.Operation
}

func (g *scheduledReportGateway) Start(context.Context, feishu.ActionHandler) error { return nil }
func (g *scheduledReportGateway) Apply(_ context.Context, ops []feishu.Operation) error {
	for _, op := range ops {
		if !strings.HasPrefix(op.Text, "定时任务：") {
			continue
		}
		if g.fail {
			return errors.New("notification transport unavailable")
		}
		g.reports = append(g.reports, op)
	}
	return nil
}

func scheduledRuntimeFixture(t *testing.T) (*App, *scheduledtasks.Store, scheduledtasks.Task, time.Time) {
	t.Helper()
	app, ctx, _ := scheduledToolTestApp(t)
	args := scheduleCreateArgs()
	args["kind"] = "once"
	delete(args, "interval_minutes")
	when := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	args["at"] = when.Format(time.RFC3339)
	view, e := app.handleScheduledTaskTool(ctx, scheduleCreateToolName, args)
	if e != nil {
		t.Fatal(e)
	}
	store := app.scheduledTasks.store
	task, err := store.Get(scheduledTaskOwner("schedule-gateway", "schedule-surface", "schedule-user"), view.(map[string]any)["task_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return app, store, task, when.Add(time.Second)
}

func scheduledRuntimeTick(app *App, now time.Time) {
	app.mu.Lock()
	defer app.mu.Unlock()
	app.maybeScheduleTasksLocked(context.Background(), now)
}

func finishScheduledFixtureUserTurn(app *App) {
	app.service.ApplyAgentEvent("schedule-instance", agentproto.Event{Kind: agentproto.EventTurnCompleted, ThreadID: "schedule-thread", TurnID: "schedule-turn", Status: "completed", Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: "schedule-surface"}})
}

func TestScheduledRuntimePersistsBeforeWireAndRetriesOnlyNotification(t *testing.T) {
	app, store, task, now := scheduledRuntimeFixture(t)
	gateway := &scheduledReportGateway{fail: true}
	app.gateway = gateway
	finishScheduledFixtureUserTurn(app)
	sent := 0
	app.sendAgentCommand = func(instance string, command agentproto.Command) error {
		if command.Kind != agentproto.CommandPromptSend {
			return nil
		}
		sent++
		runs, err := store.OutstandingRuns()
		if err != nil || len(runs) != 1 || runs[0].Status != scheduledtasks.StatusRunning || runs[0].QueueItemID == "" || runs[0].InstanceID != instance || runs[0].CommandID != command.CommandID {
			t.Fatalf("wire command preceded durable acceptance: %+v %v", runs, err)
		}
		return nil
	}
	scheduledRuntimeTick(app, now)
	if sent != 1 {
		t.Fatalf("expected one model command, got %d", sent)
	}
	runs, _ := store.OutstandingRuns()
	run := runs[0]
	initiator := agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: task.SurfaceID}
	app.service.ApplyAgentEvent(run.InstanceID, agentproto.Event{Kind: agentproto.EventTurnStarted, ThreadID: task.ThreadID, TurnID: "task-turn", Initiator: initiator})
	app.service.ApplyAgentEvent(run.InstanceID, agentproto.Event{Kind: agentproto.EventItemCompleted, ThreadID: task.ThreadID, TurnID: "task-turn", ItemID: "answer", ItemKind: "agent_message", Metadata: map[string]any{"text": "设备正常"}, Initiator: initiator})
	app.service.ApplyAgentEvent(run.InstanceID, agentproto.Event{Kind: agentproto.EventTurnCompleted, ThreadID: task.ThreadID, TurnID: "task-turn", Status: "completed", Initiator: initiator})
	// Selection/last actor changes do not alter the persisted notification target.
	surface := app.service.Surface(task.SurfaceID)
	surface.ChatID, surface.ActorUserID = "other-chat", "other-user"
	scheduledRuntimeTick(app, now.Add(11*time.Second))
	run, err := store.GetRun(run.ID)
	if err != nil || run.Status != scheduledtasks.StatusSucceeded || run.Notified || run.Result != "设备正常" {
		t.Fatalf("lost terminal/outbox evidence: %+v %v", run, err)
	}
	gateway.fail = false
	app.mu.Lock()
	app.scheduledTasks.notifyRetry[run.ID] = time.Time{}
	app.reportScheduledRunsLocked(context.Background(), store)
	app.mu.Unlock()
	run, _ = store.GetRun(run.ID)
	if !run.Notified || sent != 1 || len(gateway.reports) != 1 {
		t.Fatalf("notification retry reran job or lost report: %+v sent=%d reports=%d", run, sent, len(gateway.reports))
	}
	op := gateway.reports[0]
	if op.ChatID != task.ChatID || op.GatewayID != task.GatewayID || op.ReceiveID != task.ChatID || !strings.Contains(op.Text, "设备正常") {
		t.Fatalf("report retargeted: %+v", op)
	}
	app.mu.Lock()
	app.reportScheduledRunsLocked(context.Background(), store)
	app.mu.Unlock()
	if len(gateway.reports) != 1 {
		t.Fatal("already delivered result sent again")
	}
}

func TestScheduledRuntimePauseCancelsAcceptedUnsentRound(t *testing.T) {
	app, store, task, now := scheduledRuntimeFixture(t)
	sent := 0
	app.sendAgentCommand = func(string, agentproto.Command) error { sent++; return nil }
	// The user turn is still active, so the due task can only enqueue.
	scheduledRuntimeTick(app, now)
	runs, _ := store.OutstandingRuns()
	if len(runs) != 1 || runs[0].QueueItemID == "" || sent != 0 {
		t.Fatalf("busy writer was not queued: %+v sent=%d", runs, sent)
	}
	app.mu.Lock()
	_, err := store.SetEnabled(task.Owner, task.ID, false, now)
	if err == nil {
		err = app.cancelWaitingScheduledTaskRunsLocked(task.Owner, task.ID, now)
	}
	app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	run, _ := store.GetRun(runs[0].ID)
	if run.Status != scheduledtasks.StatusCancelled {
		t.Fatalf("accepted waiting round continued after pause: %+v", run)
	}
	finishScheduledFixtureUserTurn(app)
	scheduledRuntimeTick(app, now.Add(2*time.Second))
	if sent != 0 {
		t.Fatal("cancelled round reached the model")
	}
}

func TestScheduledRuntimeWaitsForFrozenDestinationAndMarksLostAcceptanceUnknown(t *testing.T) {
	app, store, task, now := scheduledRuntimeFixture(t)
	app.service.Instance("schedule-instance").Online = false
	scheduledRuntimeTick(app, now)
	runs, _ := store.OutstandingRuns()
	if len(runs) != 1 || runs[0].Status != scheduledtasks.StatusPending || runs[0].Error != "instance_unavailable" {
		t.Fatalf("offline route silently changed: %+v", runs)
	}
	// An accepted run whose volatile queue was reset must never be submitted again.
	run := runs[0]
	run.Status, run.InstanceID, run.QueueItemID = scheduledtasks.StatusRunning, "old-instance", "old-queue"
	if err := store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	app.service.Instance("schedule-instance").Online = true
	sent := 0
	app.sendAgentCommand = func(string, agentproto.Command) error { sent++; return nil }
	scheduledRuntimeTick(app, now.Add(2*time.Second))
	run, _ = store.GetRun(run.ID)
	if run.Status != scheduledtasks.StatusInterrupted || !strings.Contains(run.Error, "not replayed") || sent != 0 {
		t.Fatalf("unknown accepted run was replayed: %+v sent=%d", run, sent)
	}
	stored, _ := store.Get(task.Owner, task.ID)
	if stored.Enabled {
		t.Fatal("one-time task remained enabled")
	}
}

func TestScheduledRuntimeRestartDoesNotReplayAcceptedRun(t *testing.T) {
	app, store, task, now := scheduledRuntimeFixture(t)
	runs, err := store.ReserveDue(now, 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("reserve: %+v %v", runs, err)
	}
	run := runs[0]
	run.Status = scheduledtasks.StatusRunning
	run.InstanceID = "old"
	run.QueueItemID = "old"
	if err = store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	err = store.Close()
	app.scheduledTasks.store = nil
	app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	reopened, err := app.scheduledTaskStoreLocked()
	app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	run, err = reopened.GetRun(run.ID)
	if err != nil || run.Status != scheduledtasks.StatusInterrupted {
		t.Fatalf("restart replayed accepted run: %+v %v", run, err)
	}
	history, _ := reopened.RunHistory(task.Owner, task.ID, 5)
	if len(history) != 1 {
		t.Fatal("recovery created another run")
	}
}

// Commands cached before a gateway lock release must not execute after pause.
func TestScheduledRuntimeDropsStaleCancelledCommand(t *testing.T) {
	app, store, task, now := scheduledRuntimeFixture(t)
	finishScheduledFixtureUserTurn(app)
	runs, err := store.ReserveDue(now, 1)
	if err != nil || len(runs) != 1 {
		t.Fatal(err)
	}
	run := runs[0]
	result := app.service.TryEnqueueScheduledPrompt(orchestratorRequestForScheduledTest(run))
	if result.QueueItemID == "" {
		t.Fatalf("enqueue: %+v", result)
	}
	run.Status = scheduledtasks.StatusRunning
	run.QueueItemID = result.QueueItemID
	run.InstanceID = result.InstanceID
	if err = store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	if err = app.cancelWaitingScheduledTaskRunsLocked(task.Owner, task.ID, now); err != nil {
		t.Fatal(err)
	}
	sent := 0
	app.sendAgentCommand = func(string, agentproto.Command) error { sent++; return nil }
	app.handleUIEventsLocked(context.Background(), result.Events)
	app.mu.Unlock()
	if sent != 0 {
		t.Fatal("stale cancelled command reached agent")
	}
	if app.service.Surface(task.SurfaceID).ActiveQueueItemID != "" {
		t.Fatal("cancelled queue retained active ownership")
	}
}

func orchestratorRequestForScheduledTest(run scheduledtasks.Run) orchestrator.ScheduledPromptRequest {
	return orchestrator.ScheduledPromptRequest{RunID: run.ID, GatewayID: run.Task.GatewayID, SurfaceID: run.Task.SurfaceID, ChatID: run.Task.ChatID, ActorUserID: run.Task.ActorUserID, Workspace: run.Task.Workspace, CWD: run.Task.CWD, ThreadID: run.Task.ThreadID, Prompt: run.Task.Prompt, Mode: run.Task.Mode}
}

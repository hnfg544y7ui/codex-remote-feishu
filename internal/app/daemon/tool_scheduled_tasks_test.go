package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/scheduledtasks"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func scheduledToolTestApp(t *testing.T) (*App, context.Context, *state.SurfaceConsoleRecord) {
	t.Helper()
	app, _ := newToolServiceTestApp(t, nil)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	workspace := t.TempDir()
	app.service.UpsertInstance(&state.InstanceRecord{InstanceID: "schedule-instance", WorkspaceRoot: workspace, WorkspaceKey: workspace, Source: "headless", Online: true, Threads: map[string]*state.ThreadRecord{}})
	app.HandleAction(context.Background(), control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "schedule-surface", GatewayID: "schedule-gateway", ChatID: "schedule-chat", ActorUserID: "schedule-user", InstanceID: "schedule-instance"})
	startToolTestRemoteTurn(t, app, "schedule-surface", "schedule-instance", "schedule-thread", "schedule-turn")
	return app, withToolCallerInstanceID(context.Background(), "schedule-instance"), app.service.Surface("schedule-surface")
}

func scheduleCreateArgs() map[string]any {
	return map[string]any{"name": "Check device", "prompt": "Read device status without making changes", "kind": "interval", "interval_minutes": float64(1)}
}

func TestScheduleUpdatesPreserveTargetWhenRepeatingModeOrChangingToNew(t *testing.T) {
	task := scheduledtasks.Task{Workspace: "original-root", CWD: "original-cwd", ThreadID: "original-thread", Mode: scheduledtasks.ModeExisting}
	caller := scheduleCaller{Workspace: "other-root", CWD: "other-cwd", ThreadID: "other-thread"}
	if err := applyScheduleArguments(&task, map[string]any{"mode": "existing", "interval_seconds": 60}, caller); err != nil {
		t.Fatal(err)
	}
	if task.Workspace != "original-root" || task.CWD != "original-cwd" || task.ThreadID != "original-thread" {
		t.Fatalf("repeated mode silently retargeted task: %+v", task)
	}
	if err := applyScheduleArguments(&task, map[string]any{"mode": "new_each_run"}, caller); err != nil {
		t.Fatal(err)
	}
	if task.Workspace != "original-root" || task.CWD != "original-cwd" || task.ThreadID != "" {
		t.Fatalf("new mode silently changed workspace: %+v", task)
	}
	if err := applyScheduleArguments(&task, map[string]any{"mode": "existing"}, caller); err == nil {
		t.Fatal("existing target silently moved to caller's unrelated workspace")
	}
}

func TestScheduleToolsUseImmutableTurnActorThreadAndWorkspace(t *testing.T) {
	app, ctx, surface := scheduledToolTestApp(t)
	turns := app.service.ActiveRemoteTurns()
	if len(turns) != 1 {
		t.Fatalf("no active turn: %+v", turns)
	}
	item := surface.QueueItems[turns[0].QueueItemID]
	if item == nil {
		t.Fatal("active turn queue item missing")
	}
	originalWorkspace := app.service.Instance("schedule-instance").WorkspaceRoot
	originalCWD := filepath.Join(originalWorkspace, "subdirectory")
	item.FrozenDispatchPlan.CWD = originalCWD
	surface.ActorUserID = "someone-else"
	surface.SelectedThreadID = "unrelated-thread"
	view, apiErr := app.handleScheduledTaskTool(ctx, scheduleCreateToolName, scheduleCreateArgs())
	if apiErr != nil {
		t.Fatalf("create: %+v", apiErr)
	}
	id := view.(map[string]any)["task_id"].(string)
	app.mu.Lock()
	store, err := app.scheduledTaskStoreLocked()
	app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.Get(scheduledTaskOwner("schedule-gateway", "schedule-surface", "schedule-user"), id)
	if err != nil {
		t.Fatal(err)
	}
	if task.ActorUserID != "schedule-user" || task.ThreadID != "schedule-thread" || task.Workspace != originalWorkspace || task.CWD != originalCWD || task.ChatID != "schedule-chat" {
		t.Fatalf("used mutable surface routing: %+v", task)
	}
	if task.TimeZone != "Asia/Shanghai" || task.IntervalSeconds != 60 || !task.Enabled {
		t.Fatalf("wrong defaults: %+v", task)
	}
}

func TestScheduleToolsRejectAutomaticTurnsAndMissingCaller(t *testing.T) {
	app, ctx, surface := scheduledToolTestApp(t)
	if _, apiErr := app.handleScheduledTaskTool(context.Background(), scheduleListToolName, map[string]any{}); apiErr == nil || apiErr.Code != "schedule_forbidden" {
		t.Fatalf("missing caller accepted: %+v", apiErr)
	}
	turn := app.service.ActiveRemoteTurns()[0]
	item := surface.QueueItems[turn.QueueItemID]
	item.SourceKind = state.QueueItemSourceKind("scheduled_task")
	for _, name := range []string{scheduleCreateToolName, scheduleListToolName, schedulePauseToolName, scheduleDeleteToolName} {
		args := map[string]any{"task_id": "some-task"}
		if name == scheduleCreateToolName {
			args = scheduleCreateArgs()
		}
		if name == scheduleListToolName {
			args = map[string]any{}
		}
		if _, apiErr := app.handleScheduledTaskTool(ctx, name, args); apiErr == nil || apiErr.Code != "schedule_forbidden" {
			t.Fatalf("automatic turn called %s: %+v", name, apiErr)
		}
	}
}

func TestScheduleToolsOwnerIsolationAndCRUD(t *testing.T) {
	app, ctx, surface := scheduledToolTestApp(t)
	view, apiErr := app.handleScheduledTaskTool(ctx, scheduleCreateToolName, scheduleCreateArgs())
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	id := view.(map[string]any)["task_id"].(string)
	updated, apiErr := app.handleScheduledTaskTool(ctx, scheduleUpdateToolName, map[string]any{"task_id": id, "name": "Updated", "interval_seconds": float64(120), "mode": "new_each_run"})
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if updated.(map[string]any)["thread_id"] != "" || updated.(map[string]any)["name"] != "Updated" {
		t.Fatalf("update failed: %+v", updated)
	}
	listed, apiErr := app.handleScheduledTaskTool(ctx, scheduleListToolName, map[string]any{})
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if len(listed.(map[string]any)["tasks"].([]map[string]any)) != 1 {
		t.Fatalf("missing task: %+v", listed)
	}
	turn := app.service.ActiveRemoteTurns()[0]
	item := surface.QueueItems[turn.QueueItemID]
	item.ActorUserID = "different-user"
	listed, apiErr = app.handleScheduledTaskTool(ctx, scheduleListToolName, map[string]any{})
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if len(listed.(map[string]any)["tasks"].([]map[string]any)) != 0 {
		t.Fatal("another actor saw tasks")
	}
	for _, name := range []string{scheduleUpdateToolName, schedulePauseToolName, scheduleDeleteToolName} {
		if _, apiErr = app.handleScheduledTaskTool(ctx, name, map[string]any{"task_id": id}); apiErr == nil || apiErr.Code != "schedule_not_found" {
			t.Fatalf("another actor called %s: %+v", name, apiErr)
		}
	}
	item.ActorUserID = "schedule-user"
	paused, apiErr := app.handleScheduledTaskTool(ctx, schedulePauseToolName, map[string]any{"task_id": id})
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if paused.(map[string]any)["enabled"] != false {
		t.Fatal("pause failed")
	}
	resumed, apiErr := app.handleScheduledTaskTool(ctx, schedulePauseToolName, map[string]any{"task_id": id, "paused": false})
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if resumed.(map[string]any)["enabled"] != true {
		t.Fatal("resume failed")
	}
	if _, apiErr = app.handleScheduledTaskTool(ctx, scheduleDeleteToolName, map[string]any{"task_id": id}); apiErr != nil {
		t.Fatal(apiErr)
	}
	if _, apiErr = app.handleScheduledTaskTool(ctx, scheduleDeleteToolName, map[string]any{"task_id": id}); apiErr == nil {
		t.Fatal("deleted task visible")
	}
}

func TestScheduleToolsStrictSchemasAndArgumentValidation(t *testing.T) {
	definitions := scheduledTaskToolDefinitions()
	if len(definitions) != 5 {
		t.Fatal("missing definitions")
	}
	for _, d := range definitions {
		if d.InputSchema["additionalProperties"] != false {
			t.Fatalf("loose schema %s", d.Name)
		}
		data, err := json.Marshal(d.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var parsed map[string]any
		if err = json.Unmarshal(data, &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed["required"] == nil {
			t.Fatalf("null required schema: %s", d.Name)
		}
		for _, forbidden := range []string{"owner", "gateway_id", "surface_session_id", "chat_id", "actor_user_id", "workspace", "thread_id"} {
			if _, ok := d.InputSchema["properties"].(map[string]any)[forbidden]; ok {
				t.Fatalf("identity input in %s: %s", d.Name, forbidden)
			}
		}
	}
	app, ctx, _ := scheduledToolTestApp(t)
	for _, change := range []func(map[string]any){
		func(a map[string]any) { a["owner"] = "other" }, func(a map[string]any) { a["prompt"] = 7 }, func(a map[string]any) { a["interval_minutes"] = 1.5 }, func(a map[string]any) { a["interval_minutes"] = 0 }, func(a map[string]any) { a["interval_seconds"] = 9; delete(a, "interval_minutes") }, func(a map[string]any) { a["time_zone"] = "bad/zone" }, func(a map[string]any) { a["interval_seconds"] = 60 }, func(a map[string]any) { a["kind"] = "once"; a["at"] = "tomorrow" },
	} {
		args := scheduleCreateArgs()
		change(args)
		if _, apiErr := app.handleScheduledTaskTool(ctx, scheduleCreateToolName, args); apiErr == nil {
			t.Fatalf("invalid arguments accepted: %+v", args)
		}
	}
	if _, apiErr := app.handleScheduledTaskTool(ctx, schedulePauseToolName, map[string]any{"task_id": "x", "paused": "true"}); apiErr == nil {
		t.Fatal("invalid paused accepted")
	}
}

func TestScheduleToolsSupportOnceAndDailyAndFreezeNewThreadMode(t *testing.T) {
	app, ctx, _ := scheduledToolTestApp(t)
	once := scheduleCreateArgs()
	once["kind"] = "once"
	once["at"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	once["mode"] = "new_each_run"
	delete(once, "interval_minutes")
	view, apiErr := app.handleScheduledTaskTool(ctx, scheduleCreateToolName, once)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if view.(map[string]any)["mode"] != scheduledtasks.ModeNewEachRun || view.(map[string]any)["thread_id"] != "" {
		t.Fatal("new thread mode incorrectly pinned")
	}
	daily := scheduleCreateArgs()
	daily["kind"] = "daily"
	daily["daily_time"] = "09:30"
	daily["time_zone"] = "America/New_York"
	delete(daily, "interval_minutes")
	if _, apiErr = app.handleScheduledTaskTool(ctx, scheduleCreateToolName, daily); apiErr != nil {
		t.Fatal(apiErr)
	}
}

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/scheduledtasks"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/orchestrator"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

const (
	scheduleCreateToolName = "schedule_create"
	scheduleUpdateToolName = "schedule_update"
	scheduleListToolName   = "schedule_list"
	schedulePauseToolName  = "schedule_pause"
	scheduleDeleteToolName = "schedule_delete"
)

func scheduledTaskToolDefinitions() []toolDefinition {
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	fields := func() map[string]any {
		return map[string]any{
			"name": text("Short user-facing task name"), "prompt": text("Instructions to execute each scheduled round"),
			"mode":             map[string]any{"type": "string", "enum": []string{"existing", "new_each_run"}, "description": "existing freezes the actual calling thread; select another thread with /use before creating. new_each_run starts a fresh session per round."},
			"kind":             map[string]any{"type": "string", "enum": []string{"interval", "daily", "once"}},
			"interval_seconds": map[string]any{"type": "integer", "minimum": 10, "maximum": 31536000},
			"interval_minutes": map[string]any{"type": "integer", "minimum": 1, "maximum": 525600},
			"time_zone":        text("IANA time zone, defaults to Asia/Shanghai"), "daily_time": text("Daily local HH:MM, e.g. 09:30"),
			"at": text("One-time future RFC3339 timestamp with explicit offset, e.g. 2026-10-11T09:30:00+08:00"),
		}
	}
	schema := func(properties map[string]any, required ...string) map[string]any {
		if required == nil {
			required = []string{}
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	create := fields()
	update := fields()
	update["task_id"] = text("Task ID returned by schedule_create or schedule_list")
	common := "Use these local schedule tools when the Feishu user asks for a timed task, reminder, recurring check, or changes to a schedule. They persist on this host and deliver results to the original conversation. Prefer these tools over automation_update or writing crontab entries. "
	return []toolDefinition{
		{Name: scheduleCreateToolName, Description: common + "Create a schedule owned by the user who started the current Feishu turn. existing uses this turn's thread; new_each_run starts a fresh thread every round. Tasks themselves cannot create further schedules.", InputSchema: schema(create, "name", "prompt", "kind")},
		{Name: scheduleUpdateToolName, Description: common + "Update an owned task's supplied fields. Origin and workspace cannot change. Repeating the same mode preserves the target. Switching from new_each_run to existing selects the calling thread only in the task's original workspace.", InputSchema: schema(update, "task_id")},
		{Name: scheduleListToolName, Description: common + "List only the current user's tasks in this conversation, including enabled state and next run time.", InputSchema: schema(map[string]any{})},
		{Name: schedulePauseToolName, Description: common + "Pause future rounds and cancel waiting rounds while allowing an active round to finish. Pass paused=false to resume with a recalculated next time.", InputSchema: schema(map[string]any{"task_id": text("Task ID"), "paused": map[string]any{"type": "boolean", "default": true}}, "task_id")},
		{Name: scheduleDeleteToolName, Description: common + "Delete an owned schedule and cancel waiting rounds; do not interrupt an already executing round.", InputSchema: schema(map[string]any{"task_id": text("Task ID")}, "task_id")},
	}
}

func scheduledTaskOwner(gateway, surface, actor string) string {
	raw, _ := json.Marshal([]string{gateway, surface, actor})
	return string(raw)
}

type scheduleCaller struct {
	Owner, GatewayID, SurfaceID, ChatID, ActorUserID, Workspace, CWD, ThreadID string
}

// Schedule ownership comes from the immutable queue item, never the surface's
// mutable last actor or currently selected thread.
func (a *App) resolveScheduleCallerLocked(ctx context.Context) (scheduleCaller, error) {
	id := toolCallerInstanceIDFromContext(ctx)
	if id == "" {
		return scheduleCaller{}, errors.New("current remote turn caller is not available")
	}
	selectTurn := func(turns []orchestrator.RemoteTurnStatus) (*orchestrator.RemoteTurnStatus, error) {
		var selected *orchestrator.RemoteTurnStatus
		for _, turn := range turns {
			if turn.InstanceID == id {
				if selected != nil {
					return nil, errors.New("ambiguous remote turn caller")
				}
				copy := turn
				selected = &copy
			}
		}
		return selected, nil
	}
	turn, err := selectTurn(a.service.ActiveRemoteTurns())
	if err != nil {
		return scheduleCaller{}, err
	}
	if turn == nil {
		turn, err = selectTurn(a.service.PendingRemoteTurns())
		if err != nil {
			return scheduleCaller{}, err
		}
	}
	if turn == nil {
		return scheduleCaller{}, errors.New("schedule tools require a user-triggered Feishu turn")
	}
	surface := a.service.Surface(turn.SurfaceSessionID)
	if surface == nil || state.NormalizeProductMode(surface.ProductMode) != state.ProductModeNormal {
		return scheduleCaller{}, errors.New("current Feishu surface is unavailable")
	}
	item := surface.QueueItems[turn.QueueItemID]
	if item == nil || item.SourceKind != state.QueueItemSourceUser || strings.TrimSpace(item.ActorUserID) == "" {
		return scheduleCaller{}, errors.New("only an explicit user turn can manage schedules; automatic tasks cannot create or modify schedules")
	}
	inst := a.service.Instance(id)
	if inst == nil {
		return scheduleCaller{}, errors.New("calling instance unavailable")
	}
	if state.EffectiveInstanceBackend(inst) != agentproto.BackendCodex || state.IsInstanceSource(inst.Source, state.InstanceSourceVSCode) {
		return scheduleCaller{}, errors.New("local schedules currently require Codex headless mode")
	}
	thread := strings.TrimSpace(turn.ThreadID)
	if thread == "" {
		thread = strings.TrimSpace(item.FrozenDispatchPlan.ExecutionThreadID)
	}
	workspace := strings.TrimSpace(inst.WorkspaceRoot)
	if workspace == "" {
		workspace = strings.TrimSpace(inst.WorkspaceKey)
	}
	cwd := strings.TrimSpace(item.FrozenDispatchPlan.CWD)
	if cwd == "" && inst.Threads[thread] != nil {
		cwd = strings.TrimSpace(inst.Threads[thread].CWD)
	}
	if cwd == "" {
		cwd = workspace
	}
	caller := scheduleCaller{GatewayID: strings.TrimSpace(surface.GatewayID), SurfaceID: surface.SurfaceSessionID, ChatID: strings.TrimSpace(surface.ChatID), ActorUserID: item.ActorUserID, Workspace: workspace, CWD: cwd, ThreadID: thread}
	if caller.GatewayID == "" || caller.SurfaceID == "" || caller.ChatID == "" {
		return scheduleCaller{}, errors.New("calling conversation has no delivery address")
	}
	caller.Owner = scheduledTaskOwner(caller.GatewayID, caller.SurfaceID, caller.ActorUserID)
	return caller, nil
}

func (a *App) handleScheduledTaskTool(ctx context.Context, name string, args map[string]any) (any, *toolError) {
	if err := validateScheduleArguments(name, args); err != nil {
		return nil, scheduleToolError("schedule_invalid", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.shuttingDown {
		return nil, scheduleToolError("schedule_unavailable", errors.New("daemon is shutting down"))
	}
	caller, err := a.resolveScheduleCallerLocked(ctx)
	if err != nil {
		return nil, scheduleToolError("schedule_forbidden", err)
	}
	store, err := a.scheduledTaskStoreLocked()
	if err != nil {
		return nil, scheduleToolError("schedule_unavailable", err)
	}
	now := time.Now()
	if name == scheduleListToolName {
		tasks, err := store.List(caller.Owner)
		if err != nil {
			return nil, scheduleToolError("schedule_failed", err)
		}
		views := []map[string]any{}
		for _, t := range tasks {
			view := scheduledTaskToolView(t)
			history, err := store.RunHistory(caller.Owner, t.ID, 1)
			if err != nil {
				return nil, scheduleToolError("schedule_failed", err)
			}
			if len(history) > 0 {
				r := history[0]
				view["last_run"] = map[string]any{"run_id": r.ID, "status": r.Status, "scheduled_at": r.ScheduledAt, "started_at": r.StartedAt, "completed_at": r.CompletedAt, "error": r.Error}
			}
			views = append(views, view)
		}
		return map[string]any{"tasks": views}, nil
	}
	var task scheduledtasks.Task
	if name == scheduleCreateToolName {
		task = scheduledtasks.Task{Owner: caller.Owner, GatewayID: caller.GatewayID, SurfaceID: caller.SurfaceID, ChatID: caller.ChatID, ActorUserID: caller.ActorUserID, Workspace: caller.Workspace, CWD: caller.CWD, ThreadID: caller.ThreadID, Mode: scheduledtasks.ModeExisting, TimeZone: "Asia/Shanghai", Enabled: true}
	} else {
		id, _ := args["task_id"].(string)
		if strings.TrimSpace(id) == "" {
			return nil, scheduleToolError("schedule_invalid", errors.New("task_id is required"))
		}
		task, err = store.Get(caller.Owner, id)
		if err != nil {
			return nil, scheduleStoreToolError(err)
		}
	}
	switch name {
	case scheduleDeleteToolName:
		if err = store.Delete(caller.Owner, task.ID); err != nil {
			return nil, scheduleStoreToolError(err)
		}
		if err = a.cancelWaitingScheduledTaskRunsLocked(caller.Owner, task.ID, now); err != nil {
			return nil, scheduleStoreToolError(err)
		}
		return map[string]any{"task_id": task.ID, "deleted": true}, nil
	case schedulePauseToolName:
		paused := true
		if value, ok := args["paused"]; ok {
			paused = value.(bool)
		}
		task, err = store.SetEnabled(caller.Owner, task.ID, !paused, now)
		if err == nil && paused {
			err = a.cancelWaitingScheduledTaskRunsLocked(caller.Owner, task.ID, now)
		}
	case scheduleCreateToolName, scheduleUpdateToolName:
		if err = applyScheduleArguments(&task, args, caller); err != nil {
			return nil, scheduleToolError("schedule_invalid", err)
		}
		if name == scheduleCreateToolName {
			task, err = store.Create(task, now)
		} else {
			task, err = store.Update(caller.Owner, task.ID, task, now)
		}
	default:
		return nil, scheduleToolError("schedule_invalid", errors.New("unknown schedule tool"))
	}
	if err != nil {
		return nil, scheduleStoreToolError(err)
	}
	return scheduledTaskToolView(task), nil
}

func validateScheduleArguments(name string, args map[string]any) error {
	var definition *toolDefinition
	for _, d := range scheduledTaskToolDefinitions() {
		if d.Name == name {
			copy := d
			definition = &copy
			break
		}
	}
	if definition == nil {
		return errors.New("unknown schedule tool")
	}
	properties := definition.InputSchema["properties"].(map[string]any)
	for key, value := range args {
		property, ok := properties[key]
		if !ok {
			return fmt.Errorf("unsupported argument %s", key)
		}
		switch property.(map[string]any)["type"] {
		case "string":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s must be a string", key)
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be a boolean", key)
			}
		case "integer":
			if _, err := scheduleInteger(value); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	for _, key := range definition.InputSchema["required"].([]string) {
		if _, ok := args[key]; !ok {
			return fmt.Errorf("%s is required", key)
		}
	}
	return nil
}

func applyScheduleArguments(t *scheduledtasks.Task, args map[string]any, caller scheduleCaller) error {
	oldMode := t.Mode
	for key, destination := range map[string]*string{"name": &t.Name, "prompt": &t.Prompt, "mode": &t.Mode, "kind": &t.Kind, "time_zone": &t.TimeZone, "daily_time": &t.DailyTime} {
		if value, ok := args[key]; ok {
			*destination = strings.TrimSpace(value.(string))
		}
	}
	if _, seconds := args["interval_seconds"]; seconds {
		if _, minutes := args["interval_minutes"]; minutes {
			return errors.New("provide interval_seconds or interval_minutes, not both")
		}
	}
	if value, ok := args["interval_seconds"]; ok {
		seconds, err := scheduleInteger(value)
		if err != nil {
			return err
		}
		t.IntervalSeconds = seconds
	}
	if value, ok := args["interval_minutes"]; ok {
		minutes, err := scheduleInteger(value)
		if err != nil {
			return err
		}
		if minutes < 1 || minutes > 525600 {
			return errors.New("interval_minutes must be between 1 and 525600")
		}
		t.IntervalSeconds = minutes * 60
	}
	if value, ok := args["at"]; ok {
		at, err := time.Parse(time.RFC3339, value.(string))
		if err != nil {
			return errors.New("at must be a RFC3339 timestamp with a timezone")
		}
		t.At = at
	}
	if t.Mode == scheduledtasks.ModeNewEachRun {
		t.ThreadID = ""
	} else if t.Mode != oldMode {
		if t.Workspace != caller.Workspace {
			return errors.New("select the task's original workspace before switching to an existing thread")
		}
		t.ThreadID = caller.ThreadID
		t.CWD = caller.CWD
	}
	return nil
}

func scheduleInteger(value any) (int, error) {
	var n float64
	switch value := value.(type) {
	case float64:
		n = value
	case int:
		n = float64(value)
	case int64:
		n = float64(value)
	case json.Number:
		var err error
		n, err = value.Float64()
		if err != nil {
			return 0, err
		}
	default:
		return 0, errors.New("must be an integer")
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n < 0 || n > 31536000 {
		return 0, errors.New("must be an integer between 0 and 31536000")
	}
	return int(n), nil
}

func scheduledTaskToolView(t scheduledtasks.Task) map[string]any {
	return map[string]any{"task_id": t.ID, "name": t.Name, "prompt": t.Prompt, "workspace": t.Workspace, "cwd": t.CWD, "thread_id": t.ThreadID, "mode": t.Mode, "kind": t.Kind, "time_zone": t.TimeZone, "daily_time": t.DailyTime, "interval_seconds": t.IntervalSeconds, "at": t.At, "next_run_at": t.NextRunAt, "enabled": t.Enabled}
}
func scheduleToolError(code string, err error) *toolError {
	return &toolError{Code: code, Message: err.Error()}
}
func scheduleStoreToolError(err error) *toolError {
	if errors.Is(err, scheduledtasks.ErrNotFound) {
		return scheduleToolError("schedule_not_found", errors.New("task not found in this user's conversation"))
	}
	return scheduleToolError("schedule_failed", err)
}

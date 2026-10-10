package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/app/scheduledtasks"
	"github.com/kxn/codex-remote-feishu/internal/core/orchestrator"
)

// Access is serialized by App.mu. Slow outbound delivery releases that lock;
// inFlight prevents another tick from submitting or reporting the same run.
type scheduledTaskRuntime struct {
	store                *scheduledtasks.Store
	nextTick, nextNotify time.Time
	notifyRetry          map[string]time.Time
	inFlight             bool
}

func (a *App) scheduledTaskStoreLocked() (*scheduledtasks.Store, error) {
	if a.scheduledTasks.store != nil {
		return a.scheduledTasks.store, nil
	}
	dir := strings.TrimSpace(a.headlessRuntime.Paths.StateDir)
	if dir == "" {
		return nil, errors.New("local schedule state directory is unavailable")
	}
	store, err := scheduledtasks.NewStore(filepath.Join(dir, "scheduled-tasks.sqlite"))
	if err != nil {
		return nil, err
	}
	if err = store.Recover(time.Now()); err != nil {
		_ = store.Close()
		return nil, err
	}
	a.scheduledTasks.store = store
	return store, nil
}

func (a *App) maybeScheduleTasksLocked(ctx context.Context, now time.Time) {
	rt := &a.scheduledTasks
	if rt.inFlight || now.Before(rt.nextTick) || strings.TrimSpace(a.headlessRuntime.Paths.StateDir) == "" {
		return
	}
	rt.nextTick = now.Add(time.Second)
	rt.inFlight = true
	defer func() { rt.inFlight = false }()
	store, err := a.scheduledTaskStoreLocked()
	if err != nil {
		log.Printf("scheduled tasks: store unavailable: %v", err)
		rt.nextTick = now.Add(30 * time.Second)
		return
	}
	if _, err = store.ReserveDue(now, 8); err != nil {
		log.Printf("scheduled tasks: reservation failed: %v", err)
		return
	}
	runs, err := store.OutstandingRuns()
	if err != nil {
		log.Printf("scheduled tasks: read runs: %v", err)
		return
	}
	for _, run := range runs {
		if a.shuttingDown {
			return
		}
		if run.Status == scheduledtasks.StatusPending {
			result := a.service.TryEnqueueScheduledPrompt(orchestrator.ScheduledPromptRequest{
				RunID: run.ID, GatewayID: run.Task.GatewayID, SurfaceID: run.Task.SurfaceID,
				ChatID: run.Task.ChatID, ActorUserID: run.Task.ActorUserID,
				Workspace: run.Task.Workspace, CWD: run.Task.CWD, ThreadID: run.Task.ThreadID, Prompt: run.Task.Prompt, Mode: run.Task.Mode,
			})
			if result.QueueItemID == "" {
				// Persist only on change; an offline route should not write every second.
				if run.Error != result.WaitReason {
					run.Error = result.WaitReason
					if err = store.SaveRun(run); err != nil {
						log.Printf("scheduled tasks: save wait: %v", err)
					}
				}
				continue
			}
			run.Status, run.Error = scheduledtasks.StatusRunning, ""
			run.QueueItemID, run.InstanceID = result.QueueItemID, result.InstanceID
			run.CommandID = "relay-schedule-" + run.ID
			run.StartedAt = now
			// Durable acceptance precedes any external command. Restart recovery does
			// not replay accepted runs, even if the native process outlived us.
			if err = store.SaveRun(run); err != nil {
				if !a.service.AbortScheduledRunBeforeDispatch(run.ID) {
					log.Printf("scheduled tasks: CRITICAL cannot roll back unsaved acceptance run=%s", run.ID)
				}
				log.Printf("scheduled tasks: persist acceptance failed: %v", err)
				return
			}
			a.handleUIEventsLocked(ctx, result.Events)
		}
		a.reconcileScheduledRunLocked(store, run, now)
	}
	if !now.Before(rt.nextNotify) {
		rt.nextNotify = now.Add(10 * time.Second)
		a.reportScheduledRunsLocked(ctx, store)
	}
}

func (a *App) reconcileScheduledRunLocked(store *scheduledtasks.Store, run scheduledtasks.Run, now time.Time) {
	outcome := a.service.ScheduledRunOutcome(run.ID)
	if !outcome.Found {
		run.Status, run.Error = scheduledtasks.StatusInterrupted, "accepted run lost its live queue; result unknown, not replayed"
		run.CompletedAt = now
	} else if outcome.Terminal {
		run.Result, run.Error = outcome.ResultText, outcome.ResultError
		run.ThreadID, run.TurnID = outcome.ThreadID, outcome.TurnID
		run.CompletedAt = now
		switch outcome.TerminalCause {
		case "completed":
			run.Status = scheduledtasks.StatusSucceeded
		case "user_interrupted":
			run.Status = scheduledtasks.StatusInterrupted
		case "cancelled":
			run.Status = scheduledtasks.StatusCancelled
		case "unknown_terminal":
			run.Status, run.Error = scheduledtasks.StatusInterrupted, "terminal queue has no model completion evidence; result unknown"
		default:
			run.Status = scheduledtasks.StatusFailed
			if run.Error == "" {
				run.Error = outcome.TerminalCause
			}
		}
	} else {
		note := ""
		if outcome.Status == "queued" {
			note = "waiting for writer or queue"
		}
		if outcome.Status == "dispatching" {
			note = "waiting for model turn to start"
		}
		changed := run.Error != note
		run.Error = note
		turns := append(a.service.PendingRemoteTurns(), a.service.ActiveRemoteTurns()...)
		for _, turn := range turns {
			if turn.QueueItemID != run.QueueItemID || turn.InstanceID != run.InstanceID {
				continue
			}
			if turn.CommandID != run.CommandID || turn.ThreadID != run.ThreadID || turn.TurnID != run.TurnID {
				run.CommandID, run.ThreadID, run.TurnID = turn.CommandID, turn.ThreadID, turn.TurnID
				changed = true
			}
			break
		}
		if !changed {
			return
		}
	}
	if err := store.SaveRun(run); err != nil {
		log.Printf("scheduled tasks: save outcome run=%s: %v", run.ID, err)
	}
}

func (a *App) reportScheduledRunsLocked(ctx context.Context, store *scheduledtasks.Store) {
	runs, err := store.UnnotifiedRuns()
	if err != nil {
		log.Printf("scheduled tasks: read outbox: %v", err)
		return
	}
	// Bound network work per tick; failures retain the outbox only, never resubmit.
	if a.scheduledTasks.notifyRetry == nil {
		a.scheduledTasks.notifyRetry = map[string]time.Time{}
	}
	attempts := 0
	for _, run := range runs {
		if a.shuttingDown {
			return
		}
		if time.Now().Before(a.scheduledTasks.notifyRetry[run.ID]) {
			continue
		}
		if attempts >= 4 {
			break
		}
		attempts++
		receiveID, receiveType := feishu.ResolveReceiveTarget(run.Task.ChatID, run.Task.ActorUserID)
		operation := feishu.Operation{
			Kind: feishu.OperationSendText, GatewayID: run.Task.GatewayID,
			SurfaceSessionID: run.Task.SurfaceID, ChatID: run.Task.ChatID,
			ReceiveID: receiveID, ReceiveIDType: receiveType,
			Text: scheduledRunReport(run),
		}
		applyCtx, cancel := a.newTimeoutContext(ctx, a.gatewayApplyTimeout)
		gateway := a.gateway
		a.mu.Unlock()
		err = gateway.Apply(applyCtx, []feishu.Operation{operation})
		cancel()
		a.mu.Lock()
		if err != nil {
			a.scheduledTasks.notifyRetry[run.ID] = time.Now().Add(30 * time.Second)
			log.Printf("scheduled tasks: notify failed run=%s: %v", run.ID, err)
			continue
		}
		run.Notified = true
		if err = store.SaveRun(run); err != nil {
			log.Printf("scheduled tasks: save notification run=%s: %v", run.ID, err)
		} else {
			delete(a.scheduledTasks.notifyRetry, run.ID)
		}
	}
}

func scheduledRunReport(run scheduledtasks.Run) string {
	status := map[string]string{"succeeded": "完成", "failed": "失败", "interrupted": "中断（不自动重放）", "cancelled": "已取消"}[run.Status]
	text := fmt.Sprintf("定时任务：%s\n任务：%s\n运行：%s\n状态：%s", run.Task.Name, run.TaskID, run.ID, status)
	if run.ThreadID != "" {
		text += "\n会话：" + run.ThreadID
	}
	if run.Error != "" {
		text += "\n原因：" + run.Error
	}
	if run.Result != "" {
		text += "\n\n" + run.Result
	}
	chars := []rune(text)
	if len(chars) > 6000 {
		text = string(chars[:6000]) + "\n（内容较长，完整结果保存在本机任务记录中）"
	}
	return text
}

// Pause/delete may remove an accepted but unsent queue item. A command already
// sent to the agent continues, even if its turn-start event has not arrived yet.
func (a *App) cancelWaitingScheduledTaskRunsLocked(owner, taskID string, now time.Time) error {
	store, err := a.scheduledTaskStoreLocked()
	if err != nil {
		return err
	}
	runs, err := store.OutstandingRuns()
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.Owner != owner || run.TaskID != taskID || run.QueueItemID == "" {
			continue
		}
		if !a.service.AbortScheduledRunBeforeDispatch(run.ID) {
			continue
		}
		run.Status, run.CompletedAt, run.Error = scheduledtasks.StatusCancelled, now, "cancelled before dispatch"
		if err = store.SaveRun(run); err != nil {
			return err
		}
	}
	return nil
}

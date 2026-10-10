package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/scheduledtasks"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

func TestScheduledTasksCommandScopesOwnerAndPreservesRunningRound(t *testing.T) {
	app := New(":0", ":0", nil, agentproto.ServerIdentity{StartedAt: time.Now().UTC()})
	app.headlessRuntime.Paths.StateDir = t.TempDir()
	store, err := app.scheduledTaskStoreLocked()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	task, err := store.Create(scheduledtasks.Task{Owner: scheduledTaskOwner("g", "s", "a"), GatewayID: "g", SurfaceID: "s",
		ChatID: "chat", ActorUserID: "a", Name: "设备检查", Prompt: "只读检查", Workspace: "/tmp/device", ThreadID: "thread",
		Mode: scheduledtasks.ModeExisting, Kind: scheduledtasks.KindInterval, TimeZone: "Asia/Shanghai", IntervalSeconds: 60, Enabled: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	command := control.DaemonCommand{Kind: control.DaemonCommandSchedule, GatewayID: "g", SurfaceSessionID: "s", ActorUserID: "b", Text: "/schedule list"}
	events := app.handleScheduledTasksCommandLocked(command)
	if len(events) != 1 || events[0].Notice == nil || strings.Contains(events[0].Notice.Text, task.ID) {
		t.Fatalf("another actor can see task: %#v", events)
	}
	command.Text = "/schedule delete " + task.ID
	events = app.handleScheduledTasksCommandLocked(command)
	if events[0].Notice.Code != "schedule_delete_failed" {
		t.Fatalf("another actor deleted task: %#v", events)
	}
	runs, err := store.ReserveDue(now.Add(61*time.Second), 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("reserve: %#v %v", runs, err)
	}
	run := runs[0]
	run.Status = scheduledtasks.StatusRunning
	if err = store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	command.ActorUserID = "a"
	command.Text = "/schedule pause " + task.ID
	events = app.handleScheduledTasksCommandLocked(command)
	if events[0].Notice.Code != "schedule_updated" {
		t.Fatalf("pause: %#v", events)
	}
	run, err = store.GetRun(run.ID)
	if err != nil || run.Status != scheduledtasks.StatusRunning {
		t.Fatalf("pause interrupted active round: %#v %v", run, err)
	}
	command.Text = "/schedule list"
	events = app.handleScheduledTasksCommandLocked(command)
	if !strings.Contains(events[0].Notice.Text, "最近执行：running") || !strings.Contains(events[0].Notice.Text, "状态：暂停") {
		t.Fatalf("list lost pause/run state: %s", events[0].Notice.Text)
	}
}

func TestScheduledTasksCommandRejectsMissingActorAndInvalidArguments(t *testing.T) {
	app := &App{}
	for _, text := range []string{"/schedule pause", "/schedule pause task extra", "/schedule unknown", "/schedule list extra"} {
		events := app.handleScheduledTasksCommandLocked(control.DaemonCommand{Text: text})
		if events[0].Notice.Code != "schedule_usage" {
			t.Fatalf("%q accepted: %#v", text, events)
		}
	}
	events := app.handleScheduledTasksCommandLocked(control.DaemonCommand{Text: "/schedule list", GatewayID: "g", SurfaceSessionID: "s"})
	if events[0].Notice.Code != "schedule_owner_missing" {
		t.Fatalf("missing owner accepted: %#v", events)
	}
	events = app.handleScheduledTasksCommandLocked(control.DaemonCommand{Text: "/schedule"})
	if events[0].Notice.Code != "schedule_help" || !strings.Contains(events[0].Notice.Text, "每轮新建会话") {
		t.Fatalf("help unavailable: %#v", events)
	}
}

package daemon

import (
	"fmt"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/scheduledtasks"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
)

const scheduledTasksHelp = "聊天定时任务（本机持久化）\n" +
	"先在 Codex headless 工作区选择会话，再直接发送自然语言：\n" +
	"• 每 30 分钟在当前已有会话检查设备状态，每轮回报结果。\n" +
	"• 每天 09:00 每轮新建会话检查服务，时区 Asia/Shanghai。\n" +
	"• 5 分钟后在当前会话检查一次日志。\n" +
	"创建、修改和查询会调用 schedule 工具；确认回复中的任务 ID、时间、工作区和会话。\n" +
	"/schedule list：查看自己在当前聊天创建的任务与最近执行。\n" +
	"/schedule pause <任务 ID>：暂停未来轮次和等待中的轮次，已开始的轮次继续。\n" +
	"/schedule resume <任务 ID>：从现在重新计算下一轮时间。已过期的一次性任务需先修改执行时间。\n" +
	"/schedule delete <任务 ID>：删除任务，已开始的轮次继续；/stop 可停止当前执行。\n" +
	"默认时区 Asia/Shanghai；支持间隔、每天定时、一次性。切换会话不会改变任务目标；目标工作区不可用或会话被占用时等待，错过的周期不会堆积。"

func (a *App) handleScheduledTasksCommandLocked(command control.DaemonCommand) []eventcontract.Event {
	reply := func(code, text string) []eventcontract.Event {
		return []eventcontract.Event{{Kind: eventcontract.KindNotice, GatewayID: command.GatewayID,
			SurfaceSessionID: command.SurfaceSessionID, SourceMessageID: command.SourceMessageID,
			Notice: &control.Notice{Code: code, Title: "聊天定时任务", Text: text}}}
	}
	fields := strings.Fields(strings.TrimSpace(command.Text))
	if len(fields) == 0 || !strings.EqualFold(fields[0], "/schedule") {
		return reply("schedule_usage", scheduledTasksHelp)
	}
	if len(fields) == 1 || (len(fields) == 2 && strings.EqualFold(fields[1], "help")) {
		return reply("schedule_help", scheduledTasksHelp)
	}
	mode := strings.ToLower(fields[1])
	if (mode == "list" && len(fields) != 2) ||
		(mode != "list" && (len(fields) != 3 || (mode != "pause" && mode != "resume" && mode != "delete"))) {
		return reply("schedule_usage", "不支持的命令或参数。\n"+scheduledTasksHelp)
	}
	if strings.TrimSpace(command.GatewayID) == "" || strings.TrimSpace(command.SurfaceSessionID) == "" || strings.TrimSpace(command.ActorUserID) == "" {
		return reply("schedule_owner_missing", "无法确定当前聊天与发起用户，请重新发送 /schedule 命令。")
	}
	store, err := a.scheduledTaskStoreLocked()
	if err != nil {
		return reply("schedule_store_failed", fmt.Sprintf("任务存储不可用：%v", err))
	}
	owner := scheduledTaskOwner(command.GatewayID, command.SurfaceSessionID, command.ActorUserID)
	if mode == "list" {
		tasks, err := store.List(owner)
		if err != nil {
			return reply("schedule_list_failed", fmt.Sprintf("读取任务失败：%v", err))
		}
		if len(tasks) == 0 {
			return reply("schedule_list", "你在当前聊天还没有定时任务。\n"+scheduledTasksHelp)
		}
		var lines []string
		for _, task := range tasks {
			line := scheduledTaskSummary(task)
			runs, err := store.RunHistory(owner, task.ID, 1)
			if err != nil {
				return reply("schedule_list_failed", fmt.Sprintf("读取执行记录失败：%v", err))
			}
			if len(runs) > 0 {
				run := runs[0]
				line += "\n最近执行：" + run.Status
				if run.Error != "" {
					line += "（" + run.Error + "）"
				}
			}
			lines = append(lines, line)
		}
		return reply("schedule_list", strings.Join(lines, "\n\n"))
	}
	id := fields[2]
	if mode == "delete" {
		if err = store.Delete(owner, id); err != nil {
			return reply("schedule_delete_failed", fmt.Sprintf("删除失败（请使用自己在当前聊天的任务 ID）：%v", err))
		}
		if err = a.cancelWaitingScheduledTaskRunsLocked(owner, id, time.Now()); err != nil {
			return reply("schedule_delete_failed", fmt.Sprintf("任务已删除，撤销等待轮次失败：%v", err))
		}
		return reply("schedule_deleted", "已删除任务 "+id+"。未来和等待轮次已取消；已经开始的轮次继续执行。")
	}
	task, err := store.SetEnabled(owner, id, mode == "resume", time.Now().UTC())
	if err != nil {
		return reply("schedule_update_failed", fmt.Sprintf("操作失败（请核对自己的任务 ID；一次性任务须使用未来执行时间）：%v", err))
	}
	if mode == "pause" {
		if err = a.cancelWaitingScheduledTaskRunsLocked(owner, id, time.Now()); err != nil {
			return reply("schedule_update_failed", fmt.Sprintf("任务已暂停，撤销等待轮次失败：%v", err))
		}
	}
	text := "已暂停任务。未来和等待轮次已取消；已经开始的轮次继续执行。"
	if mode == "resume" {
		text = "已恢复任务。"
	}
	return reply("schedule_updated", text+"\n"+scheduledTaskSummary(task))
}

func scheduledTaskSummary(task scheduledtasks.Task) string {
	status := "暂停"
	if task.Enabled {
		status = "启用"
	}
	target := "每轮新建会话"
	if task.Mode == scheduledtasks.ModeExisting {
		target = "已有会话 " + task.ThreadID
	}
	zone, err := time.LoadLocation(task.TimeZone)
	if err != nil {
		zone = time.UTC
	}
	next := "无"
	if task.Enabled && !task.NextRunAt.IsZero() {
		next = task.NextRunAt.In(zone).Format("2006-01-02 15:04:05 MST")
	}
	return fmt.Sprintf("%s\nID：%s\n状态：%s\n下一轮：%s\n工作区：%s\n目标：%s", task.Name, task.ID, status, next, task.Workspace, target)
}

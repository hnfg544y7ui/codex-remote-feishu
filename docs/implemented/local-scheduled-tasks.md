# 飞书聊天本机定时任务

> Type: `implemented`
> Updated: `2026-10-10`
> Summary: 增加独立本机调度器、自然语言 MCP 工具、任务管理命令和持久运行回报。

## 使用

适用于 Codex headless 工作区。在飞书先选中工作区与会话，再直接发送：

- “1 分钟后在当前会话检查一次系统时间，只读操作，完成后告诉我。”
- “每 30 分钟在当前已有会话检查服务状态，每轮回报结果。”
- “每天 09:00 检查设备状态，每轮新建一个会话，使用北京时间。”
- “把任务 task_… 改成每 10 分钟运行。”
- “查询我在这个聊天创建的定时任务。”

模型使用 `schedule_create`、`schedule_update`、`schedule_list`、`schedule_pause`、`schedule_delete`。
成功创建必须返回真实任务 ID、下一次时间、工作区与会话模式；只有工具调用成功才能宣称已经创建。
已有会话模式绑定调用工具的真实 turn，选择另一个已有会话请先 `/use`。
默认时区 `Asia/Shanghai`，支持 `interval`（最短 10 秒）、`daily`（当地 HH:MM）、`once`（带时区 RFC3339）。

不依赖模型的管理入口：

```text
/schedule
/schedule list
/schedule pause task_…
/schedule resume task_…
/schedule delete task_…
```

暂停或删除会撤销尚未发送到 agent 的轮次。已经发送的轮次允许完成，需要中断当前运行时使用 `/stop`。
恢复间隔任务从当前时间重新计时；已经过去的一次性任务须先改成未来时间。
任务与执行记录存放在 daemon StateDir 的 `scheduled-tasks.sqlite`，恢复时沿用同一 StateDir。
这套任务与旧 `/cron` 的飞书多维表格任务各自独立，不自动导入旧任务。

## 状态与执行边界

1. 创建者来自原始用户 queue item，权限键为 GatewayID、SurfaceID、ActorUserID 元组。群内其他用户不会继承创建者权限。
2. 工作区根、执行 CWD、会话模式和回报地址分别持久化。用户换会话不会把任务改到新会话。
   修改频率或重复传入相同 mode 保留原目标；改成每轮新建仍保留原工作区。由新建改为已有会话时，须先进入任务原工作区，才能绑定调用 turn。
3. SQLite 事务预订到期轮次，唯一索引限定每任务最多一个 `pending/running`。漏过周期合并，不追赶全部历史轮次。
4. 每轮保存不可变 Task 快照。修改任务影响以后预订的轮次；暂停和删除额外撤销未发送轮次。
5. 复用现有实例队列与 writer 仲裁；相同实例串行，其他聊天 claim 或另一个在线 writer 占用目标时等待。
6. `existing` 恢复冻结线程；`new_each_run` 建立真实、持久的新线程，并保留聊天当前选择。任务不消费用户暂存附件。
7. `running` 表示 daemon 已接受到实例队列，可能仍在排队；非空 turn ID 才是已观察到模型开始。
8. 调用 wire command 前先保存接受记录。命令 ID 由 run ID 关联，发送前验证 live queue/binding；已取消或陈旧命令不会回退到聊天的新实例。
9. 捕获真实完成、中断和失败，不把队列 completed 等同于成功；定时轮次不触发 AutoContinue/AutoWhip。
10. daemon 重启将接受中的运行标为 `interrupted`，明确结果未知，不重放；未接受的 pending 保留。
11. 回报通过现有 gateway/broker 向原聊天发送。发送失败只重试通知，不重新运行任务。回报属于至少一次交付；远端已接收但本地未保存确认时可能重复回报。

## 第一版限制和恢复动作

- 自动恢复不能替用户抢占其他聊天的工作区。原 surface 离线或切到另一个工作区时，尚未接受的任务等待；回到原工作区后继续。若已入队后原实例/工作区改变，未发送轮次会明确失败，绝不写入新目标。
- 原工作区实例需要在线；本版不增加另一套后台 headless 启动器。运行中可在原聊天查询 `/schedule list`、暂停或删除；实例断线后先通过 `/workspace` 或 `/use` 恢复。
- 本版每轮发送执行结果回报。尚未提供独立的“仅异常通知”、每周规则或跨主机调度迁移。
- 没有新增自动执行超时。运行等待用户审批时，审批或 `/stop` 是退出动作；同一任务后续轮次合并，不并发追加。
- 仅 Codex headless 支持创建和执行；VS Code、Claude、OpenCode 不冒充具备此调度能力。

## 验证

Store 测试覆盖权限隔离、事务回滚、单个 outstanding run、重启恢复、每日时区与 DST、暂停和删除。
Orchestrator 测试覆盖目标冻结、附件隔离、writer 竞争、陈旧命令、真实终态，以及不触发自动续跑。
Daemon 测试覆盖自然语言工具身份、MCP 注册、管理命令、持久化先于 wire、取消未发送任务、未知运行不重放和通知失败不重复执行。

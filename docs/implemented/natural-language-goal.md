# 飞书自然语言 Goal

> Type: `implemented`
> Updated: `2026-10-12`
> Summary: 记录明确自然语言 Goal 控制、当前会话约束、确认卡绑定与隔离协议验收边界。

## 使用前提

当前飞书会话需要选择一个在线 Codex headless 会话，并持有该 thread 的 claim；可先用 `/use` 选择。未选中、目标不存在、已由其他飞书会话接管、Review/helper 会话或其他后端都会拒绝 Goal 操作。

上游 Codex CLI 必须支持 `thread/goal/set`、`thread/goal/get` 和 `thread/goal/clear`。Goal 状态及续跑由 Codex 后端管理，Remote 调用已有 RPC；它不是 Remote 的 `/cron` 定时任务。

验收所用 CLI 还要求在后端读取的 `CODEX_HOME/config.toml` 中开启 Goal：

```toml
[features]
goals = true
```

该版本在 `goals=false` 时明确返回 `goals feature is disabled`；Remote 的 `experimentalApi=true` 不会代替这一配置。共享模式使用外部后端的配置文件，修改 Remote 自己的配置不会改变它，配置变更应由宿主管理生效。本分支未自动修改任何运行中的后端配置。

共享后端继续使用已有显式 WebSocket 配置：Remote 的 `wrapper.codexAppServerURL` 或 `CODEX_REMOTE_CODEX_APP_SERVER_URL`，以及桌面 App 的 `CODEX_APP_SERVER_WS_URL`。不会自动探测已有桌面后端，也不能热接管其私有 stdio 实例。地址、账号和生命周期约束见 [共享 Codex 后端](shared-codex-backend.md)。本功能未新增 MCP Goal 工具；共享连接也不会继承其他客户端的私有工具能力。

## 六种操作

在当前机器人聊天中直接发送以下纯文本：

| 操作 | 示例 | 结果 |
| --- | --- | --- |
| 创建 | `创建Goal：完成登录重构，预算5万tokens` | 设置目标及 50,000 token 预算 |
| 修改 | `修改Goal：完成登录重构并补充测试，预算6万tokens` | 更新当前会话的目标及预算 |
| 查看 | `查看当前Goal` | 读取并显示当前 Goal 状态 |
| 暂停 | `暂停当前Goal` | 暂停 Goal 的后续续跑 |
| 恢复 | `恢复当前Goal` | 显式恢复当前 Goal |
| 清除 | `清除当前Goal` | 打开确认卡，点击确认后才清除 |

明确英文句式也可用，例如 `create goal: finish login refactoring, budget 50k tokens`、`show current goal`、`pause current goal`、`resume current goal`、`clear current goal`。

预算是正整数 token 数，支持 `k`、`千`、`万` 后缀。省略预算时不额外指定新的预算；保留既有 `/goal` 命令与菜单入口，可以用 `/goal` 查看状态或打开表单。

暂停控制后续 Goal 续跑，不保证中断已经开始的 turn。如需结束当前正在执行的工作，应使用现有中断操作。

## 识别边界

首版只接收可信用户的完整、明确、单行纯 `text` 控制消息。入口生成 typed `GoalIntent`，orchestrator 再核对原文字和 intent，并通过与 `/goal` 相同的 `user_control` RPC 路径执行；这些明确控制不进入普通 prompt 队列。

模糊表达、否定或条件句、引用句、代码块、图片/附件和富文本 `post` 不转换为自然语言 Goal 控制。例如“不要暂停Goal”“如果需要就清除Goal”“能不能创建Goal？”以及引用的示例文字都不会被这个控制入口执行。未匹配的消息沿用既有普通消息或提示流程，不应把它们视为控制命令已成功。

此入口不依赖模型解释文本，不解析模型或工具输出作为用户控制，也不向模型注入新的 Goal MCP 工具。

## 确认、表单与旧卡

清除确认卡及创建/编辑表单复用已有命令卡和 `CommandText` 重放。服务器生成一次性 opaque context，绑定：

- 飞书 surface 和用户 actor；
- 原 instance、thread 和操作类型；
- Goal 是否存在，以及 createdAt、目标描述和 token budget 快照。

上下文有效期为 10 分钟，提交时消费一次，校验失败也不能再次使用。切换实例或会话、其他用户提交、目标或预算变化、过期及重放均拒绝执行。请重新打开当前会话的 Goal 页面后操作。内部上下文由卡片生成，用户无需手工输入。

RPC 结果还会核对原 instance/thread。操作后切换会话，迟到的原会话回应不会被显示为新会话的 Goal，只提示刷新当前状态。

## 与普通队列互锁的关系

普通用户任务暂时暂停 Goal 时，Remote 持有可恢复的互锁 lease。用户真正发出创建/修改、暂停、恢复或确认清除后，只撤销匹配 instance+thread 的自动恢复权限；查询、打开表单及尚未确认的清除不会撤销。

撤销保留 `pause_pending`、`quiescing`、`draining` 屏障及 pending command 关联。用户 RPC 或交付失败不重新授予权限，迟到的状态 get 或暂停/probe 回包也不重新开启自动恢复。撤销后的队列在原屏障内推进，排空后不发送自动恢复；恢复流程不会重发旧 pause 去暂停用户的新 Goal。

如果自动 resume RPC 在用户修改之前已经发出，Remote 无法撤回它，只能等待原 ack，并禁止再次发出自动恢复。ack 丢失时保持 fail-closed，不根据目标指纹或一次查询猜测 RPC 已完成。可通过现有 detach/use 重新接管精确会话；移除实例也会清理相关 lease。重新接管前应查看后端当前 Goal 状态，避免将清理本地互锁误认为已取消后端 Goal。

## 验证与发布状态

仓库回归覆盖文本入口、精确 thread claim、typed intent 校验、上下文漂移/重放/过期、用户修改与查询的授权区别、迟到回包、交付/RPC 失败及共享协议归属。共享协议回归载体包括 `translator_shared_goal_test.go` 和 `app_shared_goal_protocol_test.go`。

另使用官方 Codex CLI `0.162.0-alpha.17.2`，在隔离 `CODEX_HOME`、两个客户端及本地 mock model endpoint 下验证了实际 `thread/goal/set|get|clear`：创建 active Goal 后出现两轮自主 turn，两个客户端观察到相同 turn；暂停后不再产生新轮次，清除后查询返回无 Goal。该检查未复制生产账号、未请求真实模型、未修改生产 Goal。

这证明了该版本 CLI 的隔离协议行为与本地 mock 续跑，不代表已经完成真实模型、桌面 UI 或生产飞书全链路验收。本分支尚未部署、创建 Release 或版本标签；Changelog 中的未发布版本规划保持不变。

禁用 Goal 的隔离检查也验证了上述原生拒绝行为，未发送任何本地模拟或真实模型请求。

卡片与状态机规范分别见 [飞书卡片 UI 状态机](../general/feishu-card-ui-state-machine.md) 和 [Remote Surface 状态机](../general/remote-surface-state-machine.md)。

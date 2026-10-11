# Remote 托管官方 Codex CLI

> Type: `implemented`
> Updated: `2026-10-11`
> Summary: 记录独立 Codex CLI 的配置、候选验证、空闲切换、飞书回退和离线维护边界。

## 当前事实与规范来源

本功能保持官方 Codex CLI 原样，由 Remote 管理私有安装和版本选择。CLI 程序与账号、会话数据分开管理，不依赖 ChatGPT App 或 VS Code 扩展路径才能启动。

本文描述 `codex/feat-managed-codex-runtime` 分支代码和 NanoPi 部署验证；飞书客户端的实际点击与对话体验仍需单独验收。路由、队列和恢复的 canonical source 是 [远程面状态机](../general/remote-surface-state-machine.md)，卡片和 callback 的 canonical source 是 [飞书 UI 状态机](../general/feishu-card-ui-state-machine.md)。

## 配置与兼容边界

在既有 `config.json` 的 `wrapper` 中添加 `codexRuntime`；保留原有账号和机器人配置：

```json
{
  "wrapper": {
    "codexRuntime": {
      "source": "managed",
      "channel": "stable",
      "autoUpdate": false
    }
  }
}
```

| 字段 | 当前含义 |
| --- | --- |
| `source: managed` | Remote 独立托管；启动不先验证 `codexRealBinary`、PATH 或 App 是否存在 |
| `source: external` | 使用用户配置的外部 CLI；不触发私有自动安装，也不把 VS Code 路径自愈成别的入口 |
| source 缺省、空或 `legacy` | 保留原有 App/PATH/VS Code 解析及旧私有更新行为；不会自动迁移为新模式 |
| `channel` | `stable` 或 `alpha`；省略时托管安装库按 stable 处理 |
| `version` | 精确官方 CLI 三段版本，可带预发布后缀；不是 Remote 自己的四段版本号；设置 pin 后不执行自动更新 |
| `autoUpdate` | 默认 false；开启后允许后台准备候选，并在受影响窗口空闲后切换 |

环境变量 `CODEX_REMOTE_CODEX_RUNTIME_SOURCE`、`CODEX_REMOTE_CODEX_RUNTIME_CHANNEL`、`CODEX_REMOTE_CODEX_RUNTIME_VERSION`、`CODEX_REMOTE_CODEX_RUNTIME_AUTO_UPDATE` 优先于 JSON。显式空 version 清除本次运行的 pin，`false` 关闭自动更新；覆盖值不写回配置。修改服务配置后需重启对应 daemon，使启动快照生效。

daemon 把已经解析的 source/channel/version/autoUpdate 通过环境传给它启动的 wrapper，冻结同一份启动策略。运行中改 JSON 不会让新 pool worker 使用另一来源，而 daemon 仍按旧来源升级；真实 CLI 凭据环境不因此改变。

新模式不会移动或重写用户的 `CODEX_HOME`、CLI 账号和历史会话。托管 CLI 也不会自动获得桌面 App 专有工具回调能力。源码中的 `Managed` 实例标记仍表示 daemon 对工作进程的所有权，不等同于 `codexRuntime.source`。

## 安装与持久版本选择

私有根目录是 `Paths.StateDir/codex-runtime`。官方 npm 包按平台安装到不可覆盖的 `releases/<version>` 目录；安装/更新需要可用的 Node/npm 及安装源网络。安装文件先在临时目录构建，验证后原子发布，不能把半成品目录作为候选。

`managed.json` 是唯一版本选择依据：

| 状态字段 | 含义 |
| --- | --- |
| `active` | 工作进程应使用的版本；wrapper 只解析它，不按目录排序选择最高版 |
| `previous` | 可验证的上一版；成功升级保留原 active，供明确回退使用 |
| `candidate` | 已准备的目标包；存在 candidate 不代表已切换工作进程 |
| `failed` | 失败/拒绝版本及原因、时间；自动流程不重试此版本 |

旧私有目录若没有新 manifest，可在明确准备该精确版本时验证后接入；不靠扫描目录隐式选择 active。无 active 时 daemon 首次启动负责安装、能力探测和激活。已有 active 时验证其组件与实际 CLI 版本；失效则明确报错，不自动回退到 App 或另一目录。

## 验证与状态转换

准备阶段检查 CLI 实际 `--version`，并要求完整平台组件：CLI、code mode host；Windows 另有 command runner 和 sandbox setup，Linux另有 `bwrap`。manifest 保存各必需组件 SHA-256；激活、回退及复用既有安装时检查组件完整性与校验和。普通 wrapper 启动检查组件存在及版本，不每次重新下载或重算整包哈希。

候选必须通过隔离 capability preflight：临时 `CODEX_HOME`、临时工作目录和 dummy provider，执行 `initialize`、`config/read`、`thread/start`，验证 Remote 所需协议。它不发送 `turn/start`，不进行模型推理，也不验证真实账号、代理和模型服务可用性。真实会话的认证/网络故障不能仅凭该探测结论归类为版本故障。

自动更新仅在 `managed + autoUpdate=true + 无 version pin` 下生效：

1. 每 15 分钟准备并验证候选；准备失败保留 active。
2. 每 30 秒检查能否激活已有候选。受影响 Codex 工作进程有 active/pending turn、请求、待处理派发等 blocker 时继续等待，不中断现有任务。
3. 事务开始时冻结受影响窗口的派发，记录要重启的在线 managed Codex 实例。VS Code 集成和非 Codex backend 不参与新托管事务。
4. 激活目标后逐个执行原 `process.child.restart`，等待新 child 接管的 ack，以及独立 thread restore outcome；bare ack 不代表升级成功。
5. 成功才结束事务。激活后的 worker 恢复失败会使用独立恢复 deadline 尝试重新激活原版本并恢复 worker，拒绝失败目标，最后解除输入暂停并报告失败。

自动流程还有本进程候选授权：只有 daemon 的无 pin 后台 prepare 和 probe 成功才记录精确候选版本；自动确认要求这个记录与 candidate 匹配、channel 与启动配置一致，而且版本严格高于 active。CLI/manual prepare 不设置该授权，重启不会继承授权，需等下一轮后台准备。激活/恢复 active 后清掉记录。因而旧候选、手工指定候选及跨通道候选不会仅因目录存在而自动生效。

手动和自动切换共用该事务。发起窗口的普通输入在事务中被挡住；其他已 attach 的受影响窗口把文本、图片、文件写入原队列，待事务结束再派发。失败及超时也必须退出事务、释放暂停，不依赖本地活动 watchdog 代为解锁。

回退只改变 CLI 程序选择及工作进程，不回滚 CLI 数据库、账号、历史文件或新版本写入的数据格式。程序可回退并不证明数据能降级读取。

## 飞书使用

1. 发送 `/upgrade codex`。active managed 或可升级的外部 npm 安装可显示该入口；打开卡片不自动查安装源。
2. 点击“检查更新”。卡片显示来源、当前版本、最新版本；managed 有上一版时同时显示上一版本。
3. 有新版本且当前允许切换时，点击“确认升级”。确认时重新检查目标、工作进程和事务状态。
4. managed 检查完成且存在可回退版本时，点击“回退上一版”。即使当前已是最新版也可回退；确认再次验证精确 previous，不能 silent 改成另一版本。

回退后，被替换版本进入 failed，previous/candidate 清空，防止自动流程立即装回被拒绝版。需要重试时由操作者明确再次准备/确认，不通过自动更新绕过拒绝记录。

升级和回退复用原 owner card：`upgrade_owner_flow` 的业务字段仍为 `picker_id`、`option_id`，Codex option 为 `check`、`confirm`、`rollback`。同一张卡推进 checking、ready、running 和 sealed terminal。过期 flow、旧 daemon 生命周期、他人卡片、并发事务及不允许的 stage 均拒绝；终态不残留可执行按钮。

## 命令行维护

首次从 legacy 迁移时，先停止旧 daemon，再设置 managed 配置、准备和离线激活，最后启动 daemon。旧 legacy updater 仍会扫描同一个私有目录，不能在它运行时把新候选写入该目录后期待它遵守新 active 选择规则。

只读 status 可在线查询；已有 managed 模式下的 prepare 可在 daemon 运行时执行。请使用 daemon 所属用户及相同 XDG 路径：

```sh
codex-remote codex-runtime status
codex-remote codex-runtime prepare -channel stable
codex-remote codex-runtime prepare -channel alpha -version 0.135.0-alpha.1
codex-remote codex-runtime prepare -version 0.134.0 -retry-failed
```

上述版本只是命令格式示例，不代表安装源一定提供该版本。`prepare` 不切换 active。`-retry-failed` 是显式重试被拒绝版本的操作者选择。

离线激活必须先停止这个用户、这个 XDG 状态目录对应的 daemon，再执行：

```sh
codex-remote codex-runtime activate
codex-remote codex-runtime activate -version 0.134.0
```

activate 只允许切到已准备 candidate，并持有 daemon lock 直到隔离探测及激活结束；检测到 daemon 仍运行会拒绝。完成后使用原服务管理器启动 daemon。无需卸载或关闭 ChatGPT App；这个限制针对 Remote daemon 和它管理的 worker。

`-root` 可指定私有根目录，但 daemon 运行守卫仍读取当前用户的 `XDG_STATE_HOME`。维护自定义目录时必须匹配真实服务用户和 XDG 环境，不能把别的用户/实例目录作为绕过在线协调的入口。在线升级和回退应使用飞书事务，CLI 没有在线 `activate` 或独立 `rollback` 子命令。

## 审查与验收边界

- 版本选择、安装发布和元数据写入使用跨进程锁；读取 active 不持有长时间下载锁。
- 前台菜单/owner-card 的 Locked 路径不可再获取 App mutex。只读安装来源必须使用可安全读取的固定路径/启动快照，不以冻结卡片换取配置一致性。
- 检查/确认重复点击、旧 flow、过期 owner、非 owner、已有事务均需回归覆盖；gateway 与 projector 的字段合同没有改变。
- 本轮配置、wrapper、managed/owner focused 验证及 pre-commit gate 已通过。Windows 全量回归并非全绿：桌面 setup 状态、数据库清理占用和路径选择的代表性失败在原 develop 基线也能复现。NanoPi 新托管功能定向测试通过；全量套件受缺少 Go/源码夹具及旧 legacy 跨进程 JSON 锁竞态影响，不能声明全量通过。

## NanoPi 部署验证（2026-10-11）

- 目标为 NanoPi-R76S（ARM64），测试版 `v1.0.0.2-managed-runtime-test.20261011` 已经原生 local-upgrade 提交，服务启用、root linger 开启，飞书“linux工作台”已重连。退出部署 SSH 后再次核对，daemon 和两个工作进程仍在线；本轮没有重启整机。
- 配置只新增 `wrapper.codexRuntime = {source: managed, channel: stable, autoUpdate: false}`。原 `codexRealBinary` 外部路径保留，但实际两个 child 的 `/proc/<pid>/exe` 均指向新私有目录中的官方 CLI `0.162.1`。
- 原机器人配置、订阅登录、账号记录保留；迁移前已备份配置、安装状态、服务、凭据和 SQLite。定时任务数据库 `quick_check=ok`，前后都是 1 个任务、1 条运行记录。
- 临时 `CODEX_HOME` 内 app-server 初始化成功，`command/exec` 在只读沙箱返回退出码 0 和预定标记；随后使用原订阅凭据执行 ephemeral 模型测试，正常回复预定标记，网络配置未改。
- 第一次创建 managed active，没有 previous，因此暂不显示可回退按钮。以后成功切换到另一版才有上一版；候选失败及工作进程恢复失败的回退路径已经定向测试。
- 自动更新保持关闭。本次没有验证飞书客户端里实际点击 owner 卡，也没有把二进制回退测试等同于真实账号数据降级兼容验证。

## 待讨论取舍

- 飞书回退入口目前要求“检查更新”成功；安装源不可用时，尽管 start 的回退再次校验本身不需要网络，卡片仍不提供回退按钮。是否把本地回退能力与最新目录查询拆开需要另行决定；安全默认保留 active，必要时由操作者停 daemon 后离线维护。
- 暂时保留 npm 安装源和 Node/npm 依赖；是否加入官方原生发行包下载应独立评估平台组件布局与校验来源。
- 不自动清理旧不可变版本目录，避免删除尚有进程使用的程序；垃圾回收需另设安全条件。
- CLI 数据格式降级不做自动恢复。未来是否为特定数据库提供迁移前快照，应保留账号/会话数据所有权，不能与二进制回退混为一谈。

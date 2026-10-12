# 共享 Codex app-server 后端

> Type: `implemented`
> Updated: `2026-10-12`
> Summary: 增加显式 loopback WebSocket 接入、native 配置继承、连接归属与断线边界。

## 当前事实与规范来源

Remote 可以通过显式配置连接已经运行的 Codex app-server WebSocket 后端。桌面 App 也连接这个端点时，两端共用同一个后端进程和线程状态，不再为 Remote 另起一个持有同一会话写权限的私有 CLI。

本文说明功能配置与使用边界。远程面和飞书交互的 canonical source 分别为 [remote-surface-state-machine.md](../general/remote-surface-state-machine.md) 与 [feishu-card-ui-state-machine.md](../general/feishu-card-ui-state-machine.md)。独立托管 CLI 的维护方式见 [managed-codex-runtime.md](./managed-codex-runtime.md)。

共享接入没有默认开启。未配置共享 URL 时，原有私有 stdio 子进程路径保持不变。当前共享路径用于 Remote 管理的 headless/cron Codex 工作进程；桌面 App 直接连接后端，不通过这个 headless wrapper 启动。

## 会话历史

共享后端的 `/history` 不再一次请求完整会话。列表先读取线程元数据，再每页 50 轮读取全部轮次摘要；进入详情时每页 50 项读取所选轮次的完整条目。详情条目先校验 envelope 的 `turnId`，再解包 `item` 对象取得原生条目 ID、正文和命令字段。列表页码、轮次序号和详情导航不变，历史查询不会获取会话写权限或改变桌面任务归属。共享后端需要支持 `thread/turns/list` 和 `thread/items/list`；接口不支持或分页失败会显示历史查询失败，保留连接并允许重试，不回退可能超出传输上限的全量请求。私有 stdio 保留既有历史读取方式。

## 显式配置

| 使用方 | 配置入口 | 示例 |
| --- | --- | --- |
| Remote JSON | `wrapper.codexAppServerURL` | `ws://127.0.0.1:17550` |
| Remote 运行环境 | `CODEX_REMOTE_CODEX_APP_SERVER_URL` | `ws://127.0.0.1:17550` |
| 桌面 App 启动环境 | `CODEX_APP_SERVER_WS_URL` | `ws://127.0.0.1:17550` |

Remote 环境变量显式覆盖 JSON；显式空值关闭共享接入，并且不写回配置文件。JSON 字段必须合并进现有配置，不能用下面的片段覆盖整个凭据文件：

```json
{
  "wrapper": {
    "codexAppServerURL": "ws://127.0.0.1:17550"
  }
}
```

Remote 只接受 `ws://127.0.0.1:PORT` 或 `ws://[::1]:PORT`，端口必须为 1–65535 的显式整数。可省略或使用根路径 `/`；不接受 hostname、远程 IP、userinfo、query、fragment、其他路径或其他协议。连接直接到 loopback，不读取系统代理。错误配置或连接失败直接报错，不回退启动私有 CLI。

桌面 App 已经运行的私有 stdio 后端无法热转换成共享后端。需要结束已有 App 实例，再从设置了 `CODEX_APP_SERVER_WS_URL` 的启动环境重新打开 App。仅给另一个终端设置变量、打开已有窗口或把变量设置在 Remote 服务里，都不能改变已运行 App 的连接。

## 后端生命周期与配置所有权

共享后端由外部宿主管理启动、关闭、CLI 版本、账号和 `CODEX_HOME`；Remote 只拥有自己的 WebSocket 连接。停止或重启 Remote 工作进程只关闭对应连接，不杀共享 app-server，也不替它下载或切换 CLI。

共享模式只能使用 native 配置，继承后端实际账号、provider 和 endpoint。Remote 的配置探测直接连接所选共享端点，只执行 `initialize`、`initialized`、`config/read`；不启动另一个 CLI，不创建线程，也不请求模型。原始配置观察结果不能序列化；公开连接证据对包含凭据或敏感参数的 endpoint 使用 opaque 标识。

共享 native 使用独立的 `codex-shared-native-v1` 连接合同，不声称已经完成私有 CLI 的 `codex-profile-runtime-v1` 能力验证。profile revision、连接 generation 和 context preference 引用仍须精确匹配。线程级 context 策略沿用既有 RPC 表达；不会生成 child 环境变量、CLI override 或托管启动文件。API/OAuth profile 以及需要改变 child 启动配置的选项在共享模式下拒绝，不能由某个客户端悄悄替换公共后端凭据。

## Windows 启动示例

下面只展示显式启动方式，不修改已安装的服务、生产 JSON、注册表环境或自动启动配置。示例 CLI 路径来自本次本机安装位置；版本目录变化后需替换为本机实际存在的完整路径。后端与两个客户端必须使用相同用户的数据目录。要隔离测试，可在启动前显式设置测试用 `CODEX_HOME`。

在一个终端启动后端，保持终端运行：

```powershell
$codexPath = Join-Path $env:LOCALAPPDATA 'OpenAI\Codex\bin\2e5e00daee91c61d\codex.exe'
if (-not (Test-Path -LiteralPath $codexPath)) {
    throw '请把 codexPath 改为本机实际存在的 Codex CLI 路径'
}
& $codexPath app-server --listen ws://127.0.0.1:17550
```

在另一个终端，为将要启动的 Remote 进程设置地址。后台服务不会继承这个终端的新变量；测试时应使用另一个配置、状态目录和未被生产服务占用的端口：

```powershell
$env:CODEX_REMOTE_CODEX_APP_SERVER_URL = 'ws://127.0.0.1:17550'
# 在此终端用隔离的配置启动测试版 Remote daemon。
# codex-remote daemon -config C:\path\to\isolated-config.json
```

完全退出已有桌面 App 后，从同一个设置了共享地址的终端启动新实例。当前 Windows Store 包名称为 `OpenAI.Codex`，启动程序名称仍为 `ChatGPT.exe`：

```powershell
$appPackage = Get-AppxPackage -Name OpenAI.Codex |
    Sort-Object Version -Descending | Select-Object -First 1
if (-not $appPackage) { throw '未找到 OpenAI.Codex 桌面安装包' }
$appPath = Join-Path $appPackage.InstallLocation 'app\ChatGPT.exe'
if (-not (Test-Path -LiteralPath $appPath)) { throw '未找到桌面启动程序' }
$env:CODEX_APP_SERVER_WS_URL = 'ws://127.0.0.1:17550'
Start-Process -FilePath $appPath
```

这里的前台 app-server 在终端关闭后可能退出。长期运行需由宿主配置独立进程管理和启动顺序；这不属于 Remote 安装共享接入时自动完成的操作。升级桌面 App 或 CLI 后需核对实际运行的后端版本与 App 协议兼容性，不能认为安装新 App 就自动更新已经运行的共享后端。

## 会话归属、交替使用与断线

共享后端使两个程序连接同一个线程，但不允许同一线程同时运行两个 turn。一端的 turn 结束后，另一端才能在同一线程继续；不同线程的并发仍受后端和 Remote 的并发策略限制。请求 ID、响应中的 thread/turn ID 和当前连接归属用于区分本客户端操作与外部客户端广播，不通过时间顺序猜测。

外部客户端的活动与请求不能冒充本 Remote 请求，也不能把桌面需要处理的工具或审批回调抢到飞书。归属无法在规定时间内确定时关闭当前连接并报告问题，不能借合成完成事件继续派发。

WebSocket 断开不等于后端 turn 已经结束。Remote 明确报告共享连接断开以及后端任务状态未知，不伪造成功、不杀后端、不把旧请求自动重放到另一条连接。恢复后应先核对线程实际状态，再决定继续、重新发送或取消，避免重复工具操作。

## 本机验证证据与验收边界

当前定向测试已经覆盖配置加载/保存和凭据保留、环境覆盖不持久化、非法 URL 拒绝、共享配置只读探测、取消/超时关闭连接、帧上限，以及 native projection 与私有 capability 合同的区分。fake WebSocket 测试验证了探测结束后外部后端仍可接受新连接。

2026-10-12，本机隔离集成使用官方 `codex-cli 0.162.0-alpha.17.2`、独立 `CODEX_HOME`、真实 Windows Desktop App 和当前分支编译的三个 Remote wrapper，通过真实 app-server WebSocket 完成验证：

- 真实 Desktop App 发送 `Codex Desktop` initialize 并读取 account/config/project；两个 Remote wrapper 对不同 thread 不重复投影。
- Remote 与另一个客户端在同一 thread 交替运行，own turn 归属 `remote_surface`，外部 turn 归属 `local_ui`。进入已有会话的 startup resume 仅携带精确 threadId。
- 官方后端产生的数值 ID native 命令审批只进入发起 Remote 一次；另一个客户端的审批及 resolved 通知不会生成飞书可回答请求。审批全部 decline，未执行模型请求的命令。
- Remote 连接重启只重新初始化并恢复 thread；强制断线不产生伪 `TurnCompleted`，外部后端上的任务继续完成，Desktop 和另一 Remote 连接保持可用。关闭 Remote 不终止后端。

最终证据保存为本机测试目录的 `result.json` 与 `protocol-frames.jsonl`；全部断言通过，12 次本机模拟 Responses 请求，0 次外部模型请求。实际 App 握手来自桌面应用，对话输入来自隔离协议控制器；尚未验证 Desktop UI 手工输入、手机飞书真人操作或真实账号的模型网络。本次没有修改生产 Remote 服务、主 App 配置或已有会话。

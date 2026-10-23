# 共享 Codex 后端版本维护

> Type: `implemented`
> Updated: `2026-10-23`
> Summary: 增加共享宿主立即升级的影响预览、单次确认与中断恢复边界，保留普通升级空闲检查。

## 使用入口

在飞书发送 `/upgrade`，点击“Codex 升级”，再点击“检查更新”。也可直接发送 `/upgrade codex`。
当前版本来自实际登记宿主；只有确认后才下载、校验并尝试切换。普通“确认升级”和回退仍要求空闲，不暂停或清除 Goal。
最新版本查询使用官方 npm 稳定版 metadata 和 Remote 保存的模型代理环境。飞书自身可保持直连；私有 CLI 的固定版本及 alpha channel 不参与共享后端检查。网络失败如实展示，不用缓存版本冒充本次检查成功。

未登记宿主时入口仍可见，显示登记缺失的原因；不会升级另一份本地 CLI。共享来源切换与自动更新继续禁止。

### 立即升级

需要先更新共享宿主源码，并在该宿主的私有 JSON 配置中明确设置 `"allowImmediateUpgrade": true`，于全部客户端空闲时安全重启宿主。默认关闭；只更新 Remote 而未更新或启用宿主不会出现此按钮，旧宿主不会被当作支持强制中断。

检查发现新版本后，支持该能力的共享来源卡片同时提供“立即升级”；不要求普通升级的空闲条件。点击只读取影响范围，不下载或中断任务。确认页显示此宿主所有 App/飞书会话的活动任务、活动 Goal、待回执与结果未知请求计数，以及有限的会话名称。范围覆盖其它窗口与话题，不限于发起聊天。

只有发起者点击“确认中断并升级”才执行。取消不停止任务。每次预览和取消都轮换同卡 flow identity，旧按钮不能确认新计划；保持原 `upgrade_owner_flow`、daemon lifecycle、owner、surface 与 TTL 校验。预览 token 由宿主私有内存保存，2 分钟有效、单次消费，绑定当前版本、目标和 native generation；重启、目标变化或重复提交均要求重新预览。

执行顺序是：完整验证旧包和候选包 → 冻结 gateway → 重新核对已确认影响 → 暂停并确认所有活动 Goal → 中断已确认 turn → 停止自有 native 进程树 → 确认退出后清理该代请求跟踪 → 启动并验证目标完整运行环境。发现新增任务、改变的 Goal 或新非只读请求则拒绝，要求重新预览；不向未确认 turn 发送 interrupt。原生协议不提供跨 RPC 原子快照，不能把多次检查表述为原子事务。

已执行的外部操作无法撤销。已经中断的任务不会自动继续，Goal 不会自动恢复，结果未知的请求不会重发；即使候选启动失败并恢复旧包，这些规则仍适用。未发送的队列保留，在原绑定、连接合同和 loaded 状态恢复验证完成后按原队列规则派发；独立维护屏障跨断线/重连保留，不因 surface 执行 gate 重置而提前发送。恢复失败保持屏障与人工恢复提示。共享维护不修改用户登录、项目、记录或定时任务。

## 宿主安装边界

- 源码位于 `scripts/shared-backend/`，依赖固定在 `requirements.txt`；生产模块嵌入 Remote 二进制，通过 `codex-runtime host-export --directory <私有目录>` 导出完整的内容哈希包。导出不改选择文件、服务或用户配置；已有包损坏或夹带文件时明确失败，不原地修复。
- Python 3.10 或更新版本。使用独立虚拟环境，不修改系统 Python 包。
- 现有 Python/其它宿主不会被热接管。部署前须确认全部客户端无运行任务，保留原服务/config/CLI，并停止原宿主；配置新的宿主服务后验证其公共端点与飞书重连。
- 所有 Remote 和 App 客户端只能连接公共 `endpoint`。原生内部端点只供宿主使用；绕过公共 gateway 的连接不受准入控制，不属于受支持的部署。
- 所有端点为显式端口的 literal loopback。远程主机、DNS 名称、URL 凭据和跳转不支持维护。
- `stateDirectory` 必须与目标 Remote 的 StateDir 相同。登记文件只含本机控制凭据，不包含 OpenAI 登录信息；Unix 为 0600，Windows 限当前用户、SYSTEM、管理员 ACL。
- 无 App 的主机继承服务配置的认证与代理环境。需要桌面工具时显式配置 `appEnvironment`，由仓库内实现发现当前 App、Node 和资源路径；不再依赖仓库外的 App 启动适配器。升级不更改用户 config/auth。

可信 owner-only JSON 配置示例（路径均须改成该主机的绝对路径）：

```json
{
  "directory": "/opt/codex-owner",
  "stateDirectory": "/var/lib/codex-remote-state",
  "endpoint": "ws://127.0.0.1:17550",
  "nativeEndpoint": "ws://127.0.0.1:17551",
  "controlURL": "http://127.0.0.1:17552",
  "initialBinary": "/opt/codex-owner/runtime/codex",
  "remoteBinary": "/opt/codex-remote/bin/codex-remote",
  "launchArgs": []
}
```

首次由目标 Remote 执行 `codex-runtime host-export --directory <source-packages绝对路径>`，保存返回的 `bootstrap` 路径作为固定服务入口：`python <bootstrap> --config <私有配置绝对路径> --source-root <source-packages绝对路径>`。每次启动，固定入口从配置的当前 `remoteBinary` 重新导出、校验并在同一 Python 进程加载宿主；Remote 升级后的下一次安全宿主重启会加载新代码，支持新包增加模块，不在执行中热替换。固定入口文件必须保留。直接运行 `host.py` 只适合开发，不会自动跟随安装的 Remote。

Windows 使用 pythonw 或隐藏后台服务；Linux 由同一用户 systemd 管理，支持 `Type=notify`，仅在 native 健康且维护登记完成后报告 ready；Remote 可用 Requires/After 排在宿主就绪之后。不得用全局杀进程命令代替已核对的宿主切换。

### App 环境配置

Windows 在同一配置中加入：

```json
"appEnvironment": {
  "mode": "windows",
  "sandboxPackageFamily": "OpenAI.Codex_2p2nqsd0c76g0",
  "appUserData": "D:\\CodexProfile",
  "required": true,
  "requiredServers": ["codex_app"],
  "pollInterval": 15,
  "discoveryTimeout": 20
}
```

请填写实际 package family 和用户数据目录。也可显式指定 `appExe`。Windows 按当前包和当前进程发现；Linux 使用 `mode: linux`、`appExe` 和 App 环境采集文件 `runtimeFile`，要求文件属于当前用户、权限 0600、采集时间不早于进程启动且 PID/可执行文件仍匹配。Linux 捕获文件的生成仍由部署侧负责，单凭进程启动环境不足以取得 App 后加的工具凭据。

每次启动/升级都重新组合 `CODEX_CLI_PATH`、`PATH`、Node 和资源环境，清除旧 CLI/资源路径以及共享连接自指配置。Node 可以位于当前 App 声明的用户运行时缓存，不要求固定在资源目录。不会回退使用旧 Node。App 模式默认 `required: true`；当前 App/Node 不可用时拒绝新维护，不停止既有进程。显式 `required: false` 允许新启动暂不接入 App 工具。

监测默认每 15 秒重试；发现 App 重启或路径变化后，先核对 Remote 队列、未确认请求、原生任务和 active Goal，空闲才冻结准入并刷新。附着但空闲的客户端不会永久阻止刷新。启动必须通过原生握手和指定 MCP 工具的只读健康检查；失败恢复旧环境，双重失败关闭准入。App 退出、发现超时、暂时繁忙或发布状态失败都不会永久终止监测。只持久化安全状态与完整运行包索引，App 凭据仅留内存。需要非默认 Remote 管理端口时设置 `remoteStatusURL`，仅接受无跳转的 literal loopback HTTP `/v1/status`。

### 完整运行包索引

`selection.json` 是共享宿主实际选择，`managed.json` 仅是准备器状态，两者不需要伪造为同一 active。选择和 previous 都保留 `schemaVersion/bundleRoot/layout/components`；Linux 校验 CLI、code-mode-host、bwrap，Windows 校验 CLI、code-mode-host、command-runner 和 sandbox-setup。同一个受支持 npm vendor 或 flat 布局内的每个组件都必须存在、可执行且 SHA 匹配，不能用另一版辅助程序拼包。

`codex-runtime bundle --binary <native> --version <版本>` 生成完整证据，`verify-bundle --manifest <私有JSON>` 复核。旧单文件记录仅在主程序原 SHA、完整组件和版本全部验证后迁移。启动、升级前旧版检查、候选启动和回退均用完整校验；缺失/篡改助手必须在停止健康旧进程前被发现。选择只在新进程健康后持久化，写盘失败同样恢复旧版；不会只切主程序或把下载完成当作升级成功。

## 事务与恢复

1. 用户确认时重查 owner、版本、Remote 队列和任务；旧卡/过期 target 不执行。
2. 宿主串行执行，调用 Remote 的 `codex-runtime prepare` 下载官方目标并做能力校验；不激活 Remote 的私有 runtime。
3. 准备完成后冻结公共 gateway，新方法请求返回明确维护错误。普通升级在已接纳请求未回执、native 存在 active thread 或仍有 active Goal 时拒绝，原进程不停止；不自动暂停 Goal。断线前未确认的非只读请求要求先人工核对工作状态，再于空闲时重启宿主，不承诺等待即可自动恢复。显式立即升级遵循上节确认范围与中断步骤，不沿用普通空闲准入。
4. 空闲确认后仅停止自有 backend 子进程；新 CLI 校验完整组件/版本、启动并完成原生握手及配置的 App 工具健康检查，再持久化选择与上一版记录。内部端口可变化，避开 Windows 刚关闭端口不可立即重用的问题。
5. 失败使用单独的恢复窗口启动已校验旧版。恢复成功才开放准入；双重失败保持关闭，不重复启动用户任务。
6. 宿主状态包含本进程生命周期内的 native `generation`（启动尝试递增，旧宿主没有该字段时须先升级宿主）。Remote 保存原 surface、thread、workspace、连接 contract 和 wrapper ID。宿主代次变化后等待既有 headless 恢复管线创建新 wrapper、恢复原绑定并加载原 thread，不向已退出的旧 ID 发送 `child.restart`，也不以旧 ID 的短暂在线快照当恢复完成。准备失败且代次未变则保留原连接；空闲池由原预热管线补齐。仅在绑定恢复后释放事务与缓存。未知结果保留 gate，卡片及输入提示人工修复宿主、重启 Remote；不清队列，不伪装任务完成。

恢复检查遵循真实路由过渡：断线先清空选择与 claims，再进入 PendingHeadless、replacement hello，最后装回原选择。空选择只等待，不能误报改选，也不能当作显式 detach 而提前完成；原 surface 消失或明确恢复到不同 thread/工作区/contract 时失败关闭。失败卡片在 gate 保留时只给人工恢复指引，不再同时提示重复检查，错误详情不拼接 `<nil>`。

维护宿主尚未开放准入时，新 wrapper 的 initialize 可能遇到 WebSocket 1013，或握手阶段的 HTTP 503。wrapper 在原有 bootstrap 总期限内退避重连，不立即落入一次 headless 启动失败加恢复退避的长路径；initialize 成功后才发出一次原 thread 的 resume。订阅请求已发送后回执丢失、非维护断线和协议错误不自动重试，避免重复订阅或重放任务。该修复不增加维护恢复窗口，也不绕过原绑定、连接合同和 loaded 校验。

进程在持久化前异常退出时仍以最后完整选择文件为准。正在执行的旧进程不会被其它宿主自动收养；服务启动遇到占用端口必须失败。登记凭据每次宿主启动刷新，已加载旧凭据的维护请求失败后需重新检查。

## 验证范围

- 2026-10-23 立即升级：Windows Python discover 93 项通过、跳过 6 项；NanoPi Linux 原回归 92 项通过、跳过 3 项（平台/显式原生测试），另运行新增原生立即升级测试。隔离 CODEX_HOME 与本地 Responses fixture，无用户认证和真实模型请求：运行中的 turn/Goal 阻止普通升级，预览不中断，明确确认后完整三组件 `0.164.0 → 0.163.0`，Goal 保持 paused，本地模型调用始终一次。真实 POSIX 子进程忽略 SIGTERM 后仍能随进程组清理。测试结束已清理临时目录与进程，没有修改生产版本或服务。
- 本地覆盖登记权限、认证、endpoint 绑定、拒绝重定向和代理、菜单入口、真实宿主版本、较新 alpha 不被旧 stable 覆盖、缓存保留。
- Python 覆盖 active native 工作拒绝、并发拒绝、stale target、准备失败、启动失败恢复、双重失败关闭准入、gateway 上竞态 App turn 被拒绝。
- 隔离原生 CLI 测试只使用新的 CODEX_HOME、只读协议和故障候选，不向模型发送 prompt，不接入生产共享后端。需显式设置 `SHARED_CODEX_TEST_BINARY`、支持 bundle/export 的 `SHARED_CODEX_TEST_REMOTE` 和新的 `SHARED_CODEX_TEST_DIRECTORY`；可加 `SHARED_CODEX_TEST_OTHER_BINARY` 验证第二套官方包，保留诊断证据目录。
- 2026-10-16 NanoPi `root@100.75.254.29`（ARM64）：12 个 Python 宿主测试、3 个 ARM64 Go 维护客户端测试、隔离真实 CLI 故障恢复通过。独立 CODEX_HOME 下经实际 HTTP 维护接口完成官方 `0.162.1 → 0.163.0 → 0.162.1`，无模型 prompt 或硬件操作。
- 同日已在空闲时部署维护宿主与 Remote 测试版，Linux notify-ready、升级事务 committed、飞书 gateway connected、原有 1 个定时任务保留均已核验。此项为 NanoPi 当时的验收记录。用户确认飞书入口正确显示“共享后端宿主管理的 Codex CLI”和当前版本 `0.162.1`；首次 npm 查询 EOF 后补充共享检查继承模型代理环境的回归修复。
- 代理继承修复后，用户在 NanoPi 飞书卡片点击“确认升级”，实际宿主完成 `0.162.1 → 0.163.0`，维护状态 `running/busy=false`，选择文件持久化 `previous=0.162.1`；原绑定和队列保留。随后用户报告普通输入仍提示“正在升级”：新 wrapper 已上线，但旧实现向已退出 ID 发送 child.restart 并遗留恢复 gate。此前版本与客户端在线检查不足以证明完整事务结束。
- 修复回归覆盖新 wrapper 替换后清除事务/暂停、旧快照拒绝、未加载原 thread 不放行、不同工作区拒绝、准备失败保留原客户端、旧版本故障回退也等待新代次、超时保留 gate 及准确恢复提示。隔离真实 native 测试断言恢复后的运行代次增加。正式 NanoPi 上不为测试而回退用户已升级的版本。
- NanoPi 已部署 `v1.4.0.7-shared-upgrade-test.20261016.3`：部署前独立核对 native 任务与 Goal 空闲，升级宿主运行代次协议后保留 CLI `0.163.0`、上一版 `0.162.1`、原绑定及 1 个定时任务；Remote 自升级事务 committed、gateway connected。新回归从真实升级入口捕获绑定，模拟 wrapper ID 替换后确认下一条普通输入不再命中升级拦截。飞书端恢复反馈另行确认，不将服务重启本身视作后续 CLI 升级完整验收。
- 网络出口稳定性与模型执行能力不由上述维护测试证明。Windows 全量回归存在可在原 HEAD 重现的 SQLite 文件锁/路径等既有失败；本功能定向测试通过，不宣称全套测试通过。上述隔离实机维护测试不代表 Windows 或 Linux 服务器端到端升级验收。
- 后续用户在 NanoPi 点到回退上一版，实际完成 `0.163.0 → 0.162.1`（generation 2）且原会话随后恢复；旧检查把路由临时空选择误判成绑定变化而保留 gate。本轮用真实 `ApplyInstanceDisconnected` 产生清空中间态，覆盖断线间隙、PendingHeadless、replacement hello、原选择恢复以及下一条输入不被升级拦截；不通过再次回退或升级用户的实际 CLI 来掩盖该问题。
- 同日 Linux 服务器已登记维护宿主，用户实际升级至 `0.163.0`，但初始化遭维护准入拒绝后进入 headless 启动超时与退避，导致原会话恢复晚于维护观察窗口。空闲时仅重启 Remote 恢复 gate，保留宿主和原生进程、版本、配置、绑定及定时任务；用户确认历史正常、当前版本为 `0.163.0`。修复回归覆盖读取初始化前即关闭的 1013、初始化途中 1013、握手 503、总期限取消，以及订阅已发送后和非维护错误不重试；不为验证而再次切换生产 CLI。
- 完整运行包修复在 NanoPi ARM64 上通过组件缺失/篡改/权限/符号链接、不可变源码包导出、68 项 Python 测试（Windows 专项及未开启的原生测试跳过）。额外显式运行的两项真实原生测试通过：独立 CODEX_HOME 下经实际 HTTP 控制接口反复 `0.162.1 → 0.163.0 → 0.162.1` 两轮、启动失败恢复、损坏候选不停止旧进程、选择文件读回，以及固定 bootstrap 两次启动和导出包复用。没有向模型发送 prompt，也没有访问设备或改动生产 CLI 版本。
- Windows 只读发现核对了当前 MSIX 包、默认 profile、Node/资源和所选 CLI；测试覆盖 family-only、默认空 profile、多实例歧义拒绝、发现超时单探针重用、活动工作阻止刷新、刷新失败恢复、监测错误后重试、选择持久化失败与敏感信息不落盘。wrapper、共享维护客户端、managed/runtime CLI、源码导出包与 daemon 共享维护定向测试通过；Windows daemon 全量测试仍有 SQLite 文件锁与路由超时失败，不将其计入本次全绿结果。

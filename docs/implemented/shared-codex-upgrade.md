# 共享 Codex 后端版本维护

> Type: `implemented`
> Updated: `2026-10-16`
> Summary: 修复维护期间启动连接被明确拒绝后进入长时间退避的问题；只重试初始化，不重放订阅或用户任务。

## 使用入口

在飞书发送 `/upgrade`，点击“Codex 升级”，再点击“检查更新”。也可直接发送 `/upgrade codex`。
当前版本来自实际登记宿主；只有确认后才下载、校验并尝试切换。正在执行任务时不强制升级，不暂停或清除 Goal。
最新版本查询使用官方 npm 稳定版 metadata 和 Remote 保存的模型代理环境。飞书自身可保持直连；私有 CLI 的固定版本及 alpha channel 不参与共享后端检查。网络失败如实展示，不用缓存版本冒充本次检查成功。

未登记宿主时入口仍可见，显示登记缺失的原因；不会升级另一份本地 CLI。共享来源切换与自动更新继续禁止。

## 宿主安装边界

- 源码位于 `scripts/shared-backend/host.py`、`owner.py`，依赖固定在 `requirements.txt`。
- Python 3.10 或更新版本。使用独立虚拟环境，不修改系统 Python 包。
- 现有 Python/其它宿主不会被热接管。部署前须确认全部客户端无运行任务，保留原服务/config/CLI，并停止原宿主；配置新的宿主服务后验证其公共端点与飞书重连。
- 所有 Remote 和 App 客户端只能连接公共 `endpoint`。原生内部端点只供宿主使用；绕过公共 gateway 的连接不受准入控制，不属于受支持的部署。
- 所有端点为显式端口的 literal loopback。远程主机、DNS 名称、URL 凭据和跳转不支持维护。
- `stateDirectory` 必须与目标 Remote 的 StateDir 相同。登记文件只含本机控制凭据，不包含 OpenAI 登录信息；Unix 为 0600，Windows 限当前用户、SYSTEM、管理员 ACL。
- 认证、代理、Node 和桌面工具环境由宿主服务显式继承。升级不更改用户 config/auth，不替代 App 工具环境的独立维护。

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

运行虚拟环境 Python 的 `host.py --config <配置绝对路径>`。Windows 使用 pythonw 或隐藏后台服务；Linux 由同一用户 systemd 管理，支持 `Type=notify`，仅在 native 健康且维护登记完成后报告 ready；Remote 可用 Requires/After 排在宿主就绪之后。不得用全局杀进程命令代替已核对的宿主切换。

## 事务与恢复

1. 用户确认时重查 owner、版本、Remote 队列和任务；旧卡/过期 target 不执行。
2. 宿主串行执行，调用 Remote 的 `codex-runtime prepare` 下载官方目标并做能力校验；不激活 Remote 的私有 runtime。
3. 准备完成后冻结公共 gateway，新方法请求返回明确维护错误，已接纳请求未回执、native 存在 active thread 或仍有 active Goal 则拒绝本轮切换，原进程不停止；不自动暂停 Goal。断线前未确认的非只读请求要求先人工核对工作状态，再于空闲时重启宿主，不承诺等待即可自动恢复。
4. 空闲确认后仅停止自有 backend 子进程；新 CLI 校验版本/digest、启动并完成原生握手，再持久化选择与上一版记录。内部端口可变化，避开 Windows 刚关闭端口不可立即重用的问题。
5. 失败使用单独的恢复窗口启动已校验旧版。恢复成功才开放准入；双重失败保持关闭，不重复启动用户任务。
6. 宿主状态包含本进程生命周期内的 native `generation`（启动尝试递增，旧宿主没有该字段时须先升级宿主）。Remote 保存原 surface、thread、workspace、连接 contract 和 wrapper ID。宿主代次变化后等待既有 headless 恢复管线创建新 wrapper、恢复原绑定并加载原 thread，不向已退出的旧 ID 发送 `child.restart`，也不以旧 ID 的短暂在线快照当恢复完成。准备失败且代次未变则保留原连接；空闲池由原预热管线补齐。仅在绑定恢复后释放事务与缓存。未知结果保留 gate，卡片及输入提示人工修复宿主、重启 Remote；不清队列，不伪装任务完成。

恢复检查遵循真实路由过渡：断线先清空选择与 claims，再进入 PendingHeadless、replacement hello，最后装回原选择。空选择只等待，不能误报改选，也不能当作显式 detach 而提前完成；原 surface 消失或明确恢复到不同 thread/工作区/contract 时失败关闭。失败卡片在 gate 保留时只给人工恢复指引，不再同时提示重复检查，错误详情不拼接 `<nil>`。

维护宿主尚未开放准入时，新 wrapper 的 initialize 可能遇到 WebSocket 1013，或握手阶段的 HTTP 503。wrapper 在原有 bootstrap 总期限内退避重连，不立即落入一次 headless 启动失败加恢复退避的长路径；initialize 成功后才发出一次原 thread 的 resume。订阅请求已发送后回执丢失、非维护断线和协议错误不自动重试，避免重复订阅或重放任务。该修复不增加维护恢复窗口，也不绕过原绑定、连接合同和 loaded 校验。

进程在持久化前异常退出时仍以最后完整选择文件为准。正在执行的旧进程不会被其它宿主自动收养；服务启动遇到占用端口必须失败。登记凭据每次宿主启动刷新，已加载旧凭据的维护请求失败后需重新检查。

## 验证范围

- 本地覆盖登记权限、认证、endpoint 绑定、拒绝重定向和代理、菜单入口、真实宿主版本、较新 alpha 不被旧 stable 覆盖、缓存保留。
- Python 覆盖 active native 工作拒绝、并发拒绝、stale target、准备失败、启动失败恢复、双重失败关闭准入、gateway 上竞态 App turn 被拒绝。
- 隔离原生 CLI 测试只使用新的 CODEX_HOME、只读协议和故障候选，不向模型发送 prompt，不接入生产共享后端。需显式设置 `SHARED_CODEX_TEST_BINARY` 和新的 `SHARED_CODEX_TEST_DIRECTORY`，保留诊断证据目录。
- 2026-10-16 NanoPi `root@100.75.254.29`（ARM64）：12 个 Python 宿主测试、3 个 ARM64 Go 维护客户端测试、隔离真实 CLI 故障恢复通过。独立 CODEX_HOME 下经实际 HTTP 维护接口完成官方 `0.162.1 → 0.163.0 → 0.162.1`，无模型 prompt 或硬件操作。
- 同日已在空闲时部署维护宿主与 Remote 测试版，Linux notify-ready、升级事务 committed、飞书 gateway connected、原有 1 个定时任务保留均已核验。此项为 NanoPi 当时的验收记录。用户确认飞书入口正确显示“共享后端宿主管理的 Codex CLI”和当前版本 `0.162.1`；首次 npm 查询 EOF 后补充共享检查继承模型代理环境的回归修复。
- 代理继承修复后，用户在 NanoPi 飞书卡片点击“确认升级”，实际宿主完成 `0.162.1 → 0.163.0`，维护状态 `running/busy=false`，选择文件持久化 `previous=0.162.1`；原绑定和队列保留。随后用户报告普通输入仍提示“正在升级”：新 wrapper 已上线，但旧实现向已退出 ID 发送 child.restart 并遗留恢复 gate。此前版本与客户端在线检查不足以证明完整事务结束。
- 修复回归覆盖新 wrapper 替换后清除事务/暂停、旧快照拒绝、未加载原 thread 不放行、不同工作区拒绝、准备失败保留原客户端、旧版本故障回退也等待新代次、超时保留 gate 及准确恢复提示。隔离真实 native 测试断言恢复后的运行代次增加。正式 NanoPi 上不为测试而回退用户已升级的版本。
- NanoPi 已部署 `v1.4.0.7-shared-upgrade-test.20261016.3`：部署前独立核对 native 任务与 Goal 空闲，升级宿主运行代次协议后保留 CLI `0.163.0`、上一版 `0.162.1`、原绑定及 1 个定时任务；Remote 自升级事务 committed、gateway connected。新回归从真实升级入口捕获绑定，模拟 wrapper ID 替换后确认下一条普通输入不再命中升级拦截。飞书端恢复反馈另行确认，不将服务重启本身视作后续 CLI 升级完整验收。
- 网络出口稳定性与模型执行能力不由上述维护测试证明。Windows 全量回归存在可在原 HEAD 重现的 SQLite 文件锁/路径等既有失败；本功能定向测试通过，不宣称全套测试通过。上述隔离实机维护测试不代表 Windows 或 Linux 服务器端到端升级验收。
- 后续用户在 NanoPi 点到回退上一版，实际完成 `0.163.0 → 0.162.1`（generation 2）且原会话随后恢复；旧检查把路由临时空选择误判成绑定变化而保留 gate。本轮用真实 `ApplyInstanceDisconnected` 产生清空中间态，覆盖断线间隙、PendingHeadless、replacement hello、原选择恢复以及下一条输入不被升级拦截；不通过再次回退或升级用户的实际 CLI 来掩盖该问题。
- 同日 Linux 服务器已登记维护宿主，用户实际升级至 `0.163.0`，但初始化遭维护准入拒绝后进入 headless 启动超时与退避，导致原会话恢复晚于维护观察窗口。空闲时仅重启 Remote 恢复 gate，保留宿主和原生进程、版本、配置、绑定及定时任务；用户确认历史正常、当前版本为 `0.163.0`。修复回归覆盖读取初始化前即关闭的 1013、初始化途中 1013、握手 503、总期限取消，以及订阅已发送后和非维护错误不重试；不为验证而再次切换生产 CLI。

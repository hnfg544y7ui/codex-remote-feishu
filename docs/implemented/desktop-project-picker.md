# 桌面项目分组选择卡

> Type: `implemented`
> Updated: `2026-10-11`
> Summary: 项目分组已部署 NanoPi 和 Windows，回归通过且用户确认 Windows 新卡片正常。

## 当前事实和范围

本说明记录实现和部署验收；交互与路由规范以 [remote-surface-state-machine.md](../general/remote-surface-state-machine.md) 和 [feishu-card-ui-state-machine.md](../general/feishu-card-ui-state-machine.md) 为准。

Codex headless 的 `/workspace list`、`/list`、`/use`、`/useall` 切换卡按本主机桌面项目显示中文项目名，再选择会话。一个项目下多个目录合并显示，没有项目的会话归入“未归属项目”。Claude、OpenCode、VS Code 模式和锁定目录选择流程保持原合同。

项目分组读取本机数据，不跨主机合并目录，不把 ChatGPT 云项目伪造为本地执行项目。纯 CLI 主机没有桌面项目数据时，其会话显示在“未归属项目”；要显示命名项目，需本主机已有桌面项目元数据。

同一路径被多个项目共用时，明确归属的已有会话仍显示在各自项目；新会话仅采用能唯一按目录推断到所选项目的根目录，否则不提供新建选项，避免创建后归到另一项目。

## 数据和自动同步

- 每轮只读 `state_5.sqlite` 的 `projects`、`project_roots` 和非空 `threads.project_id`。
- 同目录 `.codex-global-state.json` 补充明确 local assignment、projectless IDs 和 legacy/native ID 映射。数据库项目和根目录优先，缺表/字段才使用旧格式。
- 明确归属优先；明确未归属、cloud assignment、已删除或无法安全解析的 assignment 禁止按目录猜测。只有未知归属才按最长项目根目录边界推断。
- 保留数据库项目顺序和多根路径。每次打开、选择、分页和确认都重新读取，项目改名、移动会话和删除根目录自动生效；无后台轮询、无跨请求缓存、无用户状态写入。

## 卡片和执行边界

项目下拉复用现有 workspace select lane，显示值为 `desktop-project:ID`；项目标签和真实执行目录分别存储。callback kind、字段名、owner 和 daemon lifecycle 校验不变。

选择已有会话后，执行目录来自该会话原始归属；新会话使用项目第一条仍存在、未占用且能推断到该项目的根目录，并显示实际目录。Worktree 动作沿用既有实际目录子页。确认前重查 membership、目录和占用；变化则更新卡片要求重新确认，不能把旧项目选择转换成其他会话。

项目或会话列表翻页保持分组，未选择有效会话时确认按钮不可用；空项目保留切换项目、取消和从目录新建的出口。

## 当前验证和部署状态

已验证 catalog 新旧 schema、中文改名、SQL/legacy 优先级、显式未归属、失效/歧义映射、项目顺序和损坏数据；核心回归覆盖多目录合并、真实目录路由、归属变化、删除项目、占用变化和分页；飞书回归覆盖项目显示值、latest form value 与 lifecycle stamping。

最终 focused tests 在 codexstate、adapter/feishu、core/orchestrator、app/daemon 四个包通过。Linux ARM64 测试包构建成功，版本为 `v2.1.2-project-picker-test.20261011`。

Windows 完整网络测试此前受到 sandbox 限制；真实 catalog 的只读投影在后续 Windows 部署验证中已通过。获得部署所需的运行权限后，`scripts/check/pre-commit.sh` 已通过；保留仓库既有 25 项非阻断跨平台路径告警。

## NanoPi 部署验证

- 目标为 `root@100.75.254.29`，架构 aarch64；2026-10-11 通过原生 `local-upgrade` 部署，slot 为 `desktop-project-test-20261011`，事务为 committed。
- 运行二进制版本 `v2.1.2-project-picker-test.20261011`，SHA256 为 `91f349c7fae9a65912cedbb8aff4a1715d2e7b35ddcd3bce2334b36b25463f17`。
- systemd 用户服务 active/running，enabled，root Linger=yes；飞书机器人“linux工作台”为 connected。升级前后 config 校验和一致，原二进制和配置保留在服务器 deploy-backups 的该测试目录。
- NanoPi 当前数据库包含 0 个桌面项目、1 个会话，且没有 global desktop state。因此预期显示“未归属项目”，其下包含新建选项和已有会话；不应伪造其他主机项目。
- 在 NanoPi 执行 ARM64 功能回归：多目录与显式未归属、真实目录路由、归属/占用/删除重查、分页、根目录变化及共享根目录全部通过。
- 只读真实数据库投影通过，生成 1 个项目选项、2 个会话选项。该诊断使用内存服务，不发消息、不修改在线 daemon 或用户会话；临时诊断源码已移除。
- NanoPi 已完成 ARM64 回归和只读真实 catalog 投影；真实项目名显示由拥有桌面项目数据的 Windows 用户测试验收。

功能分支 `feature/desktop-project-picker`，基底 `develop` 的 `5a1d70a`。

## Windows 部署验证

- 2026-10-11 本机 stable 通过原生 `local-upgrade` 部署到同名测试 slot，事务为 committed；运行版本 `v2.1.2-project-picker-test.20261011`。
- Windows amd64 二进制 SHA256 为 `bb53bf264778e07ecb1c6d1ae931132655e40617334bdd82263c267538814b1f`，安装后匹配构建产物。
- “windows工作台”和“王林的智能助手”均 connected。原配置校验和一致，后台计划任务启用且运行；原二进制、配置和任务定义保留在本机 deploy-backups 的本次测试目录。
- 本机真实数据库只读投影通过：读取 14 个桌面项目，生成 15 个项目选项（包含未归属项目）；默认未归属组生成 80 个会话选项，首次投影约 404ms。该数据不是在线飞书回调延迟，不能替代用户点击验收。
- 临时诊断使用内存服务，不发消息、不修改在线 daemon 或用户会话；临时诊断源码已移除。
- Windows 首轮用户测试发现 `/use` 入口遗漏，修正版验收记录见下节。

## 首轮用户测试修正

用户在本机菜单发送 `/use` 后仍看见目录卡。在线日志确认动作是 `surface.button.show_threads`；该入口的 picker source 为 use，项目分组却只允许 list。这是入口覆盖遗漏，首轮只测 list 的只读投影未覆盖真实 `/use` 入口。

修正为覆盖 list/use/useall，保留原 source 和锁定目录例外。新增从 `ApplySurfaceAction` 进入的 list/use/useall 回归，修复前 use/useall 均失败，修复后通过；core/orchestrator、app/daemon、adapter/feishu focused tests 全部通过。

Windows 和 NanoPi 已通过原生升级更新为 `v2.1.2-project-picker-test.20261011.2`，slot 为 `desktop-project-test-20261011-2`。Windows SHA256 为 `5c6c7f2eafe3b6435156b2a39f5ffd437563c69c0a7a1b61e7f710a26cfd867d`，Linux ARM64 为 `ec497cf977ca0f90be178d6967bc502910ba2130b04c030357c2631914b6d02c`。

2026-10-11 用户在本机重新发送 `/use` 后确认标题“切换项目与会话”和“项目”字段已正常显示，并回复“没有问题”。按此前约定进入功能分支提交阶段；本记录不把卡片显示确认扩大为所有会话和所有入口的人工测试。

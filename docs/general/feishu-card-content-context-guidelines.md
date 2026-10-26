# Feishu Card Content Context Guidelines

> Type: `general`
> Updated: `2026-10-26`
> Summary: 补充“需求描述 -> 分层落点 -> Markdown 处理”的决策规范，明确这类代码应把格式职责放在 adapter 最后一跳渲染。

## 1. 文档定位

这份文档是当前仓库实现 Feishu 卡片文本内容时必须遵守的研发基线。

它约束的不是某一张卡片的视觉样式，而是：

- 哪些文本可以进入 Feishu markdown 语境
- 哪些文本必须留在 `plain_text` 或结构化 section 里
- 结构、动态值和 Feishu-specific 渲染职责应该落在哪一层
- 新路径替换旧路径时，仓库应如何避免长期新旧并存

## 2. 适用范围

以下变更默认都应先对照本文件：

- `internal/adapter/feishu/**` 中的 card projector / renderer / helper
- `internal/core/control/**` 中新增或修改 Feishu card DTO、summary、status、section 字段
- `internal/core/orchestrator/**` 与 `internal/app/daemon/**` 中新增或修改会投影到 Feishu 卡片的 summary、status、message、notice、catalog 内容
- 任何讨论 Feishu 卡片文本 contract、markdown/plain_text 边界、render helper 职责的设计或文档

## 3. 当前默认语境

当前仓库里，至少应明确区分下面四种文本语境：

1. `plain_text`
   - 飞书明确不会按 markdown 解释的文本字段
2. adapter-owned markdown
   - 由 adapter 明确拥有结构和格式的 Feishu markdown 文本
3. Feishu-specific markdown tag
   - 如 `<text_tag>`、`<font>` 这类只应出现在 markdown-capable 字段里的平台标签
4. final reply markdown
   - final answer 专用的 markdown 归一化链路，是当前仓库的特例，不是通用 system-card contract

如果实现里无法明确说清楚当前字符串属于哪一种语境，就说明设计还没有收敛到可安全落地的边界。

## 4. 强制规则

### 4.1 动态值不上游预拼 raw markdown

只要文本里混入了外部或动态值，就不能在上游把它预拼成一整段 raw markdown 再往下游传。

典型动态值包括：

- 用户输入、quoted text、thread title、最近消息
- runtime/debug/error 文本
- repo URL、workspace path、file name、stderr/output tail
- 运行中状态、下一步提示、命令执行结果

这些内容默认应先保留结构，再由 adapter 决定最终落到：

- `plain_text`
- `FeishuCardTextSection`
- 或少量 adapter-owned markdown 片段

### 4.2 adapter 拥有最终 markdown/plain_text 切分

orchestrator / control / daemon 层负责表达业务语义与结构，不负责拼 Feishu markdown 结构。

默认顺序应是：

1. 上游传结构和动态值
2. adapter 决定哪些部分用 markdown，哪些部分用 `plain_text`
3. adapter 决定是否使用 `<text_tag>` / `<font>` 等 Feishu-specific 标签

不要把 Feishu-specific tag 反向扩散到 orchestrator / control。

### 4.3 `renderSystemInlineTags(...)` 不是通用 sanitizer

`renderSystemInlineTags(...)` 这类 helper 只能视为 adapter-owned markdown 的局部格式工具。

它们不能被当成：

- 任意动态文本进入 markdown 的安全兜底
- “先上游拼 markdown，再下游补 neutralize” 的通用修复策略
- 新 system-card text contract 的默认边界

### 4.4 新卡片文本默认优先结构化 carrier

新增 summary / status / notice / catalog / picker 文本时，默认优先使用：

- `FeishuCardTextSection`
- `SummarySections`
- 显式 section/label/value 字段
- 飞书组件原生 `plain_text` 字段

不要优先新增“再来一个 `string` markdown body”式 contract。

### 4.5 raw system markdown 只允许留给固定系统 copy

只有同时满足以下条件，才继续允许一条链路保留 raw markdown string：

- 文本结构完全由本地系统 copy 拥有
- 不混入 runtime/debug/path/repo/output/file name 等动态值
- markdown 只是展示格式，不承担“把结构和动态值重新揉在一起”的职责

不满足这些条件时，应改成 structured/plain_text 路径。

### 4.6 新路径替换旧路径时，不保留永久双路径

如果某条 legacy markdown contract 已经有了新的 structured/plain_text 替代路径，后续应尽快删除旧 contract，而不是长期保留：

- legacy flag
- legacy renderer branch
- “以防万一”的永久 fallback

当前仓库默认不接受“新旧两套方法长期并存”的收尾方式。

### 4.7 final reply markdown 是特例，不外溢

final reply 当前有专门的 markdown normalize 与 preview rewrite 链路。

Codex 的 `:codex-file-citation{path="..." purpose="output"}` 属于文件产物引用，不能把它当作普通正文中的裸路径局部替换。预览 adapter 必须先识别并完整消费该引用，再使用原有文件解析、作用域授权和发布链路生成以文件名为标签的普通链接；不能让 Markdown 链接残留在 `path` 属性内部。代码示例、未知用途或格式不完整的引用保留原文；发布失败时也保留完整引用并沿用现有错误提示。

这条能力只能服务 final answer / final card，不应直接复制成：

- 通用 notice contract
- 通用 status card contract
- command catalog / picker / request / selection 的默认做法

### 4.8 新边界必须带回归测试

只要一条链路的文本 contract 发生变化，至少应补一类测试：

- 动态文本已迁出 markdown 时：
  - 断言动态值不再出现在 markdown element / markdown body
- 明确保留 adapter-owned markdown 时：
  - 断言输入确实是系统拥有的固定 copy，或已走专用 normalize/helper

不要只测“看起来能显示”，要测“有没有重新把动态值塞回 markdown”。

## 5. 推荐实现顺序

以后新做 Feishu 卡片文本相关功能，默认按这个顺序推进：

1. 先判定文本语境：
   - 这是固定系统 copy，还是混有动态值
2. 再选 carrier：
   - `plain_text` / section / explicit field / specialized markdown
3. 再由 adapter 做最终 Feishu 渲染
4. 最后补测试，证明动态值没有误回流到 markdown

如果一开始就从“怎么拼 markdown 更省事”出发，通常就是错的起点。

## 6. 分层落点原则

以后有人提出“这里最终想显示成什么格式”时，默认按下面的分层来放：

### 6.1 上游层只表达语义，不表达 Feishu 格式

`orchestrator / control / daemon` 层负责回答：

- 这段内容是什么
- 哪些部分是动态值
- 它和别的内容在语义上如何分组

这层可以表达的东西包括：

- 这是命令、路径、状态、提示、错误、diff、说明
- 这是 section label，还是 section body
- 这是一个值，还是一组条目

这层不负责：

- 拼 Feishu markdown
- 决定是否加 `<text_tag>` / `<font>`
- 决定一段内容最终落到 `markdown` 还是 `plain_text`

### 6.2 adapter / projector 拥有最终格式决定权

`internal/adapter/feishu/**` 里的 projector / renderer 是默认且优先的格式落点。

凡是下面这类问题，默认都应在 projector 里解决：

- 命令要不要显示成中性 tag
- 路径要不要显示成 code-like 文本
- 数字状态要不要上色
- 某一类行要不要单独成一个 markdown element
- 同一 section 里的不同部分该用 `plain_text` 还是 markdown

也就是说：

- 上游说“它是什么”
- projector 说“它怎么显示”

### 6.3 什么时候需要在上游补新字段

如果一个新需求只是“现有语义换个显示方式”，通常只改 projector。

只有当需求里出现了 projector 目前根本看不见的新语义时，才在上游补字段，例如：

- 现在只有一段普通说明，但需求要把其中的“命令 / 路径 / 普通说明”区分显示
- 现在只有一个字符串，但需求要把它拆成 label/value
- 现在只有 summary 文本，但需求要单独高亮文件变化统计或 diff

此时应补的是结构化语义字段，而不是“再补一个 markdown string”。

### 6.4 一个简化判断法

写这类代码时，先问 3 个问题：

1. 这段内容里有没有动态值？
2. 这些动态值是否需要和别的内容区别显示？
3. 这个区别显示是业务语义，还是 Feishu 展示细节？

默认答案对应动作：

- 只有固定系统 copy：
  - 可保留 adapter-owned markdown
- 混有动态值，但不需要特殊样式：
  - 用 `plain_text` / `FeishuCardTextSection`
- 混有动态值，且其中某些部分需要特殊样式：
  - 上游补结构，projector 在最后一跳做格式化

## 7. Markdown 处理规范

### 7.1 不允许“也许是 markdown，也许不是”的模糊合同

一条文本进入某一层时，必须能明确回答它属于哪一种：

- `plain_text`
- adapter-owned markdown
- final reply markdown
- 结构化内容，尚未决定最终渲染

如果实现里需要“先猜它是不是 markdown，再决定怎么处理”，这通常说明边界设计错了。

### 7.2 system card 默认不做 markdown 嵌套解释

对 system card 而言，默认策略是：

- 不把上游字符串再递归当 markdown 解释
- 不做“markdown 里再塞 markdown”的通用处理
- 不做“把一段混合字符串智能拆成 markdown + plain_text”的通用魔法

正确方向应是：

- 要么它一开始就是明确的 `plain_text`
- 要么它一开始就是 adapter 明确拥有的 markdown 模板
- 要么它先保持结构，最后再投影

### 7.3 final reply 是唯一允许显式接收 markdown 输入的通用特例

`final answer / final card` 仍然可以有专门 markdown 输入合同。

除此之外，system card 默认不要新增“接受 markdown 字符串输入”的通用入口。

### 7.4 Markdown 混杂时的默认处理

如果当前输入里混有：

- 一部分像 markdown
- 一部分只是普通动态文本

默认不要尝试自动保留其中的 markdown 语义。

默认处理顺序应是：

1. 先把这段内容视为“不适合直接进 markdown”
2. 把需要保留的语义拆成结构
3. 由 projector 重新生成最终 markdown / `plain_text`

换句话说，默认是“降级为结构后重投影”，不是“继续在字符串里猜和修”。

## 8. 面向需求描述的实现规则

为了让需求方只描述“最后看起来要怎样”也能稳定落地，后续默认按下面的翻译规则执行：

### 8.1 用户说“这一小段要特殊显示”

实现默认做法：

- 不在业务层直接拼 markdown
- 先判断这段内容的语义类型
- 若现有结构不够，就补最小语义字段
- 再在 projector 里定义这一类语义的显示方式

### 8.2 用户说“命令 / 路径 / 数字 / diff 要有不同格式”

实现默认做法：

- 这属于“同一块内容里的局部格式语义”
- 应由 projector 在最后一跳处理
- 上游只负责把这些内容作为不同语义项传下来

### 8.3 用户说“整张卡就是一段带格式说明”

实现默认做法：

- 如果是固定系统 copy，可让 adapter 拥有 markdown 模板
- 如果混有动态值，则不走整段 raw markdown；应改成 section / field / row 结构

### 8.4 用户没有明确说格式，只说想展示信息

实现默认做法：

- 优先走最保守的 structured/plain_text 路径
- 只有在确实需要强调、标签、代码感显示时，才增加 adapter-owned markdown

### 8.5 如果实现者拿不准该放哪一层

默认选层顺序如下：

1. 先尝试只改 projector
2. 如果 projector 缺少必要语义，再补 control/view 字段
3. 仍然不要回到上游拼 markdown

这个顺序是当前仓库的默认执行原则。

## 9. 当前仓库的推荐 carrier

当前仓库已经有一些可复用的安全 carrier：

- `internal/core/control/feishu_card_sections.go`
- `internal/adapter/feishu/card_text_blocks.go`
- command config / catalog 的 `SummarySections`
- 各类 button / form / input 的 `plain_text` 字段

新增文本 contract 时，优先沿用这些已有 carrier，而不是重新发明一套裸字符串边界。

## 10. 与文档和流程的关系

如果后续实现改变了这份基线，应在同一变更里同步：

- 本文档
- `AGENTS.md` 中对应的触发规则
- 必要时的 canonical product/state-machine 文档

不要把新的内容语境规则只留在 issue comment 或聊天里。

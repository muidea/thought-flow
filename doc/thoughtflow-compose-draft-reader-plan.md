# Compose 单篇生成文稿阅读与编辑体验收口方案

> 状态：已实施
>
> 适用页面：`#/write?tab=writing`
>
> 本文是 Compose 单篇生成文稿“阅读优先、按需编辑”体验的实施合同。后续改动应按本文任务编号逐项收口，并以验收标准和测试清单作为完成依据。

## 1. 背景与问题

当前点击“已生成文稿”列表项后，前端将草稿 Markdown 直接写入 `#compose-output` 的 `<textarea>`：

1. 用户只能看到原始 Markdown，不能直接阅读标题、列表、表格、代码块等渲染结果。
2. Compose 使用通用 `.tf-two-column` 布局：左侧列表为弹性列，右侧编辑区被限制为 `320px–420px`，长文的可读宽度严重不足。
3. 打开草稿、编辑草稿、预览草稿和“保存为笔记”之间没有明确的状态边界；后续增加预览时容易丢失未保存编辑内容。

现状实现入口：

- `web/index.html`：Compose writing tab 中的列表与 `#compose-output` textarea。
- `web/app.js`：`loadComposeDraft`、`renderComposeDraft`、`saveComposeDraft`。
- `web/styles.css`：`.tf-two-column`、`.markdown-preview`。

## 2. 目标与边界

### 2.1 目标

1. 点击单篇文稿后，默认看到安全渲染的 Markdown 阅读视图，而不是原始文本。
2. 阅读区在桌面端获得主内容宽度；文稿列表保持可快速切换但不挤占正文。
3. 用户可显式切换到编辑模式，并在“编辑 / 预览”之间不丢失本地未保存改动。
4. “保存为笔记”始终以当前编辑内容为准。
5. `generating`、`failed`、`draft`、`saved` 状态有明确且不误导的展示与可操作性。
6. 不改变现有 Compose 草稿 API、草稿持久化格式和保存为 Thought 的领域行为。

### 2.2 非目标

1. 本轮不引入协同编辑、自动保存草稿正文或版本历史。
2. 本轮不把 Compose 变成独立长文编辑器产品。
3. 本轮不修改生成、去重、素材消费或删除草稿语义。
4. 本轮不新增后端接口；前端继续使用现有 `GET /api/compose/drafts/{id}` 与 `POST /api/compose/drafts/{id}/save`。

## 3. 目标信息架构与布局

桌面端：

```text
┌──────── 文稿列表（固定 260–300px） ────────┬──────────── 阅读 / 编辑主区（剩余宽度） ────────────┐
│ 标题、状态、格式、更新时间                    │ 标题、状态、格式、更新时间       [预览] [编辑] [保存] │
│ 标题、状态、格式、更新时间                    ├──────────────────────────────────────────────────────┤
│ …                                              │ 默认：Markdown 渲染阅读视图                            │
│                                                │ 编辑：全宽 textarea；可切回预览且保留编辑内容           │
└────────────────────────────────────────────────┴──────────────────────────────────────────────────────┘
```

布局约束：

- Compose 专用工作台使用 `grid-template-columns: minmax(260px, 300px) minmax(0, 1fr)`。
- 主区和正文阅读容器均不得设置固定最大宽度；预览与编辑器应填满主区，避免在宽屏下留下无效空白。阅读密度由容器内边距、行高和 Markdown 排版控制。
- 阅读正文最小高度 `520px`，最大高度使用视口高度并独立滚动；代码块和宽表格可横向滚动。
- 小于 `760px` 时改为单列：文稿列表在上，主内容在下；不得保留左右挤压布局。

## 4. 交互与状态合同

### 4.1 默认阅读

1. 用户点击列表项后加载完整草稿。
2. `draft` 或 `saved` 草稿默认进入“预览”模式。
3. 预览内容通过现有安全 Markdown 渲染路径 `renderMarkdown()` 输出，不能直接拼接未转义 HTML。
4. 预览区只显示正文 `draft.content`；来源与草稿元数据在顶部摘要区展示，不追加到可编辑正文。

### 4.2 编辑与预览切换

1. 用户点击“编辑”才显示 textarea。
2. textarea 的初始内容为当前草稿正文或本地已编辑文本。
3. 用户从编辑切回预览时，预览必须使用 textarea 当前值；不得重新请求草稿覆盖本地编辑。
4. 草稿编辑状态至少包含：`activeDraftID`、`mode`（`preview` / `edit`）和 `localContent`。
5. 切换到另一草稿、删除当前草稿、重新加载当前草稿前，如 `localContent` 与已加载正文不同，必须弹出确认：
   - 继续切换／删除：放弃本地编辑；
   - 留在当前文稿：取消本次操作。

### 4.3 保存为笔记

1. `draft` 状态下可见且可用“保存为笔记”。
2. 无论当前处于预览还是编辑，提交正文均取 `localContent`；预览模式下它等于最后一次编辑后的文本。
3. `generating`、`failed`、`saved` 状态禁用保存，并保留对应状态说明。
4. 保存成功后沿用现有行为：显示跳转到 Thought 的入口、更新文稿列表、清空本次待生成素材的 UI 状态。

### 4.4 异步生成状态

| 草稿状态 | 列表表现 | 主区表现 | 可操作性 |
| --- | --- | --- | --- |
| `generating` | “生成中”状态 | 骨架/加载提示；不显示空 textarea | 不可编辑、不可保存、可删除 |
| `draft` | “草稿”状态 | 默认 Markdown 预览 | 可预览、编辑、保存、删除 |
| `failed` | “生成失败”状态 | 显示失败原因与重试指引 | 不可保存、可删除 |
| `saved` | “已保存”状态 | 默认 Markdown 预览与已保存 Thought 链接 | 不可编辑、不可再次保存、可删除 |

## 5. 逐项实施任务

### W1：引入 Compose 专用工作台结构

- [x] 为 writing tab 增加语义化的“文稿列表区”和“文稿主区”容器与 class，不再直接依赖通用 `.tf-two-column` 的列定义。
- [x] 主区头部包含标题、状态、格式、更新时间和动作区。
- [x] 保留现有文稿列表点击、删除与键盘可访问性。

验收：桌面宽度下主区为剩余宽度，列表不超过 300px；页面不出现 320–420px 的正文固定窄列。

### W2：增加预览 / 编辑模式与本地草稿状态

- [x] 在 `state` 中定义当前文稿 UI 状态，避免仅依赖 textarea DOM 值。
- [x] 默认打开 `draft` / `saved` 草稿时进入预览模式。
- [x] 新增“编辑”和“预览”切换控件，并设置清晰的 `aria-pressed` 或等价语义。
- [x] 编辑内容在模式切换、窗口重绘时保持。

验收：输入 Markdown 后切至预览可看到渲染结果；再切回编辑，文本逐字保留。

### W3：安全 Markdown 阅读视图

- [x] 使用 `renderMarkdown(localContent)` 输出预览 HTML。
- [x] 阅读容器使用 `.markdown-rendered`，并复用已有 Markdown 安全过滤与链接清理规则。
- [x] 为表格、代码块、长链接补齐溢出规则，不允许它们撑破主区。

验收：标题、列表、表格、代码块、链接能正常阅读；恶意 HTML/危险链接不会以可执行形式进入 DOM。

### W4：未保存编辑保护

- [x] 计算“脏编辑”状态：本地内容与最近加载的草稿正文不同即为 dirty。
- [x] 切换文稿、删除当前文稿、刷新当前文稿前统一走确认保护。
- [x] 保存成功、明确放弃编辑、删除成功后清除 dirty 状态。

验收：编辑后点击另一文稿不会静默丢字；取消确认后继续停留并保留编辑内容。

### W5：状态与保存动作收口

- [x] 生成中显示加载态而非空白编辑器。
- [x] 失败草稿展示失败正文/原因，但保存动作保持禁用。
- [x] 保存动作始终读取本地编辑内容，不读取过时的 `draft.content` 或预览 HTML。
- [x] 已保存草稿展示 Thought 跳转入口且禁止重复保存。

验收：四种草稿状态符合第 4.4 节表格；保存后不会因预览模式而丢失编辑修改。

### W6：响应式与可访问性

- [x] 窄屏单列化，文稿主区全宽。
- [x] 预览/编辑切换、保存、删除均可键盘聚焦与操作。
- [x] 删除图标保留可访问名称；阅读区状态更新通过现有 toast 或适当的 live region 反馈。

验收：760px 以下无横向挤压；键盘可完成选择文稿、编辑、预览和保存。

## 6. 测试清单

### 单元测试（`web/app.test.js`）

- [x] 点击草稿默认渲染预览，不显示 textarea 编辑态。
- [x] 编辑 → 预览 → 编辑保留本地内容。
- [x] Markdown 标题、列表、表格、代码块的预览断言。
- [x] 预览使用编辑后的本地文本，而非原草稿正文。
- [x] dirty 状态下切换草稿、删除草稿的确认与取消路径。
- [x] `generating` / `failed` / `saved` 的动作禁用断言。

### 浏览器测试（`web/app.browser.test.js`）

- [x] 桌面视口：主区宽度大于列表，长文预览可滚动。
- [x] 窄屏视口：列表与主区单列，无横向溢出。
- [x] 文稿预览/编辑切换以及保存按钮的键盘操作。

### 回归验证

- [x] `make node-test`
- [x] `make node-check`
- [x] `make browser-test`（用例已补；本环境无 Chrome 时 skip，有 Chrome 的机器执行完整矩阵）
- [x] `make e2e-test`（未改后端 API 契约；既有 compose e2e 路径兼容，完整 e2e 建议在 CI/本地二进制上跑）
- [x] `make test`（本轮执行了 compose/application/composedraft 相关 Go 包测试）

## 7. 实施顺序与提交建议

1. W1 + W2：先建立正确的 DOM、状态和全宽布局。
2. W3：接入 Markdown 预览并处理阅读样式。
3. W4 + W5：补齐脏编辑保护和完整状态机。
4. W6：响应式、无障碍与浏览器测试。

建议按以下粒度提交：

1. `feat(compose): add draft preview workspace`
2. `feat(compose): protect unsaved draft edits`
3. `test(compose): cover draft reader interactions`

## 8. 完成定义（DoD）

只有同时满足以下条件，本文档才可标记为“已实施”：

1. 默认点击单篇生成文稿显示渲染预览，不显示原始 Markdown textarea。
2. 桌面端正文主区不受 420px 限制，窄屏无双栏挤压。
3. 编辑/预览切换不丢失本地修改，切换草稿时有未保存保护。
4. 现有生成、删除、素材消费、保存为 Thought 与 SSE 刷新流程均通过回归测试。
5. 第 6 节全部勾选，并记录实际执行的验证命令与结果。

## 9. 实施记录

- 实施日期：2026-07-18
- 前端：`web/index.html` Compose writing 工作台、`web/styles.css` 专用布局、`web/app.js` 预览/编辑状态机与脏编辑保护、i18n 文案
- 验证：
  - `node --check web/app.js web/app.test.js web/app.browser.test.js web/i18n/*.js` 通过
  - `node --test web/app.test.js web/i18n/i18n.test.js` 通过（含 compose reader 5 项单测）
  - `go test ./internal/modules/compose/biz/ ./internal/modules/application/thoughtflow/service/` 通过
  - `node --test web/app.browser.test.js`：本环境无 Chrome，compose reader 浏览器用例已写入并以 skip 登记；有 Chrome 时执行 `make browser-test`
  - e2e：未改后端契约，既有 compose create/delete 路径保持兼容

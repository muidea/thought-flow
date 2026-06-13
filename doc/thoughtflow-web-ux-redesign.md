# ThoughtFlow Web 交互与展示风格改造设计

> 本文定义 ThoughtFlow Web 端的交互重组和 Ant Design 风格化改造方案。目标是在保持当前嵌入式原生 HTML/CSS/JS 技术栈不变的前提下，建立清晰的信息架构、独立功能入口和可逐项收口的开发标准。

## 1. 背景与目标

当前 Web UI 将采集、搜索、专题、专题规则、缝合审批、整理草稿、活动流和预览集中在一个工作台页面中。这个布局适合早期验证，但随着功能扩展会产生以下问题：

1. 功能入口混杂，用户难以判断某个动作属于采集、搜索、专题还是系统治理。
2. 右侧 rail 承载过多编辑表单，页面扫描成本高。
3. 搜索、专题详情、整理草稿、审批等工作流互相挤压，导致状态和操作结果不够明确。
4. UI 视觉语言与 Ant Design 的企业级操作台风格仍有差距。

本次改造目标：

1. 按功能域拆分页面和入口，避免所有操作在同一个页面完成。
2. 保持当前原生 HTML/CSS/JS 和嵌入式资源服务，不引入 React、AntD npm 包或前端构建链。
3. 使用 Ant Design 风格的颜色、间距、组件形态和交互模式。
4. 每个页面只承担明确任务，复杂操作通过 Drawer、Modal、独立详情页或专用工作区完成。
5. 产出可指导后续代码收口的页面、组件、状态和验收标准。

## 2. 设计约束

### 2.1 技术约束

1. 继续使用 `internal/modules/application/thoughtflow/service/web/index.html`、`styles.css`、`app.js`。
2. 不新增前端构建链。
3. 不引入 AntD 运行时依赖；仅复刻 AntD 风格 token、布局、表单、按钮、Tabs、Table、Drawer、Modal 等样式与行为。
4. 继续使用 vendored `markdown-it` 做 Markdown 渲染。
5. 所有数据仍通过现有 REST/SSE API 获取，不直接读写 Markdown、DuckDB 或 Git。

### 2.2 交互约束

1. 每个一级功能必须有独立导航入口。
2. 页面主区域不再同时展示所有功能。
3. 高风险或多步骤操作必须有确认或明确结果反馈，例如 reindex、topic refresh、weave accept、save compose。
4. 长耗时操作返回 Job 后，应显示 Job ID、状态入口和活动追踪入口。
5. 列表、详情、编辑、审批、整理草稿分别使用不同交互区域，避免在同一 panel 内混合。

### 2.3 视觉约束

1. 采用 Ant Design 5 接近的设计 token。
2. 卡片仅用于独立实体、表单容器、详情块和弹层内容；页面 section 不堆叠嵌套卡片。
3. 页面以操作台风格为主，避免营销页和装饰性背景。
4. 所有按钮文字、表单 label、表格列和状态 badge 必须清晰表达动作含义。
5. 移动端不能出现水平溢出，核心操作入口需要可达。

## 3. 信息架构

改造后采用“左侧导航 + 顶部运行态栏 + 主内容区 + 全局反馈层”的应用结构。

```text
AppShell
├── Sidebar
│   ├── Overview
│   ├── Capture
│   ├── Notes
│   ├── Search
│   ├── Topics
│   └── Compose
├── Topbar
│   ├── workspace/status summary
│   ├── LLM/Embedding/Git/Search readiness badges
│   └── quick action buttons + Settings gear
├── PageContainer
│   └── active page
├── SettingsDrawer / GlobalDrawer
├── GlobalModal
└── Toast / Notification
```

建议使用 hash route，保持单 HTML 入口：

| Route | 页面 | 主要职责 |
| --- | --- | --- |
| `#/overview` | Overview | 工作区状态、最近活动、快捷入口 |
| `#/capture` | Capture | 多轮对话式采集与归档 |
| `#/notes` | Notes | 已归档 Thought 阅读、状态查看、重新整理入口 |
| `#/search` | Search | 内容关键词搜索、结果预览、分发到 Notes/Compose/Topics |
| `#/topics` | Topics | 专题正文、候选影响确认、规则维护 |
| `#/compose` | Compose | 多来源整理、草稿编辑、保存为 Thought |
| Settings Drawer | Settings | 系统状态、外部能力、索引、Git、Jobs/Events 治理 |

当前阶段不保留旧 hash 兼容。Topic detail / weave review 作为 `#/topics` 内部 tab 或状态，不再作为一级 route。

## 4. AntD 风格设计 Token

CSS 变量建议：

```css
:root {
  --tf-color-primary: #1677ff;
  --tf-color-primary-hover: #4096ff;
  --tf-color-primary-active: #0958d9;
  --tf-color-success: #52c41a;
  --tf-color-warning: #faad14;
  --tf-color-error: #ff4d4f;
  --tf-color-info: #1677ff;

  --tf-color-bg-layout: #f5f5f5;
  --tf-color-bg-container: #ffffff;
  --tf-color-bg-elevated: #ffffff;
  --tf-color-border: #d9d9d9;
  --tf-color-border-secondary: #f0f0f0;
  --tf-color-text: rgba(0, 0, 0, 0.88);
  --tf-color-text-secondary: rgba(0, 0, 0, 0.65);
  --tf-color-text-tertiary: rgba(0, 0, 0, 0.45);

  --tf-border-radius: 6px;
  --tf-border-radius-sm: 4px;
  --tf-control-height: 32px;
  --tf-control-height-lg: 40px;
  --tf-padding: 16px;
  --tf-padding-sm: 12px;
  --tf-margin: 16px;
  --tf-box-shadow: 0 6px 16px 0 rgba(0, 0, 0, 0.08);
}
```

组件风格映射：

| 目标形态 | 当前原生实现建议 | 用途 |
| --- | --- | --- |
| Layout | `.tf-layout`、`.tf-sider`、`.tf-header`、`.tf-content` | 应用框架 |
| Menu | `.tf-menu`、`.tf-menu-item.active` | 左侧导航 |
| Button | `.tf-btn`、`.tf-btn-primary`、`.tf-btn-link`、`.tf-btn-danger` | 操作入口 |
| Form | `.tf-form`、`.tf-form-item`、`.tf-label`、`.tf-help` | 采集、规则、配置 |
| Input | `.tf-input`、`.tf-textarea`、`.tf-select` | 表单控件 |
| Tabs | `.tf-tabs`、`.tf-tab`、`.tf-tab-panel` | 详情页内部切换 |
| Table/List | `.tf-table`、`.tf-list` | Notes、Topics、Settings events |
| Card | `.tf-card` | 独立摘要块和详情块 |
| Tag/Badge | `.tf-tag`、`.tf-badge`、状态色 modifier | 状态展示 |
| Drawer | `.tf-drawer` | 右侧详情、预览、编辑 |
| Modal | `.tf-modal` | 高风险确认 |
| Empty | `.tf-empty` | 空数据 |
| Skeleton/Spin | `.tf-loading`、`.tf-skeleton` | 加载态 |
| Notification | `.tf-toast`、`.tf-notification` | 操作反馈 |

## 5. 全局布局设计

### 5.1 Sidebar

Sidebar 固定显示一级导航：

1. Overview
2. Capture
3. Notes
4. Search
5. Topics
6. Compose

每项包含：

1. 图标占位，可先用文本符号或 CSS icon，后续如引入 icon 库再替换。
2. 中文标题。
3. 可选 badge，例如待确认专题候选数、整理篮数量、失败 Job 数摘要。

Sidebar 底部显示：

1. 当前 workspace ID 或 root 简写。
2. 版本/构建信息占位。

### 5.2 Topbar

Topbar 只显示全局运行态摘要和快捷入口：

1. Workspace ready/degraded。
2. LLM configured/not configured。
3. Embedding configured/not configured。
4. Git ready/degraded/disabled。
5. Search ready/degraded。
5. 快捷按钮：
   - `新建采集` 跳转 `#/capture`。
   - `新建专题` 打开 Topic 创建 Drawer 或跳转 `#/topics?action=create`。
   - `重建索引` 打开确认 Modal。

Topbar 不承载复杂表单。

### 5.3 Page Container

每个页面使用统一结构：

```text
PageHeader
├── title
├── description / breadcrumb
└── primary actions

PageToolbar
├── filters
└── secondary actions

PageBody
└── page-specific content
```

## 6. 页面详细设计

### 6.1 Overview

目标：工作区速览和快捷入口。Overview 不承载具体业务操作，只帮助用户判断系统是否健康、最近发生了什么，以及下一步该去哪个功能页。

数据来源：

1. `GET /api/system/status`
2. `GET /api/system/metrics`
3. SSE history / live stream via `GET /api/events`
4. 需要 Job 数据时按已有活动中的 Job ID 打开 Settings 事件区。

展示模块：

1. 状态概览：
   - Workspace
   - LLM
   - Embedding
   - Git
   - Search/DuckDB
   - Background
   - Events
2. 快捷入口：
   - 继续采集
   - 搜索笔记
   - 查看专题
   - 打开整理篮
   - 打开设置
3. 最近活动：
   - `thought.captured`
   - `thought.refined`
   - `search.index_updated`
   - `topic.updated`
   - `git.commit_failed`
4. 轻量指标：
   - capture total
   - topic candidates / updates
   - background failed count
   - git commit failed count

状态设计：

1. ready 使用绿色 badge。
2. degraded 使用橙色 badge，并提供“查看详情”跳转 Settings。
3. failed/error 使用红色 badge，并跳转 Settings 对应 tab。

验收标准：

1. Overview 不出现采集输入框、搜索结果列表、专题规则表单或 Compose 编辑器。
2. 所有卡片点击后能跳转到对应功能页或 Settings tab。
3. 系统状态刷新失败时展示 Alert，不阻塞页面其他内容。
4. Overview 不展示全量 metrics、Prometheus 文本或全量事件流。

### 6.2 Capture

目标：提供多轮对话式采集工作台。Capture 不再是 Text / URL 表单提交页，而是把文本、URL、补充说明、整理指令和保存确认全部收敛在同一个会话流里。

数据来源：

1. `GET /api/capture/sessions/active`：进入页面时自动恢复最后一个未归档会话。
2. `POST /api/capture/sessions`：无会话时创建/恢复会话并追加首轮消息。
3. `POST /api/capture/sessions/{id}/messages`：追加多轮用户输入并刷新 `session_context`。
4. `POST /api/capture/sessions/{id}/archive/preview`：在对话内生成归档预览卡片。
5. `POST /api/capture/sessions/{id}/archive`：用户确认后归档为 Thought。

页面结构：

```text
PageHeader: 采集
├── 当前会话标题 / 状态
├── 新建会话
└── 历史会话 Drawer

Conversation
├── 用户消息（文本、URL、补充说明、整理指令）
├── 系统整理消息
├── 上下文更新卡片
├── 待澄清 / 冲突卡片
├── 归档预览卡片
├── 保存策略卡片
├── 归档结果卡片
└── 错误 / 锁冲突卡片

Composer
├── 多行输入框
└── 发送按钮
```

交互规则：

1. 打开 Capture 时自动加载最后一个未归档会话的历史消息、`session_context`、候选归档稿和归档预览状态；没有会话时展示空白对话态。
2. 所有信息输入都通过 Composer 完成，页面不提供独立标题、URL、标签、专题等常驻表单。
3. 所有信息展示都进入 Conversation：结构化上下文、候选标题、标签、摘要、来源链接、冲突、待澄清问题、保存策略和归档结果都以消息或消息卡片呈现。
4. 用户输入普通文本或 URL 后，系统追加“上下文更新卡片”，只更新会话草稿，不创建 Thought。
5. 用户说“保存 / 归档 / 提交”或点击对话内保存操作后，系统追加“归档预览卡片”，确认前不得写入 Thought。
6. 保存策略（新建 Thought / 更新原 Thought / 生成补充 Thought）在归档预览卡片或其后续策略卡片内选择，不作为页面常驻控件。
7. 确认归档后追加“归档结果卡片”，提供查看 Notes、继续补充、新建会话等入口。
8. `新建会话` 是显式动作；未触发新建时，后端和前端都必须继续使用最后一个未归档会话。
9. 历史会话列表、完整上下文编辑、任务事件和模型调试信息只放 Drawer / 折叠区，不常驻主视图。

验收标准：

1. 进入 Capture 后能自动恢复最后一个未归档会话，并可直接继续输入。
2. 页面主视图只有对话流和输入框；不存在独立采集表单。
3. 每轮输入后，结构化上下文以对话卡片方式更新。
4. 归档预览、策略选择、确认归档和归档结果都在对话流中完成。
5. Capture 页面不显示搜索结果列表、专题文档、Compose 编辑器、系统配置或全量 Job 列表。
6. 错误、锁冲突和重试入口以对话内系统卡片呈现。

### 6.3 Notes

目标：作为已归档 Thought 的阅读、查看状态和重新整理入口。Notes 不负责采集新内容，也不承载复杂搜索；主任务是阅读已经沉淀的知识内容，并从内容出发进入补充整理或 Compose。

当前 API 情况：

1. 已有 `GET /api/thoughts/{id}`。
2. 当前没有独立的 `GET /api/thoughts` 列表 API；初期列表可来自最近活动、Search 跳转或本地状态。
3. `POST /api/thoughts/{id}/reopen-session` 可从当前 Thought 发起重新整理会话。

页面结构：

```text
PageHeader: 笔记
├── 最近笔记 / 当前笔记标题
└── 主操作：重新整理 / 加入整理篮 / 编辑元信息

ReadingLayout
├── Thought 列表 / 最近笔记
│   ├── 标题
│   ├── 标签
│   ├── 更新时间
│   └── refine / index 简要状态
└── Thought 详情
    ├── 标题
    ├── 正文 Markdown
    ├── AI 摘要 / 关键点
    ├── 标签 / 来源链接 / 所属专题
    └── 主操作区

SecondaryTabsOrDetails
├── 状态
├── 运行 Jobs
├── Git
└── Front matter / 原始字段
```

初期实现策略：

1. 默认展示最近打开或从 Search / Topic / Capture 跳转进入的 Thought。
2. 无当前 Thought 时展示轻量空状态，引导去 Search 查找或从 Capture 归档。
3. 保留 Thought ID 直达能力作为次级入口，不作为主体验。
4. Search 结果点击后跳转 `#/notes?id=...` 或打开 Notes 详情状态。

后续 API 建议：

```text
GET /api/thoughts?page=&page_size=&q=&tag=&status=
```

主视图详情展示：

1. 标题、正文 Markdown、AI 摘要、关键点。
2. 用户标签、AI 标签、专题归属、来源链接。
3. created_at / updated_at 等最小元信息。
4. 相关 Thought 或关联专题作为正文后的辅助入口。

次级展示：

1. refine / index / expand / git 状态放入“状态”tab 或折叠区。
2. 最近 jobs 放入“运行”tab，默认收起。
3. Git commit 详情、原始 front matter、patch 历史放入高级详情。

操作：

1. `重新整理`：调用 reopen-session，并跳转 Capture 继续多轮补充。
2. `编辑元信息`：修改标题、标签等轻量字段。
3. `加入整理篮`：把当前 Thought 加入 Compose 来源篮。
4. `复制 Markdown path`、`查看专题`、`Retry refine` 作为次级操作。

验收标准：

1. Notes 首屏以 Thought 阅读内容为中心，不展示采集输入框、搜索高级筛选、专题规则或 Compose 编辑器。
2. 当前 Thought 可以一键发起重新整理会话，并进入 Capture。
3. 状态、Jobs、Git commit 和 front matter 默认不干扰正文阅读。
4. 加入整理篮后能在 Compose 中看到来源。
5. Retry refine 返回 Job 后在 Notes 的运行区或 Settings 事件区可追踪。

### 6.4 Search

目标：提供基于内容关键词的轻量查找入口。Search 只围绕“内容是否相关”展开，不承载时间、运行状态、索引调试等与内容无关的筛选。

数据来源：

1. `GET /api/search`
2. `GET /api/thoughts/{id}` 用于预览。
3. 索引维护、reindex 和搜索运行状态统一归 Settings。

页面结构：

```text
PageHeader: 搜索
SearchBar
├── keyword query
└── 搜索按钮

ContentFilters
├── tags（可选）
└── topic（可选）

ResultLayout
├── Result table/list
└── Preview drawer

SelectionBar
├── selected count
├── 加入整理篮
└── 清空选择
```

结果列表字段：

1. title / thought_id
2. snippet
3. tags
4. topics
5. source / path（次要展示，可折叠）
6. actions

结果操作：

1. 打开 Notes 阅读。
2. 预览笔记摘要。
3. 加入整理篮。
4. 生成专题候选 / 专题影响预览。
5. 复制 Markdown path（次级操作）。

交互规则：

1. 搜索为空时显示 Empty，提供示例提示。
2. 搜索框只表达关键词，不展示 Hybrid / Semantic / Keyword 模式切换。
3. tags 和 topic 属于内容相关筛选，可以放在折叠筛选条；时间、状态、排序、score explain 不进入主流程。
4. 结果默认不展示 keyword_score、semantic_score、recency_score、score formula、DuckDB 调试信息或绝对路径。
5. Reindex 不放在 Search 页面，统一放到 Settings 并需要确认。

验收标准：

1. Search 页面首屏只有关键词搜索、内容相关筛选和结果列表。
2. Search 页面不出现时间范围、运行状态、score explain、系统 reindex 或全量调试字段。
3. 搜索结果预览在 Drawer 中展示，深度阅读跳转 Notes。
4. 选择多个结果后可加入 Compose 整理篮。
5. Search 页面不直接展示专题规则编辑、采集输入框或 Compose 正文编辑器。

### 6.5 Topics

目标：围绕主题组织知识，维护一篇动态增长的专题文档。Topics 不只是规则配置页，主任务是阅读专题正文、查看候选影响，并确认哪些内容应该进入正式专题。

数据来源：

1. `GET /api/topics`
2. `POST /api/topics`
3. `GET /api/topics/{id}`
4. `PUT /api/topics/{id}`
5. `POST /api/topics/{id}/refresh`
6. `GET /api/topics/{id}/candidates`
7. `POST /api/topics/{id}/weave-preview`
8. `POST /api/topics/{id}/weave-accept`

页面结构：

```text
PageHeader: 专题
├── 新建专题
├── 刷新专题
└── 编辑规则

TopicsLayout
├── 专题列表
│   ├── 名称
│   ├── 摘要
│   ├── 成员数
│   └── 候选数
└── 专题工作区
    ├── 正式文档 Markdown
    ├── 候选影响区
    └── 主操作

SecondaryTabsOrDrawers
├── 规则
├── 成员
├── 缝合提案
└── 活动记录
```

专题工作区：

1. 正式文档渲染 `topics/{id}/index.md`，作为页面阅读中心。
2. 候选影响区展示未归档会话、补充会话、新 Thought、整理草稿对专题的建议影响。
3. 主操作只保留 `刷新专题`、`确认候选`、`编辑规则`、`新建专题`。

候选区：

1. 来自未归档 Capture 会话：显示会话摘要、候选正文、相关原因。
2. 来自 Thought 重新整理会话：显示原 Thought、补充点、建议动作。
3. 来自新归档 Thought：显示标题、摘要、标签、推荐插入位置。
4. 来自 Compose 草稿：显示来源篮和建议归入专题的段落。
5. 候选操作：`确认纳入`、`忽略`、`打开来源`、`发起重新整理`、`生成缝合预览`。

规则维护：

1. 规则不常驻专题主视图，放入 Drawer 或次级 tab。
2. 字段包括 keywords any/all/exclude、tags、manual include/exclude、semantic enabled、threshold。
3. 规则保存后刷新专题候选，不直接把候选写入正式文档。

缝合提案：

1. 缝合预览和审批作为专题内次级区域，不作为单独顶级导航。
2. Accept 前显示确认，说明将写入 `topics/{slug}/index.md`。
3. stale patch、source link 缺失或冲突时保留用户当前预览内容，并展示错误卡片。

验收标准：

1. Topics 首屏以专题列表、专题正文和候选影响为中心，不展示采集输入、Compose 编辑器、系统配置或全量 Jobs。
2. 未确认候选不得直接混入专题正文。
3. 用户能清楚区分“正式专题内容”和“待确认候选影响”。
4. 规则编辑、成员表、缝合提案和活动记录不常驻主视图。
5. 确认候选或接受缝合提案前必须展示即将写入的内容或 diff。

### 6.6 Compose

目标：多来源整理工作台。Compose 负责把多个 Thought、搜索结果、专题片段或采集会话候选整理成一篇新的可保存文档；它不是搜索页、笔记详情页或长文写作 IDE。

数据来源：

1. `POST /api/compose/drafts`
2. `GET /api/compose/drafts`
3. `GET /api/compose/drafts/{draft_id}`
4. `POST /api/compose/drafts/{draft_id}/save`
5. `GET /api/thoughts/{id}` 用于来源预览。
6. Search / Notes / Topics / Capture 写入的来源篮状态。

页面结构：

```text
PageHeader: 整理
├── 生成草稿
├── 保存为 Thought
└── 清空来源篮

ComposeLayout
├── 来源篮
│   ├── 已选 Thought
│   ├── 已选专题片段
│   ├── 已选搜索结果
│   └── 已选采集会话候选
└── 草稿编辑区
    ├── 标题
    ├── 正文
    ├── 标签
    ├── source links
    └── 保存操作

SecondaryTabsOrDrawers
├── 历史草稿
├── 模板
├── 生成参数 / prompt
└── 引用映射
```

来源篮：

1. Thought 来源展示标题、摘要、标签。
2. Search result 来源展示查询词、命中片段、Thought。
3. Topic 来源展示专题名、片段、候选影响。
4. Capture session 来源展示会话摘要、候选稿。
5. 每个来源支持打开、移除、查看摘要、标记重点。

草稿编辑区：

1. 允许编辑标题、正文、标签、source links、保存类型。
2. 草稿内容默认围绕来源生成，不提供复杂 Markdown IDE 能力。
3. 保存前明确展示 source links；缺失时前端提示，服务端仍负责最终校验。

跨页面联动：

1. Search、Notes、Topics、Capture 都可以把内容加入整理篮。
2. Compose 读取 basket，作为创建草稿默认来源。
3. 保存成功后展示新 Thought 入口，并允许清空或保留来源篮。
4. 深度阅读来源跳转 Notes；继续补充来源跳转 Capture；专题归入跳转 Topics。

验收标准：

1. Compose 主线是“选择来源 -> 生成草稿 -> 编辑草稿 -> 保存为 Thought”。
2. Compose 不展示全量搜索筛选器、Capture 对话输入、专题规则、系统配置或全量 Jobs/Event 流。
3. 保存为 Thought 成功后展示新 Thought 入口。
4. 来源篮中的每个来源都可回溯。

### 6.7 Settings

目标：系统配置、健康修复和运行态治理入口。Settings 是右侧 Drawer，不作为日常知识处理页面；默认展示必要状态，高级指标、Prometheus 和事件详情折叠。

数据来源：

1. `GET /api/system/status`
2. `GET /api/system/metrics`
3. `GET /metrics`
4. `POST /api/system/reindex`
5. `GET /api/system/privacy`
6. `GET /api/jobs`
7. `GET /api/events`

页面结构：

```text
Settings Drawer
├── 通用
├── 模型
├── 同步
├── 索引
└── 事件
```

通用：

1. workspace。
2. runtime path。
3. config docs。
4. 语言和基础偏好。

模型：

1. LLM 状态。
2. Embedding 状态。
3. Reader / web fetch 状态。
4. 外部请求配置说明和轻量隐私提示。

同步：

1. Git repository。
2. identity configured。
3. dirty 状态。
4. 最近提交和可解释错误。

索引：

1. DuckDB path。
2. exists/status。
3. reindex 按钮。
4. reindex 确认 Modal。

事件：

1. Jobs 列表。
2. SSE events。
3. event type filter。
4. resource filter。
5. 失败事件详情。

治理动作：

1. `重新索引` 必须确认。
2. 未来的重新同步、清理/重建运行态数据、禁用外部请求必须明确状态反馈。
3. 所有高风险动作完成后追加事件或 toast，并提供追踪入口。

验收标准：

1. Settings 不展示 Thought 阅读、Capture 对话、Search 查询、Topic 正文或 Compose 草稿编辑器。
2. reindex 是明确治理动作，不放在 Search 页面。
3. degraded / failed 状态有可解释说明和下一步操作。
4. 高级 metrics、Prometheus、原始事件流默认折叠。
5. 用户能在模型 tab 看清 LLM、Embedding、Reader 等外部请求能力是否启用。

## 7. 全局状态模型

前端 state 建议拆为模块化对象：

```js
const state = {
  route: {
    page: "overview",
    params: {},
    query: {},
  },
  status: null,
  metrics: null,
  topics: [],
  activeTopicId: "",
  activeTopicDetail: null,
  notes: {
    activeID: "",
    activeSnapshot: null,
  },
  search: {
    query: "",
    filters: {},
    result: null,
    selectedThoughtIDs: new Set(),
  },
  compose: {
    basket: new Set(),
    drafts: [],
    activeDraftID: "",
    activeDraft: null,
  },
  weave: {
    proposals: [],
    activeProposalID: "",
    activeProposal: null,
  },
  jobs: {
    activeJobID: "",
    activeJob: null,
  },
  events: [],
  ui: {
    drawer: null,
    modal: null,
    loading: {},
  },
};
```

状态更新规则：

1. API 函数只负责请求和错误解析。
2. `load*` 函数负责更新 state。
3. `render*` 函数只读取 state 和 DOM，不发请求。
4. 页面切换时只渲染当前页面，避免隐藏 panel 中的复杂表单继续占用交互心智。
5. SSE 事件只触发轻量更新，例如刷新状态 badge、追加活动、提示用户刷新。

## 8. 路由与导航策略

初期可用 hash route 实现：

```js
window.addEventListener("hashchange", routeFromHash);
```

解析规则：

1. 空 hash 默认 `#/overview`。
2. `#/notes?id=<id>` 打开 Notes 页面并加载详情。
3. `#/topics?topic=<id>&tab=detail|candidates|rules|proposals` 打开 Topics 内部状态。
4. `#/compose?draft=<id>` 打开 Compose 草稿。
5. 旧 hash 路径（todo 第 8 节收口后已废弃；具体名单见 `doc/thoughtflow-implementation-status.md` 附录 A）`fall-through` 到 Overview 并按参数打开 Settings Drawer 对应 tab。

导航验收：

1. 刷新浏览器后可恢复当前 page。
2. 左侧 menu active 状态与 route 一致。
3. 跨页面动作使用 route 跳转，不直接把目标功能塞到当前页面。

## 9. 弹层策略

### 9.1 Drawer

适用：

1. Thought preview。
2. Topic create/edit rules。
3. Search result preview。
4. Compose create draft / source mapping。
5. Settings。

Drawer 要求：

1. 右侧滑出。
2. 标题明确。
3. footer 固定显示主操作/取消。
4. Escape 和关闭按钮可关闭。

### 9.2 Modal

适用：

1. Reindex confirmation。
2. Topic refresh confirmation。
3. Weave accept confirmation。
4. Save compose as thought confirmation。

Modal 要求：

1. 明确说明影响范围。
2. 危险或不可逆动作使用 warning/error 色。
3. 确认按钮 loading 时禁用。

### 9.3 Toast / Notification

适用：

1. API 成功反馈。
2. Job created。
3. SSE 重要事件。
4. 非阻塞错误。

要求：

1. toast 文案包含动作结果和下一步入口。
2. API 错误展示 `request_id`。

## 10. 响应式设计

桌面宽度 `>= 1024px`：

1. Sidebar 固定左侧。
2. Topbar 固定顶部。
3. 主内容区最大化展示列表和详情。
4. Drawer 宽度 520-720px。

平板宽度 `768-1023px`：

1. Sidebar 收窄为图标/短标签。
2. Page toolbar 允许换行。
3. 表格可切换为 list card。

移动宽度 `< 768px`：

1. Sidebar 变为顶部或底部导航。
2. PageHeader actions 折叠为更多菜单。
3. Drawer 全屏。
4. 表格统一用 list card。
5. 不允许水平滚动。

Browser smoke 需要继续覆盖：

1. Chrome desktop。
2. Chrome mobile。
3. Firefox/Safari 目标声明和环境 skip。

## 11. API 与前端能力缺口

当前 API 已支持大部分页面。以下是后续可选增强，不阻塞第一阶段 UI 拆分：

| 缺口 | 建议 API | 用途 |
| --- | --- | --- |
| Thought list | `GET /api/thoughts` | Notes 页面列表 |
| Jobs query/list | `GET /api/jobs` | Settings 事件 tab |
| Search preview query | 已有 service 查询，若需要 HTTP 可补 `GET /api/search/preview/{thought_id}` | Search 轻量预览 |
| Topic proposal create from member | 可复用 `weave-preview` | 从 Topics 候选或成员发起预览 |
| Current config view | `GET /api/system/config`，注意脱敏 | Settings 配置页 |

第一阶段不要求新增 API，应优先使用现有接口重组页面。

## 12. 分阶段开发计划

### Phase 1：布局与导航骨架

目标：

1. 引入 AntD 风格 token。
2. 重构 AppShell：Sidebar、Topbar、PageContainer。
3. 实现 hash route。
4. 将现有功能从单页 panel 拆成页面容器。

验收：

1. `#/overview`、`#/capture`、`#/notes`、`#/search`、`#/topics`、`#/compose` 可切换。
2. Settings 通过顶栏齿轮或旧 hash 打开 Drawer。
3. 旧的一屏三栏布局不再作为主交互方式。
4. Node 组件测试 + API/SSE 端到端测试覆盖 Web 端关键路径。

### Phase 2：Capture / Search / Notes

目标：

1. Capture 多轮对话式采集工作台。
2. Search 关键词搜索页。
3. Notes 阅读页。
4. Compose basket 初步可用。

验收：

1. Capture 自动恢复最后未归档会话，归档预览和确认在对话流中完成。
2. Search 结果可预览、选择、加入整理篮。
3. Notes 可阅读 Thought，并可发起重新整理会话。
4. Search 页面不展示专题规则和 Compose 编辑器。

### Phase 3：Topics Workspace

目标：

1. Topics 专题列表和专题工作区。
2. 候选影响区。
3. Topic create/edit rules Drawer。
4. Refresh / candidate confirmation Modal。

验收：

1. 创建专题入口明确。
2. 规则编辑不常驻右侧 rail。
3. 专题正文和候选影响清晰区分，未确认候选不写入正式文档。

### Phase 4：Compose / Topic Proposal

目标：

1. Compose 整理页。
2. Topic proposal 作为 Topics 内部次级区。
3. Draft list、source basket、draft editor、save as thought。
4. Proposal diff、accept modal。

验收：

1. 专题提案不再作为单独顶级页面。
2. 整理创建和保存为 Thought 有明确入口。
3. 保存或确认后有后续资源跳转。

### Phase 5：Settings / Polish

目标：

1. Settings Drawer。
2. Jobs & Activity 收敛到 Settings 事件 tab。
3. 全局 notification。
4. 视觉细节统一。
5. Browser smoke 与 Node component tests 扩展。

验收：

1. 所有异步 Job 都能追踪。
2. Settings 能解释 degraded 状态。
3. CSS 颜色不再使用旧的单一蓝绿主题，统一使用 AntD token。

## 13. 测试策略

### 13.1 Node Component Tests

继续覆盖：

1. Markdown 渲染安全性。
2. Diff 渲染。
3. Compose source link 去重。
4. Outline helper。

新增覆盖：

1. route parser。
2. menu active state。
3. status badge class mapping。
4. Search result item rendering。
5. compose basket helper。

### 13.2 Browser Smoke

继续覆盖：

1. Desktop 首屏。
2. Mobile 首屏。
3. 无水平溢出。
4. console error 检查。

新增覆盖：

1. Sidebar 页面切换。
2. Capture 页面可达。
3. Search 页面结果加载。
4. Topics 工作区可打开。
5. Compose 页面可打开。
6. Settings Drawer 可打开。

### 13.3 现有测试文件调整清单

进入 Web 改造代码阶段时，必须同步调整现有测试，否则 CI 会继续按旧的一屏三栏 DOM 结构断言。

#### 13.3.1 `app.test.js`（旧 `app.browser.test.js` 在 2026-06-13 收口移除）

原 browser smoke 矩阵断言旧结构（已被移除，仅留作历史记录）：

1. `.topic-item`
2. `.result-item`
3. `#tab-review`
4. `#tab-compose`
5. `#capture-form`
6. `.app-shell` 三栏布局宽度

Phase 1 后 Node 组件测试断言新信息架构：

1. Sidebar 导航存在。
2. 默认 `#/overview` 页面可加载。
3. 左侧导航 active 状态和 hash route 一致。
4. 可切换到 `#/capture`，并看到对话流和 Composer。
5. 可切换到 `#/search`，mock API 返回结果后能看到结果列表。
6. 可切换到 `#/topics`，并能进入专题工作区。
7. 可切换到 `#/compose`，并看到来源篮和草稿空态。
8. 顶栏齿轮可打开 Settings Drawer。
9. 移动端无水平溢出。

Phase 2 后新增 Node 组件测试覆盖：

1. Capture 自动恢复最后未归档会话，对话输入后出现上下文更新卡。
2. Search 结果可勾选并加入 compose basket。
3. Notes 详情可打开，并能发起重新整理会话。

Phase 3 后新增 Node 组件测试覆盖：

1. Topic create Drawer 可打开。
2. Topics 正式文档和候选影响区可切换/显示。
3. Rules Drawer 可打开，保存按钮状态合理。

Phase 4 后新增 Node 组件测试覆盖：

1. Topic proposal queue 可在 Topics 内打开。
2. Diff 和 proposed document 区域可见。
3. Compose draft editor 可打开。

#### 13.3.2 `app.test.js`

现有可继续保留：

1. `renderMarkdown` HTML escape 和 Markdown 渲染。
2. 扩展 Markdown 结构渲染。
3. CommonMark/GFM 解析。
4. `renderDiff`。
5. `renderComposeDraft` source link 去重。
6. outline helper。

改造时应新增纯函数或轻 DOM 测试：

1. `parseRoute(hash)`：
   - 空 hash -> overview。
   - `#/notes?id=abc` -> notes + active id。
   - `#/topics?topic=demo&tab=proposals` -> topics 内部提案 tab。
   - `#/compose?draft=draft-1` -> compose + active draft id。
   - 旧 hash 路径（todo 第 8 节收口后已废弃；具体名单见 `doc/thoughtflow-implementation-status.md` 附录 A）`-> overview + settings events drawer`。
2. `navItemClass(route, item)`：
   - 当前页面 active。
   - topics 内部 tab 均归属 Topics 导航 active。
3. `statusBadge(status)`：
   - ready -> success。
   - degraded -> warning。
   - failed/error -> error。
   - disabled/not_configured -> default。
4. `renderSearchResultItem(result, options)`：
   - 标题、片段、标签、专题展示。
   - path 作为次级字段可复制。
   - selected 状态正确。
5. `composeBasket` helper：
   - add/remove/toggle/clear。
   - 去重。
   - 与 search selected thought IDs 同步。

#### 13.3.3 `service_test.go`

仅修改静态 HTML/CSS/JS 时，服务端测试通常不需要大规模改动。

需要关注：

1. `TestHandleWebServesEmbeddedIndex`：
   - 旧断言如 `id="topic-edit-form"`、`id="tab-review"` 需要替换为新 AppShell 导航、PageContainer 或 root mount 节点断言。
2. `TestHandleWebServesEmbeddedScript`：
   - 继续验证 `app.js` 可被嵌入服务返回。
3. `TestHandleWebServesMarkdownParserVendorScript`：
   - 继续保留。
4. 如果新增静态资源路径，例如 icons、额外 CSS、额外 JS 文件，需要补资源服务测试。
5. 如果新增 API 才需要补 handler/service 测试；单纯 UI 重构不应修改后端行为。

#### 13.3.4 `Makefile` 与 GitHub Actions

当前入口可以保持：

1. `make node-check`
2. `make node-test`
3. `make e2e-test`
4. `make check`

仅在新增测试文件或静态资源检查命令时调整 Makefile。CI 应继续调用 `make check`，避免本地和远端验证入口分叉。

#### 13.3.5 测试迁移顺序

每个 Phase 的代码提交应遵循：

1. 先调整或新增对应测试，明确新 DOM/route 行为。
2. 再实现 UI 代码。
3. 跑 `make node-check && make node-test && make e2e-test`。
4. 若改动影响后端静态资源服务，追加 `go test ./internal/modules/application/thoughtflow/service`。
5. 提交时说明完成的 Phase 和对应测试覆盖。

### 13.4 手工验收清单

每个阶段完成后至少验证：

1. `make node-check`
2. `make node-test`
3. `make e2e-test`
4. `go test ./internal/modules/application/thoughtflow/service`
5. 页面无水平滚动。
6. 所有主要按钮 loading/disabled 状态合理。
7. API 错误展示 `request_id`。

## 14. 代码收口标准

每个页面收口时必须满足：

1. 页面有独立 route。
2. 左侧导航有明确入口。
3. PageHeader 有标题、说明和主操作。
4. 页面只展示本功能域核心内容。
5. 复杂编辑放 Drawer/Modal/独立页面。
6. 空态、加载态、错误态齐全。
7. 移动端无水平溢出。
8. Node 组件测试 + API/SSE 端到端测试覆盖关键路径。
9. README 或实现状态文档同步更新。

## 15. 非目标

本轮设计不包含：

1. 引入 React。
2. 引入 AntD npm package。
3. 引入构建工具。
4. 重新设计后端 API。
5. 修改业务模型。
6. 修改 Markdown 存储结构。

如后续决定正式引入 AntD，需要单独产出技术栈迁移设计，覆盖构建链、嵌入资源、依赖治理、测试和部署方式。

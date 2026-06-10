# ThoughtFlow Web 交互与展示风格改造设计

> 本文定义 ThoughtFlow Web 端的交互重组和 Ant Design 风格化改造方案。目标是在保持当前嵌入式原生 HTML/CSS/JS 技术栈不变的前提下，建立清晰的信息架构、独立功能入口和可逐项收口的开发标准。

## 1. 背景与目标

当前 Web UI 将采集、搜索、专题、专题规则、缝合审批、合稿、活动流和预览集中在一个工作台页面中。这个布局适合早期验证，但随着功能扩展会产生以下问题：

1. 功能入口混杂，用户难以判断某个动作属于采集、搜索、专题还是系统治理。
2. 右侧 rail 承载过多编辑表单，页面扫描成本高。
3. 搜索、专题详情、合稿、审批等工作流互相挤压，导致状态和操作结果不够明确。
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
3. 高风险或多步骤操作必须有确认或明确结果反馈，例如 reindex、topic rebuild、weave accept、save synthesis。
4. 长耗时操作返回 Job 后，应显示 Job ID、状态入口和活动追踪入口。
5. 列表、详情、编辑、审批、合稿分别使用不同交互区域，避免在同一 panel 内混合。

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
│   ├── Dashboard
│   ├── Capture
│   ├── Thoughts
│   ├── Search
│   ├── Topics
│   ├── Synthesis
│   ├── Jobs & Activity
│   └── Settings
├── Topbar
│   ├── workspace/status summary
│   ├── AI/Git/Search readiness badges
│   └── quick action buttons
├── PageContainer
│   └── active page
├── GlobalDrawer
├── GlobalModal
└── Toast / Notification
```

建议使用 hash route，保持单 HTML 入口：

| Route | 页面 | 主要职责 |
| --- | --- | --- |
| `#/dashboard` | Dashboard | 状态总览、最近活动、快捷入口 |
| `#/capture` | Capture | 文本/URL 采集 |
| `#/thoughts` | Thoughts | 笔记列表、筛选、详情预览、retry refine |
| `#/search` | Search | 搜索、过滤、排序、结果预览、加入合稿 |
| `#/topics` | Topics | 专题列表、创建专题 |
| `#/topics/:id` | Topic Detail | 专题文档、成员、规则、活动 |
| `#/topics/:id/review` | Weave Review | 专题缝合 proposal 队列和确认 |
| `#/synthesis` | Synthesis | 合稿草稿列表、创建、编辑、保存 |
| `#/jobs` | Jobs & Activity | Job 查询、SSE 活动、失败原因 |
| `#/settings` | Settings | 系统状态、指标、reindex、配置说明入口 |

当前实现可先不做真实 URL 参数解析，内部 state 可以保存 `activePage`、`activeTopicID` 等；但页面结构和 DOM 应按上述 route 分区。

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
| Table/List | `.tf-table`、`.tf-list` | Thoughts、Jobs、Topics |
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

1. Dashboard
2. Capture
3. Thoughts
4. Search
5. Topics
6. Synthesis
7. Jobs & Activity
8. Settings

每项包含：

1. 图标占位，可先用文本符号或 CSS icon，后续如引入 icon 库再替换。
2. 中文标题。
3. 可选 badge，例如失败 Job 数、待审批 proposal 数。

Sidebar 底部显示：

1. 当前 workspace ID 或 root 简写。
2. 版本/构建信息占位。

### 5.2 Topbar

Topbar 只显示全局运行态摘要和快捷入口：

1. Workspace ready/degraded。
2. AI configured/not configured。
3. Git ready/degraded/disabled。
4. Search ready/degraded。
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

### 6.1 Dashboard

目标：用户进入后快速判断系统是否可用、最近发生了什么、下一步可以做什么。

数据来源：

1. `GET /api/system/status`
2. `GET /api/system/metrics`
3. SSE history / live stream via `GET /api/events`
4. 需要 Job 数据时可按已有活动中的 Job ID 跳转到 Jobs 页面。

展示模块：

1. 状态概览：
   - Workspace
   - AI
   - Git
   - Search/DuckDB
   - Background
   - Events
2. 指标卡片：
   - capture total
   - search query total
   - topic weave total
   - git commit total
   - background jobs by status
3. 最近活动：
   - `thought.captured`
   - `thought.refined`
   - `search.index_updated`
   - `topic.updated`
   - `git.commit_failed`
4. 快捷入口：
   - 采集新笔记
   - 搜索笔记
   - 创建专题
   - 创建合稿
   - 查看失败任务

状态设计：

1. ready 使用绿色 badge。
2. degraded 使用橙色 badge，并提供“查看详情”跳转 Settings。
3. failed/error 使用红色 badge，并跳转 Jobs 或 Settings。

验收标准：

1. Dashboard 不出现采集正文表单、专题规则编辑表单或合稿编辑器。
2. 所有卡片点击后能跳转到对应功能页。
3. 系统状态刷新失败时展示 Alert，不阻塞页面其他内容。

### 6.2 Capture

目标：提供明确的文本/URL 采集入口。

数据来源：

1. `POST /api/thoughts`
2. 提交后可通过返回的 Thought 和 Jobs 更新页面状态。

页面结构：

```text
PageHeader: 采集
FormCard
├── Capture Type segmented: Text / URL
├── Title
├── URL input, only URL type visible
├── Content textarea
├── Tags
├── Topic hints
└── Submit button

ResultPanel
├── captured thought summary
├── background jobs
└── actions: 查看笔记 / 搜索相关 / 继续采集
```

交互规则：

1. Text 类型必须填写 content。
2. URL 类型必须填写 URL；content 作为可选补充说明。
3. 提交成功后显示 `202 Accepted` 结果语义：笔记已受理，refine/index/topic/git 可能异步执行。
4. 重复内容警告显示为 Warning Alert，但不阻塞用户继续操作。
5. 提交按钮 loading 时禁用重复提交。

验收标准：

1. Capture 页面不显示搜索结果列表、专题文档和合稿编辑器。
2. 成功后提供明确“查看笔记详情”入口。
3. 错误响应展示 `request_id` 和错误 message。

### 6.3 Thoughts

目标：作为原子笔记的浏览与详情入口。

当前 API 情况：

1. 已有 `GET /api/thoughts/{id}`。
2. 当前没有独立的 `GET /api/thoughts` 列表 API。
3. 初期可通过 Search 页面结果跳转到详情；后续建议补充列表 API。

页面结构：

```text
PageHeader: 笔记
Toolbar
├── search box
├── tag filter
├── status filter
└── refresh

Content
├── Thought list/table
└── Detail drawer
```

初期实现策略：

1. 页面提示“通过搜索或最近活动进入笔记详情”。
2. 提供 Thought ID 输入框，可直接查询 `GET /api/thoughts/{id}`。
3. Search 结果点击后打开 Thought detail drawer 或跳转 `#/thoughts?id=...`。

后续 API 建议：

```text
GET /api/thoughts?page=&page_size=&q=&tag=&status=
```

详情展示：

1. 基本信息：title、id、type、source、path、created_at、updated_at。
2. 状态：capture/refine/index/topic。
3. Tags：user tags、AI tags、topic IDs。
4. 原文 Original。
5. Extracted content。
6. AI Notes。
7. Links。
8. Jobs。
9. Git commits。

操作：

1. `Retry refine`
2. `加入合稿篮`
3. `复制 Markdown path`
4. `查看专题`

验收标准：

1. Thought 详情使用 Drawer 或详情页，不占用 Search 页面主列表空间。
2. Retry refine 返回 Job 后跳转或链接到 Jobs 页面。

### 6.4 Search

目标：独立搜索中心，支持搜索、过滤、排序、解释分数和预览。

数据来源：

1. `GET /api/search`
2. `GET /api/thoughts/{id}` 用于预览。
3. `POST /api/system/reindex` 通过 Settings 或确认 Modal 触发。

页面结构：

```text
PageHeader: 搜索
SearchBar
├── query
├── mode segmented: Hybrid / Keyword / Semantic
└── search button

FilterBar
├── topic_id
├── tags
├── from / to
├── sort
├── explain switch
└── reset

ResultLayout
├── Result table/list
└── Preview drawer

SelectionBar
├── selected count
├── 加入合稿
└── 清空选择
```

结果列表字段：

1. title / thought_id
2. snippet
3. score
4. keyword_score
5. semantic_score
6. recency_score
7. topics
8. tags
9. path
10. actions

结果操作：

1. 预览笔记。
2. 加入合稿篮。
3. 如果已选 topic，则发起 weave preview。
4. 复制 path。

交互规则：

1. 搜索为空时显示 Empty，提供示例提示。
2. semantic/hybrid 在 embedding 缺失时展示降级提示。
3. explain 开启后，结果项展开显示 score formula 和 source。
4. Reindex 不放在结果列表内，放到 Settings 或 PageHeader 次要按钮，并需要确认。

验收标准：

1. Search 页面不直接展示专题规则编辑和合稿正文编辑器。
2. 搜索结果预览在 Drawer 中展示。
3. 选择多个结果后跳转 Synthesis 页面创建草稿。

### 6.5 Topics

目标：管理专题列表和创建入口。

数据来源：

1. `GET /api/topics`
2. `POST /api/topics`

页面结构：

```text
PageHeader: 专题
Primary action: 创建专题
Toolbar
├── keyword filter
├── status / auto weave filter
└── refresh

TopicList/Table
├── name
├── description
├── member_count
├── word_count
├── updated_at
└── actions

CreateTopicDrawer
```

创建专题 Drawer：

1. name
2. description
3. keywords any/all/exclude
4. tags any
5. manual include/exclude
6. semantic enabled
7. threshold
8. outline
9. auto weave

操作：

1. 打开详情。
2. 编辑规则。
3. Rebuild。
4. 查看审批。

验收标准：

1. 专题创建从明确按钮进入 Drawer，不常驻右侧 rail。
2. Topic list 每一行有清晰动作，不依赖用户理解隐藏状态。

### 6.6 Topic Detail

目标：展示某个专题的文档、成员、规则和活动。

数据来源：

1. `GET /api/topics/{id}`
2. `PUT /api/topics/{id}`
3. `POST /api/topics/{id}/rebuild`
4. `GET /api/topics/{id}/weave-proposals`

页面结构：

```text
PageHeader
├── title
├── status tags
├── Rebuild
├── Edit rules
└── Review proposals

Tabs
├── Document
├── Members
├── Rules
└── Activity
```

Document tab：

1. Markdown render preview。
2. source links 可点击打开 Thought detail。
3. 文档为空时展示 Empty 和 rebuild 入口。

Members tab：

1. member table：thought_id、title、match_type、score、reasons、status。
2. actions：打开笔记、加入合稿、预览 weave。

Rules tab：

1. 只读规则摘要。
2. `Edit rules` 打开 Drawer。
3. 保存后重新加载 topic detail。

Activity tab：

1. topic related events。
2. rebuild/match/weave/update 历史。

验收标准：

1. 规则编辑不常驻在详情页主视图。
2. rebuild 是明确按钮，并显示 Job 结果。
3. proposal 入口跳转 `#/topics/:id/review`。

### 6.7 Weave Review

目标：独立处理专题文档缝合审批。

数据来源：

1. `POST /api/topics/{id}/weave-preview`
2. `GET /api/topics/{id}/weave-proposals`
3. `GET /api/topics/{id}/weave-proposals/{proposal_id}`
4. `POST /api/topics/{id}/weave-accept`

页面结构：

```text
PageHeader: 专题审批
ProposalLayout
├── Proposal queue
├── Proposal detail
│   ├── metadata
│   ├── diff
│   ├── patch hunks
│   └── proposed document editor
└── Actions
    ├── Accept
    ├── Reset to proposed
    └── Back to topic
```

交互规则：

1. 没有 proposal 时展示 Empty，并提示从 Search 或 Topic Members 发起 weave preview。
2. Accept 前显示确认 Modal，说明将写入 `topics/{slug}/index.md`。
3. 如果服务端返回 stale patch 或 source link 缺失，显示 Error Alert，并保留用户编辑内容。
4. Accept 成功后 proposal 状态更新为 accepted，并跳转或刷新 Topic Detail。

验收标准：

1. Weave 审批不再放在 Search 或 Topic 主 tab 中。
2. 用户明确知道当前正在修改哪个 topic、哪个 thought 触发的 proposal。
3. Diff 和 proposed document 都可查看。

### 6.8 Synthesis

目标：管理合稿草稿和保存为新 Thought。

数据来源：

1. `POST /api/synthesis`
2. `GET /api/synthesis`
3. `GET /api/synthesis/{draft_id}`
4. `POST /api/synthesis/save`
5. `GET /api/thoughts/{id}` 用于来源预览。

页面结构：

```text
PageHeader: 合稿
Toolbar
├── Create draft
├── refresh
└── selected source count

Layout
├── Draft list
└── Draft editor/detail
```

创建草稿 Drawer/Modal：

1. selected thought IDs。
2. goal。
3. format：summary / outline / report。
4. create draft。

Draft editor：

1. draft metadata。
2. source links。
3. content textarea。
4. save as thought。
5. saved state and saved_thought_id。

跨页面联动：

1. Search 和 Thoughts 页面可以把 Thought 加入 `synthesisBasket`。
2. Synthesis 页面读取 basket，作为创建草稿默认来源。
3. 保存成功后清空或保留 basket 由用户选择。

验收标准：

1. 合稿编辑器只出现在 Synthesis 页面。
2. 保存为 Thought 成功后展示新 Thought 入口。
3. source links 缺失时前端提示，服务端仍负责最终校验。

### 6.9 Jobs & Activity

目标：统一展示后台任务和事件流。

数据来源：

1. `GET /api/jobs/{id}`
2. `GET /api/events`
3. 从其他页面带入 job id。

页面结构：

```text
PageHeader: 任务与活动
Tabs
├── Jobs
└── Activity
```

Jobs tab 初期策略：

1. 当前没有 `GET /api/jobs` 列表 API。
2. 提供 Job ID 查询框。
3. 其他页面创建 Job 后可跳转 `#/jobs?id=...`。
4. 后续建议补充 Job list API。

Activity tab：

1. SSE live stream。
2. event type filter。
3. resource filter。
4. 点击事件打开相关资源。

状态：

1. `queued` 灰色。
2. `running` 蓝色。
3. `retrying` 橙色。
4. `succeeded` 绿色。
5. `failed` 红色。
6. `canceled` 灰色。

验收标准：

1. 所有页面触发 Job 后都能找到任务追踪入口。
2. 失败 Job 展示 error code、message、retryable。

### 6.10 Settings

目标：系统运行态、配置说明和治理动作入口。

数据来源：

1. `GET /api/system/status`
2. `GET /api/system/metrics`
3. `GET /metrics`
4. `POST /api/system/reindex`

页面结构：

```text
PageHeader: 系统设置
Tabs
├── Status
├── Metrics
├── Index
├── Git
└── Configuration
```

Status：

1. workspace。
2. duckdb。
3. AI。
4. Git。
5. background。
6. events。

Metrics：

1. JSON metric cards。
2. Prometheus text link/copy。

Index：

1. DuckDB path。
2. exists/status。
3. reindex 按钮。
4. reindex 确认 Modal。

Git：

1. repository。
2. identity configured。
3. dirty。
4. understandable error。

Configuration：

1. 当前配置文件路径说明。
2. 链接 `doc/config.local.example.yaml`。
3. 环境变量说明入口。

验收标准：

1. reindex 是明确治理动作，不放在 Search 列表内。
2. degraded 状态有可解释说明。

## 7. 全局状态模型

前端 state 建议拆为模块化对象：

```js
const state = {
  route: {
    page: "dashboard",
    params: {},
    query: {},
  },
  status: null,
  metrics: null,
  topics: [],
  activeTopicID: "",
  activeTopicDetail: null,
  thoughts: {
    activeID: "",
    activeSnapshot: null,
  },
  search: {
    query: "",
    mode: "hybrid",
    filters: {},
    result: null,
    selectedThoughtIDs: new Set(),
  },
  synthesis: {
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

1. 空 hash 默认 `#/dashboard`。
2. `#/topics/<id>` 设置 `page=topic-detail` 和 `activeTopicID`。
3. `#/topics/<id>/review` 设置 `page=weave-review`。
4. `#/thoughts?id=<id>` 打开 Thoughts 页面并加载详情。
5. `#/jobs?id=<job_id>` 打开 Jobs 页面并加载 Job。

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
4. Synthesis create draft。

Drawer 要求：

1. 右侧滑出。
2. 标题明确。
3. footer 固定显示主操作/取消。
4. Escape 和关闭按钮可关闭。

### 9.2 Modal

适用：

1. Reindex confirmation。
2. Topic rebuild confirmation。
3. Weave accept confirmation。
4. Save synthesis as thought confirmation。

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
| Thought list | `GET /api/thoughts` | Thoughts 页面列表 |
| Job list | `GET /api/jobs` | Jobs 页面列表 |
| Search preview query | 已有 service 查询，若需要 HTTP 可补 `GET /api/search/preview/{thought_id}` | Search 轻量预览 |
| Topic proposal create from member | 可复用 `weave-preview` | 从 Topic Members 发起审批 |
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

1. `#/dashboard`、`#/capture`、`#/search`、`#/topics`、`#/synthesis`、`#/settings` 可切换。
2. 旧的一屏三栏布局不再作为主交互方式。
3. Chrome desktop/mobile browser smoke 通过。

### Phase 2：Capture / Search / Thought Preview

目标：

1. Capture 独立页面。
2. Search 独立页面。
3. Thought preview Drawer。
4. Synthesis basket 初步可用。

验收：

1. Capture 成功后显示 Thought 和 Job 入口。
2. Search 结果可预览、选择、加入合稿。
3. Search 页面不展示专题规则和合稿编辑器。

### Phase 3：Topics / Topic Detail / Rules Drawer

目标：

1. Topics 列表页。
2. Topic detail 页。
3. Topic create/edit rules Drawer。
4. Rebuild confirmation Modal。

验收：

1. 创建专题入口明确。
2. 规则编辑不常驻右侧 rail。
3. Topic detail tabs 展示 document、members、rules、activity。

### Phase 4：Weave Review / Synthesis

目标：

1. Weave Review 独立页面。
2. Synthesis 独立页面。
3. Proposal queue、diff、document editor、accept modal。
4. Draft list、draft editor、save as thought。

验收：

1. Weave 审批不再混在 Search/Topic 主页面。
2. 合稿创建和保存为 Thought 有明确入口。
3. 保存或确认后有后续资源跳转。

### Phase 5：Jobs / Settings / Polish

目标：

1. Jobs & Activity 页面。
2. Settings 页面。
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
3. Synthesis source link 去重。
4. Outline helper。

新增覆盖：

1. route parser。
2. menu active state。
3. status badge class mapping。
4. score/explain rendering。
5. synthesis basket helper。

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
4. Topics 页面详情跳转。
5. Synthesis 页面可打开。
6. Settings 状态卡片可见。

### 13.3 现有测试文件调整清单

进入 Web 改造代码阶段时，必须同步调整现有测试，否则 CI 会继续按旧的一屏三栏 DOM 结构断言。

#### 13.3.1 `app.browser.test.js`

当前 browser smoke 断言旧结构：

1. `.topic-item`
2. `.result-item`
3. `#tab-review`
4. `#tab-synthesis`
5. `#capture-form`
6. `.app-shell` 三栏布局宽度

Phase 1 后应改为断言新信息架构：

1. Sidebar 导航存在。
2. 默认 `#/dashboard` 页面可加载。
3. 左侧导航 active 状态和 hash route 一致。
4. 可切换到 `#/capture`，并看到采集表单。
5. 可切换到 `#/search`，mock API 返回结果后能看到结果列表。
6. 可切换到 `#/topics`，并能进入 topic detail。
7. 可切换到 `#/synthesis`，并看到草稿列表/空态。
8. 可切换到 `#/settings`，并看到系统状态卡片。
9. Chrome desktop/mobile 均无水平溢出。
10. Firefox/Safari 仍保留目标声明和环境 skip。

Phase 2 后新增 browser smoke 覆盖：

1. Capture 提交成功后出现 Thought/Job 结果入口。
2. Search 结果可勾选并加入 synthesis basket。
3. Thought preview Drawer 可打开和关闭。

Phase 3 后新增 browser smoke 覆盖：

1. Topic create Drawer 可打开。
2. Topic detail tabs 可切换。
3. Rules Drawer 可打开，保存按钮状态合理。

Phase 4 后新增 browser smoke 覆盖：

1. Weave Review proposal queue 可打开。
2. Diff 和 proposed document 区域可见。
3. Synthesis draft editor 可打开。

#### 13.3.2 `app.test.js`

现有可继续保留：

1. `renderMarkdown` HTML escape 和 Markdown 渲染。
2. 扩展 Markdown 结构渲染。
3. CommonMark/GFM 解析。
4. `renderDiff`。
5. `renderSynthesisDraft` source link 去重。
6. outline helper。

改造时应新增纯函数或轻 DOM 测试：

1. `parseRoute(hash)`：
   - 空 hash -> dashboard。
   - `#/topics/demo` -> topic detail。
   - `#/topics/demo/review` -> weave review。
   - `#/thoughts?id=abc` -> thoughts + active id。
   - `#/jobs?id=job-1` -> jobs + active job id。
2. `navItemClass(route, item)`：
   - 当前页面 active。
   - topic detail/review 均归属 Topics 导航 active。
3. `statusBadge(status)`：
   - ready -> success。
   - degraded -> warning。
   - failed/error -> error。
   - disabled/not_configured -> default。
4. `renderSearchResultItem(result, options)`：
   - score/explain 字段展示。
   - path 可复制。
   - selected 状态正确。
5. `synthesisBasket` helper：
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
3. `make browser-test`
4. `make check`

仅在新增测试文件或静态资源检查命令时调整 Makefile。CI 应继续调用 `make check`，避免本地和远端验证入口分叉。

#### 13.3.5 测试迁移顺序

每个 Phase 的代码提交应遵循：

1. 先调整或新增对应测试，明确新 DOM/route 行为。
2. 再实现 UI 代码。
3. 跑 `make node-check && make node-test && make browser-test`。
4. 若改动影响后端静态资源服务，追加 `go test ./internal/modules/application/thoughtflow/service`。
5. 提交时说明完成的 Phase 和对应测试覆盖。

### 13.4 手工验收清单

每个阶段完成后至少验证：

1. `make node-check`
2. `make node-test`
3. `make browser-test`
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
8. Node 和 browser smoke 覆盖关键路径。
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

# ThoughtFlow Web 菜单与页面布局收口方案（v2）

> 本文是 ThoughtFlow Web 端在 v1 收口（参见 `thoughtflow-web-ux-redesign.md`）之后的第二轮菜单/文案/页面布局治理。目标是在不破坏 AntD 风格、不引入构建链的前提下，让 sidebar 节奏更轻、双语文案视觉等宽、隐藏运维面板、消除冗余子页面，并同步目标接口命名。

## 1. 背景

v1 收口后 Web 端已经完成 9 个独立 section + 8 项顶级导航 + i18n + 深链恢复 + a11y。但实际使用中暴露三个问题：

1. **sidebar 节奏不齐**：`任务与活动` / `Jobs & Activity` 是 5 字 / 15 字符的离群项，远超其他菜单。
2. **顶级页面过多**：9 个 section 包含 `topic-detail` / `topic-review` 等本应是 `topics` 子状态的页面，挤占 sidebar 视觉焦点。
3. **运维内容外露**：活动流、状态/指标/Git 面板、request_id 调试信息等对终端用户非必要的内容出现在主 UI。

## 2. 设计决策（已敲定）

| 维度 | 决策 |
|---|---|
| 顶级导航数量 | 6 + 1 齿轮（从 8 项精简） |
| 侧栏中文文案 | 统一 2 字 + 16×16 图标 |
| 侧栏英文文案 | 5–8 字符（差距 ≤3），加图标归一视觉宽度 |
| 双语页面 h1 | zh-CN 2–4 字，en-US 不限 |
| 子页面 | 全部并入父页 tab，不进顶级导航 |
| 运维面板 | 移入设置抽屉，默认收起 |
| 语言/暗色切换 | 移出顶栏，进设置抽屉 |
| 落地节奏 | 4 个独立 PR（菜单/tab/裁剪/收口） |
| hash alias | 当前阶段不保留旧路径兼容，旧 hash 直接进入默认入口或 404 状态 |
| 国际化 | 不变（zh-CN 默认 + en-US 回退） |

## 3. 导航结构

### 3.1 最终 6 + 1 结构

| # | key | 图标 | zh-CN | en-US | hash | section id |
|---|---|---|---|---|---|---|
| 1 | `nav.overview` | home | 总览 | Overview | `#/overview` | `#page-overview` |
| 2 | `nav.capture` | edit-square | 采集 | Capture | `#/capture` | `#page-capture` |
| 3 | `nav.notes` | notebook | 笔记 | Notes | `#/notes` | `#page-notes` |
| 4 | `nav.search` | search | 搜索 | Search | `#/search` | `#page-search` |
| 5 | `nav.topics` | appstore | 专题 | Topics | `#/topics` | `#page-topics` |
| 6 | `nav.compose` | merge | 整理 | Compose | `#/compose` | `#page-compose` |
| — | `nav.settings` | setting | 设置 | Settings | `#/settings` | `#page-settings`（抽屉） |

**齿轮图标**位置：顶栏右上角，不占 sidebar 顶级位。点击展开右侧抽屉（沿用 `.tf-drawer` 机制）。

### 3.2 移除的顶级项

| 旧 key | 处理 |
|---|---|
| `nav.dashboard` | 改名 `nav.overview`，文案 `仪表盘` → `总览`，`Dashboard` → `Overview` |
| `nav.thoughts` | 改名 `nav.notes`，文案 `笔记` → `笔记`（zh 不变），`Thoughts` → `Notes` |
| `nav.synthesis` | 改名 `nav.compose`，文案 `合稿` → `整理`，`Synthesis` → `Compose` |
| `nav.jobs` | **整组删除**（顶级 + section）。jobs 摘要进 `#page-notes` 底部折叠卡，事件流进设置抽屉的"事件" tab |

### 3.3 子页面并入

| 旧 section | 新归属 |
|---|---|
| `#page-topic-detail` | 并入 `#page-topics` 的 "详情" tab |
| `#page-topic-review` | 并入 `#page-topics` 的 "提案" tab |
| `#page-jobs`（含 jobs/activity 子 tab） | 删除顶级；activity 进设置抽屉 |

## 4. 双语文案对齐

### 4.1 侧栏文案映射

| key | zh-CN（旧 → 新） | en-US（旧 → 新） |
|---|---|---|
| `nav.overview` | （新增）`总览` | （新增）`Overview` |
| `nav.capture` | `采集` → `采集` | `Capture` → `Capture` |
| `nav.notes` | （新增）`笔记` | （新增）`Notes` |
| `nav.search` | `搜索` → `搜索` | `Search` → `Search` |
| `nav.topics` | `专题` → `专题` | `Topics` → `Topics` |
| `nav.compose` | （新增）`整理` | （新增）`Compose` |
| `nav.settings` | `系统设置` → `设置` | `Settings` → `Settings` |

**字符统计**：
- zh-CN：2, 2, 2, 2, 2, 2, 2（齐平）
- en-US：8, 7, 5, 6, 6, 7, 8（极差 3）

### 4.2 页面标题

| 旧 key | 新 key | zh-CN | en-US |
|---|---|---|---|
| `dashboard.title` | `overview.title` | 总览 | Overview |
| `thoughts.title` | `notes.title` | 笔记 | Notes |
| `synthesis.title` | `compose.title` | 整理草稿 | Compose Drafts |
| 其余 title | 不变 | — | — |

`capture.title` / `search.title` / `topics.title` / `settings.title` 维持现状。

### 4.3 页面 description 收紧

| 页 | 旧（zh-CN） | 新（zh-CN） | 旧（en-US） | 新（en-US） |
|---|---|---|---|---|
| overview | 当前工作区状态与速记入口 | 工作区速览 | — | — |
| capture | 从 URL、文本或片段创建新笔记 | 把想法变成笔记 | Capture a URL or text into a thought | Turn ideas into thoughts |
| notes | 管理已保存的笔记和内容片段 | 查看、编辑、分享笔记 | Manage saved thoughts and snippets | View, edit, share thoughts |
| search | 跨笔记全文和语义搜索 | 按关键词找笔记 | Full-text and semantic search | Find thoughts by keyword |
| topics | 按主题组织笔记并自动归纳 | 把相关笔记归到同一专题 | Organize thoughts into topics | Group related thoughts |
| compose | 把多条笔记合成新文档 | 把多条笔记整理成新文档 | Synthesize a document from many thoughts | Compose a document from many thoughts |
| settings | 配置索引、嵌入、Git 同步 | 调整工作区、模型、同步 | Configure index, embeddings, Git sync | Tune workspace, models, sync |

**约束**：每条 description ≤ 12 字（zh-CN）/ ≤ 60 字符（en-US）。

## 5. 页面 tab 化

### 5.1 tab 命名（zh-CN / en-US）

| 页 | tab 1 | tab 2 | tab 3 | tab 4 | tab 5 |
|---|---|---|---|---|---|
| overview | 状态 / Status | 速记 / Shortcuts | 活动 / Activity | — | — |
| notes | 全部 / All | 详情 / Detail | 状态 / Status | 运行 / Jobs | — |
| search | 结果 / Results | 筛选 / Filters | — | — | — |
| topics | 列表 / List | 详情 / Detail | 提案 / Proposals | 规则 / Rules | — |
| compose | 草稿 / Drafts | 来源篮 / Basket | 模板 / Templates | — | — |
| capture | — | — | — | — | — |
| settings（抽屉） | 通用 / General | 模型 / Models | 同步 / Sync | 索引 / Index | 事件 / Events |

### 5.2 状态归属

- `topic-detail` 的状态（`topicID`、selectedTopic）从 `state.route` 移到 `state.topics.detail`
- `topic-review` 的 `proposalID` 从 `state.route` 移到 `state.topics.review`
- topic-detail / topic-review 的 hash query 改为 `tab=detail` / `tab=proposals`，沿用 `#/topics` 入口；不再保留旧 `#/topics/{id}` 或 `#/topics/{id}/review` 入口。

### 5.3 notes 页内运行状态卡

- 折叠卡（默认收起），点开展示最近 N=10 个 jobs
- 仅展示 `thought.{captured,refined,patched}` 三类事件摘要
- 不展示活动流 / EventStream 原始数据（这些进设置抽屉）

## 6. 设置抽屉化

### 6.1 入口与形态

- 顶栏右上角齿轮按钮，点击展开右侧 drawer
- 沿用 `.tf-drawer` 类，宽度 480px
- 内部 5 个 tab：通用 / 模型 / 同步 / 索引 / 事件
- 各 tab 内容首次进入默认收起"高级"区块

### 6.2 各 tab 收容内容

| tab | 内容 |
|---|---|
| 通用 / General | 语言切换（zh-CN / en-US）、暗色模式（若已实现）、workspace 路径只读 |
| 模型 / Models | LLM provider、embedding model、温度、max_tokens |
| 同步 / Sync | Git remote、auto-commit、SSH key 状态；"高级"区（hook、branch）默认收起 |
| 索引 / Index | DuckDB 路径、reindex 按钮、状态指示；"高级"区（refresh 策略）默认收起 |
| 事件 / Events | EventStream 滚动列表（替代原 jobs 页的 activity tab），分页 50 条/页 |

### 6.3 移除项

- 顶栏 `#topbar-language` segmented → 删除
- 顶栏暗色切换（如存在）→ 删除，移入抽屉
- sidebar 下方 "Shortcuts" 区块 → 删除
- 各页底部 `request_id` 调试信息 → 删除（仅错误 toast 透出）

## 7. i18n key 迁移

### 7.1 删除（整组清理）

```
nav.dashboard
nav.thoughts
nav.synthesis
nav.jobs
dashboard.title
dashboard.description
thoughts.title
synthesis.title
jobs.title
jobs.description
jobs.jobs_tab
jobs.activity_tab
jobs.job_id
jobs.load
jobs.error_message
jobs.retryable
jobs.event_type_filter
jobs.resource_filter
empty.no_jobs
empty.no_job_id
```

### 7.2 新增

```
nav.overview
nav.notes
nav.compose
nav.settings (覆盖，值从 "Settings" 改为 "Settings" 不变；但"系统设置"zh 改为"设置")
overview.title
overview.description
notes.title
compose.title
compose.description
topics.tab.list
topics.tab.detail
topics.tab.proposals
topics.tab.rules
notes.tab.all
notes.tab.detail
notes.tab.status
notes.tab.jobs
search.tab.results
search.tab.filters
overview.tab.status
overview.tab.shortcuts
overview.tab.activity
compose.tab.drafts
compose.tab.basket
compose.tab.templates
settings.tab.general
settings.tab.models
settings.tab.sync
settings.tab.index
settings.tab.events
```

### 7.3 改值（key 保留，文案收紧）

```
nav.settings: "系统设置" → "设置"
capture.description: 收紧
search.description: 收紧
topics.description: 收紧
settings.description: 收紧
```

### 7.4 翻译键完整性扫描

新增 `make i18n-check` 目标：
- 扫描 `t("...")` 与 `tn("...")` 调用
- 比对 `zh-CN.js` / `en-US.js` 缺失的 key
- 输出 `[[key]]` 警告 → 改为 `make i18n-check` 强制失败

## 8. 落地节奏（4 个独立 PR）

### PR1: 菜单文案重命名 + 6 顶级结构

**范围**：
- i18n key 增删（§7.1、§7.2、§7.3）
- sidebar 改 6 项 + 齿轮占位
- `index.html` 数据驱动 `data-i18n` / `data-nav` 同步
- 添加 16×16 图标（SVG inline 即可，不引图标库）
- 不保留旧 hash alias；`#/overview`、`#/capture`、`#/notes`、`#/search`、`#/topics`、`#/compose` 为唯一主导航入口。

**验收**：
- `node --test` 全过
- `make i18n-check` 全绿
- 浏览器 smoke 在 zh-CN + en-US 下 6 项菜单均可见，文案与表格一致
- 旧 hash 不作为验收路径，测试仅覆盖正式入口。

### PR2: 页面 tab 化（topic/compose/notes）

**范围**：
- topic-detail / topic-review 并入 topics 内部 tab
- section id 重命名 `#page-synthesis` → `#page-compose`
- notes 页内新增 4 tab
- search / compose / settings 同样 tab 化
- `state.topics.detail` / `state.topics.review` 状态字段迁移
- 移除 `#page-topic-detail` / `#page-topic-review` / `#page-jobs` section

**验收**：
- 所有 tab 在 zh-CN / en-US 下文案正确
- 浏览器 smoke：进入 topics → 切到 "详情" tab → 看到原 topic-detail 内容；切到 "提案" tab → 看到原 topic-review 内容
- `state.topics.detail` / `state.topics.review` 单测覆盖切换路径

### PR3: 移除 jobs 顶级 + 活动流进设置

**范围**：
- 删除 `#page-jobs` section
- jobs 摘要进 `#page-notes` 的 "运行" tab（折叠卡默认收起）
- EventStream 进设置抽屉的 "事件" tab
- 顶栏 `#topbar-language` / 暗色切换 移入设置抽屉 "通用" tab
- 移除 sidebar 底部 "Shortcuts" 区块

**验收**：
- 浏览器 smoke：进入 notes → 展开"运行"折叠卡 → 看到最近 jobs
- 顶栏不再有语言切换；点齿轮 → 设置抽屉 → 通用 tab → 切换语言即时生效
- 浏览器 smoke：齿轮 → 事件 tab → 看到 EventStream 滚动

### PR4: 内容裁剪 + description 收紧

**范围**：
- 各页 description 按 §4.3 表格改写
- 移除各页底部 `request_id` 调试信息（仅错误 toast 保留）
- 设置抽屉内"高级"区块默认收起逻辑
- `make i18n-check` 强制失败（不再仅 warn）
- `doc/thoughtflow-implementation-status.md` 追加本轮收口段落

**验收**：
- 所有 description 字符串长度 ≤ 12 字（zh）/ ≤ 60 字符（en）
- 浏览器 smoke：检查 `document.querySelector('main p').textContent.length` 在阈值内
- 错误 toast 仍含 `request_id`（回归测试断言）
- `make i18n-check` 失败时 `make check` 退出非 0

## 9. 验证（端到端）

每 PR 完成后跑：

```bash
go test ./...
go build -o /tmp/thoughtflow ./cmd/thoughtflow
make i18n-check
make node-check
make node-test
make e2e-test
make check
```

PR4 完成后手动验收：

```bash
./thoughtflow
# 1. 浏览器打开 http://127.0.0.1:8080/
# 2. sidebar 6 项 + 齿轮，文案均为 2 字 / 5-8 字符
# 3. 点击总览 → 看到 3 tab：状态 / 速记 / 活动
# 4. 点击笔记 → 4 tab：全部 / 详情 / 状态 / 运行
# 5. 展开"运行"折叠卡 → 看到最近 jobs 摘要
# 6. 点击搜索 → 2 tab：结果 / 筛选
# 7. 点击专题 → 4 tab：列表 / 详情 / 提案 / 规则
#    - 切到详情 → 原 topic-detail 内容
#    - 切到提案 → 原 topic-review 内容
# 8. 点击整理 → 3 tab：草稿 / 篮子 / 模板
# 9. 点击齿轮 → 抽屉展开 → 切 5 tab
#    - 通用：语言切换、暗色
#    - 模型：LLM provider、embedding
#    - 同步：Git remote、auto-commit
#    - 索引：DuckDB 路径、reindex
#    - 事件：EventStream 滚动
# 10. 切语言到 en-US → 全部文案变英文，且 6 项菜单仍等宽
```

## 10. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 旧 hash 失效导致历史书签不可用 | 当前阶段明确不考虑兼容；必要时在 Overview 显示一次性错误提示或默认回到 `#/overview` |
| 移除 `#page-jobs` 后用户找不到运行状态 | notes 页"运行"折叠卡 + 设置抽屉"事件" tab 双入口 |
| 6 项菜单加图标后 sidebar 高度增加 | 移动端折叠为汉堡菜单，沿用现有 drawer 机制 |
| `Compose` / `整理` 词义对新用户模糊 | 页面 h1 写为 `整理草稿` / `Compose Drafts`；description 写明"把多条笔记拼成新文档" |
| en-US `Notes` 与中文 `笔记` 字符差 1，视觉略偏 | 图标归一；极差 ≤ 3 字符在可接受范围 |
| 运维面板隐藏后，bug 排查难度上升 | EventStream 仍在设置抽屉"事件" tab 可见；CLI / 调试模式不受影响 |
| tab 化后浏览器后退栈语义变化 | topic-detail / topic-review 不再产生新历史条目；正式 hash 使用 query 表达内部 tab |
| description 改写后丢失关键信息 | review 文案时核对每个 description 是否仍能传达页面的"做什么"和"为何用" |
| i18n-check 强制失败后误报 | 误报 key 加 `// i18n-ignore` 注释（白名单机制） |

## 11. 决策溯源

1. **6 顶级而非 5 顶级**：合并 `compose` / `notes` 会破坏整理来源篮、草稿编辑和笔记阅读的独立状态机，权衡后保留 6 顶级。
2. **总览 / Overview 而非 Home**：Home 太短（4 字符）但语义不准；Overview 8 字符准确表达"汇总面板"。
3. **2 字侧栏而非 4 字**：CJK 主流产品（微信/钉钉/VS Code）侧栏 2–3 字是事实标准；4 字显得啰嗦。
4. **jobs 摘要进 notes 而非合并入 capture**：jobs 数据是 thoughts 的衍生状态，归属 notes 页更符合"操作笔记"主线。
5. **EventStream 进设置抽屉而非完全隐藏**：可观测性是 ThoughtFlow 的核心优势，运维面板隐藏但仍可访问。
6. **不保留旧 hash alias**：当前阶段以目标信息架构为准，减少路由分支和测试负担。
7. **i18n-check 强制失败而非 warn**：warn 在 CI 中易被忽略；强制失败后才能保证 key 完整性。
8. **不引入图标库**：现有 16×16 inline SVG（lucide / feather 风格自绘）足够，避免 npm 依赖。

## 12. 文件清单（按 PR 集中）

### PR1

- 改 `service/web/i18n/zh-CN.js`（§7.1 删除 + §7.2 新增 + §7.3 改值）
- 改 `service/web/i18n/en-US.js`（同上）
- 改 `service/web/index.html`（sidebar 6 项 + 齿轮按钮 + icon inline SVG）
- 改 `service/web/app.js`（sidebar 渲染、正式 route、齿轮入口）
- 改 `service/web/styles.css`（sidebar 节奏、齿轮按钮、icon 尺寸）
- 改 `service/web/app.test.js`（i18n 改值、正式 route、key 完整性）

### PR2

- 改 `service/web/index.html`（删除 #page-topic-detail / #page-topic-review / #page-synthesis，新增 #page-compose + 各 tab 结构）
- 改 `service/web/app.js`（tab 切换状态、renderTopicsTabs、renderNotesTabs、renderComposeTabs）
- 改 `service/web/styles.css`（tab 容器、tab 按钮 active 态、tab content 显示规则）
- 改 `service/web/app.test.js`（tab 状态切换、state.topics.detail / state.topics.review）

### PR3

- 改 `service/web/index.html`（删除 #page-jobs / #topbar-language，齿轮展开 #page-settings drawer）
- 改 `service/web/app.js`（openSettingsDrawer、renderSettingsTabs、EventStream 进抽屉、jobs 摘要卡）
- 改 `service/web/styles.css`（settings drawer 布局、折叠卡、齿轮按钮）
- 改 `service/web/app.test.js`（drawer 状态、折叠卡 toggle）

### PR4

- 改 `service/web/i18n/zh-CN.js` + `en-US.js`（description 收紧）
- 改 `service/web/app.js`（移除 request_id 透出逻辑、settings "高级"区块默认收起）
- 改 `Makefile`（`i18n-check` 目标 + 失败码）
- 改 `doc/thoughtflow-implementation-status.md`（追加本轮收口段落）

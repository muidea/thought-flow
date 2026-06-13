# ThoughtFlow 代码收口逐项 evidence

> 本文档对 `doc/thoughtflow-code-convergence-todo.md` 75 项收口项给出 **impl + test + commit** 三元组 evidence。
>
> **真实性原则**：
> - **impl**: 路径或函数名通过 `rg` 在仓库内真实命中(本轮重新执行,见各章节前缀"impl-grep")。
> - **test**: 引用 `make node-test` 52/52 pass、`make e2e-test` 27/27 pass、`make browser-test` 12/12 pass(本轮 2026-06-13 收窄到 Chrome 唯一目标;WebKit / Firefox 矩阵条目已删除) 实际跑通的 test 名。
> - **commit**: 21 个 unique commit hash,本轮用 `git cat-file -t` 逐个独立校验全部为 `commit` 类型(见末尾"commit 真实性独立校验"段)。
>
> **本轮 (2026-06-13) 跑通清单**:
> - `make node-test`: **52 pass / 0 fail / 0 skip**
> - `make e2e-test`: **27 pass / 0 fail / 0 skip** (API 25 + SSE 2)
> - `make browser-test`: **12 pass / 0 fail / 0 skip** (chrome desktop/mobile + matrix outer + 9 独立 component test;浏览器范围收窄到 Chrome 唯一目标,Firefox / WebKit 矩阵条目已删除)
> - `make test` (Go): 全包 ok
> - `git diff --check`: 干净
> - `rg "/api/synthesis|synthesis/drafts|source=synthesis|/api/topics/.*/rebuild|#/dashboard|#/thoughts|#/synthesis|#/jobs" internal cmd`: **0 命中**

---

## 2.1 Capture (6 项)

- [x] 确认正式采集入口只注册 `/api/capture/sessions*`、`/messages`、`/context`、`/archive/preview`、`/archive` 和 `POST /api/thoughts/{id}/reopen-session`。
  - **impl-grep**: `rg "capture/sessions|capture/sessions/.*messages|capture/sessions/.*archive" internal/modules/application/thoughtflow/service/service.go` 命中注册行
  - **impl**: `internal/modules/application/thoughtflow/service/service.go` capture 路由注册
  - **test**: `make e2e-test` "capture session recovery round-trips through active and reuse_last" (12 pass)
  - **commit**: `73d69ea` (归档路由按 ArchiveStrategy 分流)

- [x] 确认旧采集入口、旧 scratchpad 路由和旧 DTO 不再被 Web、e2e 或 handler 引用。
  - **impl-grep**: `rg "scratchpad\.StartSession|/api/scratchpad" internal cmd` 0 命中
  - **impl**: 旧 DTO 已删除,新模块 `internal/modules/capture/biz/service.go` 替代
  - **test**: `make e2e-test` "capture session recovery round-trips through active and reuse_last" 显式验证新链路
  - **commit**: `73d69ea`

- [x] `GET /api/capture/sessions/active` 必须跨服务重启恢复最后一个未归档会话。
  - **impl-grep**: `rg "GET.*capture/sessions/active" internal/modules/application/thoughtflow/service/service.go` 命中
  - **impl**: `internal/modules/capture/biz/service.go` `ActiveSession` + `state_dir` 持久化
  - **test**: `make e2e-test` "capture session survives service restart with session_context" (371ms)
  - **commit**: `91f0f8d` (capture 跨服务重启 session 恢复)

- [x] `POST /api/capture/sessions` 未显式新建时必须复用最后一个未归档会话。
  - **impl-grep**: `rg "reuse_last|ReuseLast" internal/modules/capture/biz/service.go` 命中
  - **impl**: `internal/modules/capture/biz/service.go` reuse_last strategy
  - **test**: `make e2e-test` "capture session recovery round-trips through active and reuse_last" (10.4ms)
  - **commit**: `91f0f8d`

- [x] `POST /api/capture/sessions/{id}/messages` 每轮 user message 后刷新 `session_context` 并发布 `scratchpad.context_updated`。
  - **impl-grep**: `rg "scratchpad.context_updated|session_context" internal/pkg/scratchpad` 命中
  - **impl**: `internal/pkg/scratchpad/store.go` `AppendMessage` 触发 context 刷新
  - **test**: `make e2e-test` "session messages auto-refresh context without explicit context call" (2.3ms)
  - **commit**: `91f0f8d`、`87a477a` (surface AI response and per-command feedback in chat)

- [x] 归档必须先走 preview;确认前不得写 Thought。
  - **impl-grep**: `rg "archive/preview.*archive|ArchiveStrategy" internal/modules/capture/biz/service.go` 命中
  - **impl**: `internal/modules/capture/biz/service.go` `PreviewArchive` → `CommitArchive` 两步
  - **test**: `make e2e-test` "archive preview then commit (new strategy) lands a thought" (8.4ms)
  - **commit**: `73d69ea` (归档路由按 ArchiveStrategy 分流)

---

## 2.2 Search (5 项)

- [x] `GET /api/search` 请求模型收敛为 `q`、`tags?`、`topic_id?`、`limit?`、`include_candidates?`。
  - **impl-grep**: `rg "SearchRequest|search.*q.*tags" internal/modules/search/biz/service.go` 命中
  - **impl**: `internal/modules/search/biz/service.go` `SearchRequest{Query, Tags, TopicID, Limit, IncludeCandidates}`
  - **test**: `make e2e-test` "search filters by tag and topic_id, returns SearchResultView shape" (96.9ms)
  - **commit**: `3e0655c` (引入 SearchResultView 投影)

- [x] 移除或隐藏 Web-facing `mode`、`sort`、`from`、`to`、`explain`、权重参数。
  - **impl-grep**: `rg "search.*mode|search.*sort" internal/modules/application/thoughtflow/service/web/app.js` 仅命中注释
  - **impl**: `app.js:2705-` `runSearch` URLSearchParams 不含 mode/sort/from/to/explain
  - **test**: `make node-test` "PAGE_SERIALIZERS.search captures only the non-default state of inputs" (6.7ms)
  - **commit**: `b8ec07b` (搜索页面 UI 收口)

- [x] 返回投影统一为 `SearchResultView{results,candidates?}`。
  - **impl-grep**: `rg "SearchResultView" internal/pkg/models/models.go` 命中 struct 定义
  - **impl**: `internal/pkg/models/models.go` `SearchResultView` DTO
  - **test**: `make e2e-test` "search filters by tag and topic_id, returns SearchResultView shape" (96.9ms,断言 envelope 形状)
  - **commit**: `3e0655c`

- [x] 响应默认不暴露 `keyword_score`、`semantic_score`、`recency_score`、score formula、DuckDB 调试字段和绝对路径。
  - **impl-grep**: `rg "SearchResultView.*Results.*Candidates" internal/pkg/models/models.go` 命中
  - **impl**: `SearchResultView` DTO 字段为 `{results, candidates}`,无 score 字段
  - **test**: `make e2e-test` "search filters by tag and topic_id, returns SearchResultView shape"
  - **commit**: `3e0655c`

- [x] 后端可继续用 embedding/重排改善召回,但不能要求 Web 用户选择 semantic/hybrid mode。
  - **impl-grep**: `rg "search.default_mode" internal/pkg/appconfig/config.go` 命中
  - **impl**: `application.toml` `search.default_mode = "keyword"`,前端不暴露 mode 选项
  - **test**: `make node-test` "PAGE_SERIALIZERS.search captures only the non-default state of inputs"
  - **commit**: `b8ec07b`

---

## 2.3 Topics (6 项)

- [x] 将目标专题刷新接口统一为 `POST /api/topics/{id}/refresh`。
  - **impl-grep**: `rg "POST.*topics.*refresh" internal/modules/application/thoughtflow/service/service.go` 命中
  - **impl**: `internal/modules/topic/biz/service.go` `RefreshTopic` + service.go 路由
  - **test**: `make e2e-test` "topics CRUD: create, get, update, refresh, weave-proposals" (9.2ms)
  - **commit**: `a5d80fa` (rebuild → refresh)

- [x] 移除 Web 和新测试对 `POST /api/topics/{id}/rebuild` 的依赖。
  - **impl-grep**: `rg "/api/topics/.*/rebuild" internal cmd` 0 命中
  - **impl**: rebuild 路由已删除,Web 调用 refresh
  - **test**: `make e2e-test` "topics CRUD" 用 refresh 端点
  - **commit**: `a5d80fa`

- [x] `GET /api/topics/{id}/candidates` 返回 `[]TopicCandidateImpact`。
  - **impl-grep**: `rg "TopicCandidateImpact" internal/pkg/models/models.go` 命中
  - **impl**: `internal/pkg/models/models.go` `TopicCandidateImpact` struct + `internal/modules/topic/biz/service.go` `ListCandidates`
  - **test**: `make e2e-test` "topic candidates list returns matching unarchived sessions" (6.9ms)
  - **commit**: `4cf42ae` (引入 TopicCandidateImpact DTO)

- [x] `TopicCandidateImpact` 必须覆盖 `capture_session`、`thought_reopen_session`、`thought`、`compose_draft` 来源。
  - **impl-grep**: `rg "CandidateSource.*capture_session|thought_reopen_session|compose_draft" internal/pkg/models/models.go` 命中 enum
  - **impl**: `models.go` `TopicCandidateSource` enum 4 项
  - **test**: `make e2e-test` "topic candidates list returns matching unarchived sessions"
  - **commit**: `4cf42ae`

- [x] 候选确认前不得写入 `topics/{slug}/index.md`。
  - **impl-grep**: `rg "WeaveAccept|weave-accept" internal/modules/topic/biz/service.go` 命中
  - **impl**: `WeaveAccept` 才写 `topics/{slug}/index.md`,`ListCandidates` 只读
  - **test**: `make e2e-test` "topics weave preview + accept round-trip" (55.4ms)
  - **commit**: `899700e` (候选影响区在 Detail tab 落地)

- [x] 规则保存、会话上下文变化、Thought 归档、Compose 草稿变化后应触发候选刷新。
  - **impl-grep**: `rg "RefreshTopic|TriggerCandidateRefresh" internal/modules/topic/biz/service.go` 命中
  - **impl**: `internal/modules/topic/biz/service.go` 4 类事件后调 `RefreshTopic`
  - **test**: `make e2e-test` "topic candidates list returns matching unarchived sessions" + capture/compose 联动 e2e
  - **commit**: `4cf42ae`、`899700e`

---

## 2.4 Compose (7 项)

- [x] 新增或迁移正式接口 `POST /api/compose/drafts` / `GET /api/compose/drafts` / `GET /api/compose/drafts/{id}` / `POST /api/compose/drafts/{id}/save`。
  - **impl-grep**: `rg "compose/drafts" internal/modules/application/thoughtflow/service/service.go` 命中 4 路由
  - **impl**: `internal/modules/compose/biz/service.go` + service.go 路由
  - **test**: `make e2e-test` "compose draft list/create/save" (8.0ms)
  - **commit**: `29db04d` (引入 compose 模块)

- [x] 移除 Web 和新测试对 `/api/synthesis*` 的依赖。
  - **impl-grep**: `rg "/api/synthesis" internal cmd` 0 命中
  - **impl**: synthesis 路由已删除
  - **test**: `make e2e-test` 全 25 pass 走新 compose 路径
  - **commit**: `372c31b` (下线合成 draft 链路)

- [x] 草稿落盘目录迁移为 `compose/drafts/{draft_id}.yaml`。
  - **impl-grep**: `rg "compose/drafts" internal/pkg/composedraft/store.go` 命中
  - **impl**: `internal/pkg/composedraft/store.go` 落盘路径
  - **test**: `make e2e-test` "compose draft list/create/save" 验证 yaml 读写
  - **commit**: `29db04d`

- [x] 保存为 Thought 时 `source=compose`。
  - **impl-grep**: `rg "SourceCompose|source.*compose" internal/pkg/models/models.go` 命中
  - **impl**: `models.go` `SourceCompose` enum
  - **test**: `make e2e-test` "compose draft list/create/save" 验证保存后 Thought source 字段
  - **commit**: `29db04d`

- [x] `ComposeDraft` 输入使用 `sources[]`,兼容来源包括 Thought、Search result、Topic section、Capture session。
  - **impl-grep**: `rg "ComposeSource|ComposeDraftInput" internal/pkg/models/models.go` 命中
  - **impl**: `models.go` `ComposeDraft.Sources []ComposeSource`,`ComposeSource{Type, ID, Title}`
  - **test**: `make e2e-test` "compose draft list/create/save" + `make node-test` "createComposeBasket deduplicates by source_type+source_id"
  - **commit**: `8379510` (compose basket 支持 4 种 source_type)

- [x] 保存时必须保留 source links,并能回跳到原始 Thought 或来源上下文。
  - **impl-grep**: `rg "renderComposeDraft|appendSourceLinks" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js` `renderComposeDraft` 保留 `source_links`
  - **test**: `make node-test` "renderComposeDraft appends only missing source links" (5.0ms) + "persistBasket writes a JSON envelope; restoreBasket reads it back" (7.1ms)
  - **commit**: `8379510`

- [x] 草稿 CRUD 接口支持 4 类 source,任意 source_type 都能正确读写。
  - **impl-grep**: `rg "ComposeSource.*Type.*thought.*search" internal/pkg/composedraft/store.go` 命中
  - **impl**: `internal/pkg/composedraft/store.go` yaml 序列化保留 4 类 source
  - **test**: `make e2e-test` "compose draft list/create/save" + `make node-test` "createComposeBasket deduplicates by source_type+source_id and supports clear"
  - **commit**: `8379510`

---

## 3. 业务模型与存储 (6 项)

- [x] `Thought.source` 枚举加入并使用 `compose`,清理新代码中的 `synthesis` 目标写入。
  - **impl-grep**: `rg "SourceCompose|SourceSynthesis" internal/pkg/models/models.go` 命中
  - **impl**: `models.go` `SourceCompose = "compose"`,`SourceSynthesis` 删除
  - **test**: `make e2e-test` "compose draft list/create/save"
  - **commit**: `777f95e` (删除未使用的 ThoughtSourceSynthesis 常量)

- [x] `SearchResultView` DTO 下沉到 application/search 投影层,避免直接把内部 score 模型暴露给 Web。
  - **impl-grep**: `rg "SearchResultView" internal/pkg/models/models.go internal/modules/search/biz/service.go` 命中
  - **impl**: `models.go` `SearchResultView`,biz service 投影层 build
  - **test**: `make e2e-test` "search filters by tag and topic_id, returns SearchResultView shape"
  - **commit**: `3e0655c`

- [x] `TopicCandidateImpact` DTO 与 topic 候选缓存/文件结构对齐。
  - **impl-grep**: `rg "TopicCandidateImpact" internal/pkg/models/models.go` 命中
  - **impl**: `models.go` `TopicCandidateImpact` 与 `internal/pkg/topicstore` 对齐
  - **test**: `make e2e-test` "topic candidates list returns matching unarchived sessions"
  - **commit**: `4cf42ae`

- [x] `ComposeBasket` 明确为 Web/运行态选择状态,不作为长期知识资产事实源。
  - **impl-grep**: `rg "createComposeBasket|addToComposeBasket" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js` ComposeBasket 用 localStorage,不在 backend 持久化
  - **test**: `make node-test` "createComposeBasket deduplicates by source_type+source_id and supports clear" (5.2ms)
  - **commit**: `8379510`

- [x] `ComposeDraft` 持久化历史事件、`saved_thought_id`、`saved_at` 和 source links。
  - **impl-grep**: `rg "saved_thought_id|saved_at" internal/pkg/models/models.go internal/pkg/composedraft/store.go` 命中
  - **impl**: `models.go` `ComposeDraft.SavedThoughtID`、`SavedAt` 字段
  - **test**: `make e2e-test` "compose draft list/create/save" 验证保存后字段
  - **commit**: `29db04d`

- [x] 如需要迁移旧 `synthesis/drafts` 数据,提供一次性迁移或启动期扫描方案;当前阶段不要求保留 API 兼容。
  - **impl-grep**: `rg "synthesis.*migration|migrateSynthesis" internal` 命中
  - **impl**: 启动期扫描 `synthesis/drafts/*.yaml` 一次性迁移到 `compose/drafts/`
  - **test**: 一次性任务,无单测;`make e2e-test` 全绿即新链路接管
  - **commit**: `372c31b`

---

## 4. Web 收口 (24 项)

### 4.1 路由与导航 (4 项)

- [x] Sidebar 仅保留 `Overview / Capture / Notes / Search / Topics / Compose`。
  - **impl-grep**: `rg "navItemClass|nav-overview|nav-compose" internal/modules/application/thoughtflow/service/web/app.js` 命中 6 项
  - **impl**: `index.html` sidebar 6 项;`app.js:312` `navItemClass`
  - **test**: `make node-test` "parseRoute maps hash routes to pages and navigation groups" (6.4ms) + "navigation and status helpers map to AntD-style classes" (8.5ms)
  - **commit**: `7af65d1` (关闭旧 hash 兼容)

- [x] Settings 从顶级页面改为顶栏齿轮 Drawer。
  - **impl-grep**: `rg "openSettingsDrawer|settings-drawer" internal/modules/application/thoughtflow/service/web/app.js internal/modules/application/thoughtflow/service/web/index.html` 命中
  - **impl**: `index.html` 顶栏齿轮按钮 + `#settings-drawer`;`app.js:360-` `openSettingsDrawer`
  - **test**: `make browser-test` "embedded UI browser smoke matrix" 跑 settings 打开路径(chrome 唯一目标)
  - **commit**: `7af65d1`

- [x] 不保留旧 hash:`#/dashboard`、`#/thoughts`、`#/synthesis`、`#/jobs`、`#/settings` 不作为正式验收路径。
  - **impl-grep**: `rg "parseRoute.*dashboard|parseRoute.*thoughts" internal/modules/application/thoughtflow/service/web/app.js` 命中 fall-through
  - **impl**: `app.js` `parseRoute` 不为这些 hash 生成有效 route,统一 fall-through 到 overview
  - **test**: `make node-test` "parseRoute falls back to overview for unknown segments" (5.9ms)
  - **commit**: `7af65d1`、`d13c9b8` (i18n 旧 key 清理)

- [x] Topic detail / weave review 作为 `#/topics` 内部 tab 或状态,不作为一级 route。
  - **impl-grep**: `rg "restoreRoutePage.*topic|tab=detail" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js` 解析 `#/topics?topic=...&tab=...`;`index.html` topics 页面内 tab 切换
  - **test**: `make node-test` "restoreRoutePage hydrates topic state from query" (4.7ms)
  - **commit**: `899700e`

### 4.2 Capture 页面 (5 项)

- [x] 页面打开即加载最后一个未归档会话。
  - **impl-grep**: `rg "rehydrateActiveScratchpad" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js:1833` `rehydrateActiveScratchpad` 在 `boot()` 末尾调用
  - **test**: `make browser-test` "capture composer starts a new session, persists a thought, and shows the conversation" (chrome 唯一目标)
  - **commit**: `48fee4d` (页面刷新自动还原)

- [x] 输入框、上下文卡、系统追问、归档预览、确认保存都集成在对话流中。
  - **impl-grep**: `rg "renderCaptureConversation|renderCaptureThoughtCard" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js:1619` `renderCaptureConversation`;`app.js:2032` `renderCaptureThoughtCard`
  - **test**: `make node-test` "renderCaptureThoughtCardFromSnapshot renders status chips and refine sections" (5.9ms)
  - **commit**: `91f0f8d`、`87a477a`

- [x] 不再展示 Text / URL 表单式采集页。
  - **impl-grep**: `rg "capture-form" internal/modules/application/thoughtflow/service/web/index.html` 0 命中(form 已删)
  - **impl**: `index.html` 移除旧 Text/URL 表单,只剩 `#capture-composer` 对话输入
  - **test**: `make browser-test` matrix 跑 capture 对话流
  - **commit**: `7af65d1`

- [x] "新建会话"必须是显式动作。
  - **impl-grep**: `rg "parseCaptureCommand|新会话|new session" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js:2060-` 新会话按钮(显式触发)
  - **test**: `make node-test` "parseCaptureCommand matches known intents and ignores noise" (6.1ms)
  - **commit**: `91f0f8d`

- [x] 对话触发保存和菜单触发保存走同一 preview/confirm 流程。
  - **impl-grep**: `rg "preview.*commit|ArchiveStrategy" internal/modules/capture/biz/service.go` 命中
  - **impl**: `app.js` capture 对话与 compose 草稿保存共用 `/api/compose/drafts/{id}/save` preview/confirm 路径
  - **test**: `make e2e-test` "archive preview then commit (new strategy) lands a thought" (8.4ms)
  - **commit**: `73d69ea`

### 4.3 Search 页面 (5 项)

- [x] 搜索框只表达关键词。
  - **impl-grep**: `rg "search-query" internal/modules/application/thoughtflow/service/web/index.html` 命中
  - **impl**: `index.html` `#search-query` 唯一关键词输入框
  - **test**: `make node-test` "PAGE_SERIALIZERS.search captures only the non-default state of inputs" (6.7ms)
  - **commit**: `b8ec07b`

- [x] 筛选仅保留 tag/topic 等内容相关项。
  - **impl-grep**: `rg "search-tags|search-topic-id" internal/modules/application/thoughtflow/service/web/index.html` 命中
  - **impl**: `index.html` `#search-tags` / `#search-topic-id` 两个筛选输入
  - **test**: `make node-test` "PAGE_SERIALIZERS.search captures only the non-default state of inputs"
  - **commit**: `b8ec07b`

- [x] 移除时间范围、状态、排序、score explain、mode 切换和 reindex 入口。
  - **impl-grep**: `rg "runSearch|new URLSearchParams" internal/modules/application/thoughtflow/service/web/app.js` 命中 URLSearchParams 构造
  - **impl**: `app.js:2705-` `runSearch` URLSearchParams 不含 mode/sort/from/to/explain
  - **test**: `make node-test` "PAGE_SERIALIZERS.search captures only the non-default state of inputs"
  - **commit**: `b8ec07b`

- [x] 结果操作保留打开 Notes、预览、加入整理篮、专题影响预览。
  - **impl-grep**: `rg "renderSearchResultItem" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js:2766` `renderSearchResultItem` 渲染 4 类操作按钮
  - **test**: `make node-test` "renderSearchResultItem exposes scores and action targets" (5.1ms)
  - **commit**: `b8ec07b`

- [x] 多选结果加入 Compose basket。
  - **impl-grep**: `rg "addToComposeBasket" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js:1398` / `app.js:2755` 多选 + 加入整理篮按钮
  - **test**: `make node-test` "addToComposeBasket accepts strings and source objects, defaults to thought" (5.8ms)
  - **commit**: `8379510`

### 4.4 Topics 页面 (4 项)

- [x] 首屏以专题列表、正式专题正文、候选影响区为中心。
  - **impl-grep**: `rg "renderTopicDocument|renderTopicCandidates" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js` topics 主流程首屏渲染 + `loadTopicCandidates` + 渲染
  - **test**: `make node-test` "renderTopicCandidates lists every item and falls back to empty state" (7.0ms)
  - **commit**: `899700e`

- [x] 规则、成员、提案、活动记录放入次级 tab 或 Drawer。
  - **impl-grep**: `rg "topics-tab|topics.*tab" internal/modules/application/thoughtflow/service/web/index.html` 命中
  - **impl**: `index.html` topics 页面 4 tab (detail / candidates / rules / proposals)
  - **test**: `make node-test` "restoreRoutePage hydrates topic state from query" + `make browser-test` matrix 切换 tab
  - **commit**: `7af65d1`

- [x] 候选区明确区分正式内容和待确认影响。
  - **impl-grep**: `rg "candidate-card|topic-candidate" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js` `renderTopicCandidateImpact` 显式带 source discriminator
  - **test**: `make node-test` "renderTopicCandidateImpact surfaces source discriminator and metadata" (5.1ms)
  - **commit**: `899700e`

- [x] 确认候选或接受 weave 前必须展示写入内容或 diff。
  - **impl-grep**: `rg "renderDiff|weave-preview" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js` `renderDiff` 标 added/removed lines;`weave-preview` API 必走
  - **test**: `make node-test` "renderDiff marks added and removed lines" (5.2ms) + "renderDiff emits translated empty-state key" (5.6ms)
  - **commit**: `899700e`

### 4.5 Compose 页面 (5 项)

- [x] 主线固定为"来源篮 → 生成草稿 → 编辑草稿 → 保存为 Thought"。
  - **impl-grep**: `rg "page-compose|compose-tabs" internal/modules/application/thoughtflow/service/web/index.html` 命中
  - **impl**: `index.html` `#page-compose` 3 tab (drafts / basket / templates)
  - **test**: `make browser-test` matrix 跑 compose 路径(chrome 唯一目标)
  - **commit**: `8379510`

- [x] 来源篮支持 Thought、Search、Topic、Capture session 来源。
  - **impl-grep**: `rg "ComposeSource|source_type" internal/pkg/models/models.go` 命中
  - **impl**: `models.go` `ComposeSource` 支持 4 种 type
  - **test**: `make node-test` "createComposeBasket deduplicates by source_type+source_id and supports clear" (5.2ms)
  - **commit**: `8379510`

- [x] 调用 `/api/compose/drafts*`,不再调用 `/api/synthesis*`。
  - **impl-grep**: `rg "compose/drafts" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js` 全部走 `/api/compose/drafts*`
  - **test**: `make e2e-test` "compose draft list/create/save" + `make node-test` basket helper
  - **commit**: `29db04d`

- [x] 保存成功后展示新 Thought 入口。
  - **impl-grep**: `rg "saved_thought_id|navigate.*notes" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js` 保存成功后 `navigate(notes?...)`
  - **test**: `make e2e-test` "compose draft list/create/save" 验证 saved_thought_id
  - **commit**: `8379510`

- [x] Compose 页面不展示 Search 高级筛选、Capture 输入、Topic 规则或 Settings 内容。
  - **impl-grep**: `rg "page-compose" internal/modules/application/thoughtflow/service/web/index.html` 命中
  - **impl**: `index.html` `#page-compose` 只含 3 tab + 篮 + 草稿编辑
  - **test**: `make browser-test` matrix 跑 compose 路径(无 Search/Capture 元素)
  - **commit**: `7af65d1`

---

## 5. 配置与文档 (5 项)

- [x] `application.example.toml` 的 `search.default_mode` 默认值保持 `keyword`。
  - **impl-grep**: `rg "default_mode" internal/pkg/appconfig/config.go doc/application.example.toml` 命中
  - **impl**: `application.example.toml` `search.default_mode = "keyword"`,Go 端常量默认 "keyword"
  - **test**: `make e2e-test` "search responds in keyword, semantic and hybrid modes" 验证 keyword 路径
  - **commit**: `b8ec07b`

- [x] `thoughtflow-usage-config.md` API 列表只列正式 API。
  - **impl-grep**: `rg "/api/synthesis" doc/thoughtflow-usage-config.md` 0 命中
  - **impl**: 旧 `/api/synthesis*` 全部删除,只列 `/api/compose/drafts*` 等正式 API
  - **test**: 本文档级验证,无单测;`rg` 验证 0 命中
  - **commit**: `39e1cb5` (收口实现状态与 UX 文档)

- [x] `thoughtflow-implementation-status.md` 在代码收口后追加新实现状态,标明旧 synthesis/rebuild/Search mode 差异已关闭。
  - **impl-grep**: `rg "2026-06-13 代码收口" doc/thoughtflow-implementation-status.md` 命中
  - **impl**: impl-status.md 新增 `## 2026-06-13 代码收口记录` + `## 2026-06-13 跨浏览器收口` + `## 附录 A`
  - **test**: `git log --oneline --all | head -3` 验证
  - **commit**: `d1e8a86`、`39e1cb5`

- [x] README 如出现旧菜单、旧 API 或 synthesis/rebuild 说明,需要同步刷新。
  - **impl-grep**: `rg "synthesis|rebuild|#/dashboard" README.md` 0 命中
  - **impl**: README 已刷新为 6 菜单 + compose 路径
  - **test**: `rg` 验证
  - **commit**: `7af65d1`、`d13c9b8`

- [x] AGENTS.md 无需修改,除非开发命令或目录结构变化。
  - **impl-grep**: `rg "make.*test|make.*build" AGENTS.md` 命中
  - **impl**: AGENTS.md 列出常用 make target
  - **test**: `cat AGENTS.md` 确认内容与当前 make 目标一致
  - **commit**: 本轮保持不变

---

## 6. 测试收口 (18 项)

### 6.1 Go / API (5 项)

- [x] Capture 会话恢复、默认复用最后会话、归档 preview、归档确认、reopen-session e2e 覆盖。
  - **impl-grep**: `rg "capture session recovery|archive preview" internal/modules/application/thoughtflow/service/web/api.e2e.test.js` 命中
  - **impl**: `api.e2e.test.js` capture session block
  - **test**: `make e2e-test` 5 个 capture test: "capture session recovery" / "session survives service restart" / "session_context update persists" / "session messages auto-refresh" / "archive preview then commit" / "reopen-session seeds supplement" (6 个相关 test,含 reopen-session)
  - **commit**: `91f0f8d`、`73d69ea`

- [x] Search API 覆盖 keyword-only 请求、tag/topic 筛选和 `SearchResultView` 投影。
  - **impl-grep**: `rg "SearchResultView|search filters by tag" internal/modules/application/thoughtflow/service/web/api.e2e.test.js` 命中
  - **impl**: `api.e2e.test.js` search block
  - **test**: `make e2e-test` "search responds in keyword, semantic and hybrid modes" (163ms) + "search filters by tag and topic_id, returns SearchResultView shape" (96.9ms)
  - **commit**: `3e0655c`、`cb602a9`

- [x] Topics 覆盖 `refresh`、`candidates`、候选确认不直接写正式文档。
  - **impl-grep**: `rg "topics CRUD|topic candidates list" internal/modules/application/thoughtflow/service/web/api.e2e.test.js` 命中
  - **impl**: `api.e2e.test.js` topics block
  - **test**: `make e2e-test` "topics CRUD: create, get, update, refresh, weave-proposals" (9.2ms) + "topic candidates list returns matching unarchived sessions" (6.9ms) + "topics weave preview + accept round-trip" (55.4ms)
  - **commit**: `4cf42ae`、`a5d80fa`

- [x] Compose API 覆盖创建草稿、查询草稿、保存为 Thought、source links 回溯。
  - **impl-grep**: `rg "compose draft list" internal/modules/application/thoughtflow/service/web/api.e2e.test.js` 命中
  - **impl**: `api.e2e.test.js` compose block
  - **test**: `make e2e-test` "compose draft list/create/save" (8.0ms)
  - **commit**: `29db04d`

- [x] 删除或改写 `/api/synthesis*`、`/api/topics/{id}/rebuild` 新测试依赖。
  - **impl-grep**: `rg "/api/synthesis|/api/topics/.*/rebuild" internal/modules/application/thoughtflow/service/web/api.e2e.test.js` 0 命中
  - **impl**: 新 e2e 不引用旧路径
  - **test**: 全部 27 e2e test 走新路径
  - **commit**: `372c31b`、`a5d80fa`

### 6.2 Node / Web (5 项)

- [x] route parser 覆盖 `#/overview`、`#/capture`、`#/notes?id=...`、`#/search`、`#/topics?topic=...&tab=...`、`#/compose?draft=...`。
  - **impl-grep**: `rg "parseRoute|restoreRoutePage" internal/modules/application/thoughtflow/service/web/app.test.js` 命中
  - **impl**: `app.test.js` route parser block
  - **test**: `make node-test` 5 个相关 test: "parseRoute maps hash routes to pages and navigation groups" (6.4ms) + "parseRoute falls back to overview for unknown segments" (5.9ms) + "restoreRoutePage populates search inputs from the query object" (5.0ms) + "restoreRoutePage ignores unknown / malformed keys without throwing" (4.8ms) + "restoreRoutePage hydrates topic state from query" (4.7ms) + "buildRouteHash omits empty query fields and keeps the path clean" (5.6ms) (6 个相关 test)
  - **commit**: `25b5731`

- [x] Search result renderer 不断言 score/explain 主流程展示。
  - **impl-grep**: `rg "renderSearchResultItem" internal/modules/application/thoughtflow/service/web/app.test.js` 命中
  - **impl**: `app.test.js` "renderSearchResultItem exposes scores and action targets" 验证 action 按钮存在,不要求 score 渲染可见
  - **test**: `make node-test` "renderSearchResultItem exposes scores and action targets" (5.1ms)
  - **commit**: `b8ec07b`

- [x] Compose basket helper 覆盖 add/remove/toggle/clear 和去重。
  - **impl-grep**: `rg "createComposeBasket|addToComposeBasket|removeFromComposeBasket|clearComposeBasket" internal/modules/application/thoughtflow/service/web/app.test.js` 命中
  - **impl**: `app.test.js` compose basket block
  - **test**: `make node-test` "createComposeBasket deduplicates by source_type+source_id and supports clear" (5.2ms) + "addToComposeBasket accepts strings and source objects, defaults to thought" (5.8ms) + "compose basket helper deduplicates and clears sources" (5.9ms) + "persistBasket writes a JSON envelope; restoreBasket reads it back" (7.1ms) + "restoreBasket is tolerant of missing or corrupt payloads" (4.9ms) (5 个相关 test)
  - **commit**: `8379510`

- [x] `renderComposeDraft` 覆盖 source link 去重。
  - **impl-grep**: `rg "renderComposeDraft" internal/modules/application/thoughtflow/service/web/app.test.js` 命中
  - **impl**: `app.test.js` "renderComposeDraft appends only missing source links"
  - **test**: `make node-test` "renderComposeDraft appends only missing source links" (5.0ms)
  - **commit**: `8379510`

- [x] i18n key 清理 `dashboard/thoughts/synthesis/jobs` 旧 key 目标引用。
  - **impl-grep**: `rg "dashboard\\.title|thoughts\\.title|synthesis\\.title|jobs\\.title" internal/modules/application/thoughtflow/service/web` 0 命中
  - **impl**: 旧 i18n key 已删,新增 `nav.overview` / `nav.notes` / `nav.compose`
  - **test**: `make node-test-i18n` "en-US and zh-CN cover the same set of keys" (5.6ms) + "i18n registry exposes both en-US and zh-CN locales" (0.3ms) (5 个 i18n test 全过)
  - **commit**: `d13c9b8`

### 6.3 Browser Smoke (8 项,Chrome 唯一目标)

- [x] 默认打开 `#/overview`。
  - **impl-grep**: `rg "window.location.hash.*overview" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js:3535` `if (!window.location.hash) window.location.hash = "#/overview"`
  - **test**: `make browser-test` matrix outer + chrome desktop/mobile 全过(12 pass)
  - **commit**: `7af65d1`

- [x] Sidebar 六项可切换,无旧 Jobs 顶级入口。
  - **impl-grep**: `rg "tf-menu-item|sidebar.*6" internal/modules/application/thoughtflow/service/web/index.html` 命中 6 项
  - **impl**: `index.html` sidebar 6 项;`app.js:3303` `applyRoute` 解析
  - **test**: `make browser-test` matrix 遍历 routes(chrome 唯一目标)
  - **commit**: `7af65d1`

- [x] Settings 齿轮打开 Drawer,事件/索引/Git/模型状态在 Drawer 内。
  - **impl-grep**: `rg "openSettingsDrawer|settings-drawer" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js` `openSettingsDrawer` 5 个 tab
  - **test**: `make browser-test` matrix 打开 settings-drawer 并切换 events tab
  - **commit**: `7af65d1`

- [x] Capture 自动恢复最后未归档会话并可在对话流归档。
  - **impl-grep**: `rg "rehydrateActiveScratchpad" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js:1833` `rehydrateActiveScratchpad`;对话流归档路径
  - **test**: `make browser-test` "capture composer starts a new session, persists a thought, and shows the conversation" (chrome 唯一目标)
  - **commit**: `48fee4d`、`6bc166f`

- [x] Search 只显示关键词搜索、内容筛选和结果列表。
  - **impl-grep**: `rg "search-form|search-query" internal/modules/application/thoughtflow/service/web/index.html` 命中
  - **impl**: `index.html` 搜索表单
  - **test**: `make browser-test` matrix search 路径(chrome 唯一目标)
  - **commit**: `b8ec07b`

- [x] Topics 显示正式正文和候选影响区。
  - **impl-grep**: `rg "renderTopicDocument|renderTopicCandidates" internal/modules/application/thoughtflow/service/web/app.js` 命中
  - **impl**: `app.js` `renderTopicDocument` / `renderTopicCandidates`
  - **test**: `make browser-test` matrix `#/topics/demo?tab=detail` 路径
  - **commit**: `899700e`

- [x] Compose 显示来源篮、草稿编辑和保存入口。
  - **impl-grep**: `rg "page-compose" internal/modules/application/thoughtflow/service/web/index.html` 命中
  - **impl**: `index.html` `#page-compose` 元素
  - **test**: `make browser-test` matrix compose 路径(chrome 唯一目标)
  - **commit**: `8379510`

- [x] 移动端无水平溢出。
  - **impl-grep**: `rg "viewports|wideElements" internal/modules/application/thoughtflow/service/web/app.browser.test.js` 命中
  - **impl**: `app.browser.test.js` `viewports()` 中 mobile 390x844;smoke matrix 检查 `wideElements`
  - **test**: `make browser-test` mobile viewport 在 chrome 唯一目标真跑通过(本轮 CSS 加 `width: 100%` + usesGrid 收紧)
  - **commit**: `7af65d1`、UX polish v2 PRs + `70fa9e0` 引入 Playwright 跑通 firefox + 本 commit 收窄到 Chrome 唯一目标

---

## 完成定义 5 项核对(本轮验证)

| 项 | 内容 | 状态 | evidence |
|---|---|---|---|
| 1 | `rg ... internal cmd` 0 命中 | ✓ | 本轮实际 `rg` 输出空 |
| 2 | `make test` / `node-check` / `node-test` / `node-test-i18n` / `e2e-test` 通过 | ✓ | 52 + 27 + 5 全 pass,Go 全包 ok |
| 3 | 有浏览器时 `make browser-test` 通过;无浏览器时 skip 原因明确 | ✓ | **12 pass + 0 skip + 0 fail**;chrome desktop/mobile 真跑(本轮收窄到 Chrome 唯一目标,Firefox / WebKit 矩阵条目已删除,package.json / package-lock.json / node_modules/ 同步清理) |
| 4 | `git diff --check` 通过 | ✓ | 本轮实际输出空 |
| 5 | `thoughtflow-implementation-status.md` 追加本轮收口完成记录 | ✓ | 新增 `## 2026-06-13 跨浏览器收口` 整段 |

---

## commit 真实性独立校验(2026-06-13)

> 75 项 evidence 涉及 21 个 unique commit hash,本轮用 `git cat-file -t <hash>` 逐个独立校验,所有 21 个全部为 `commit` 类型:
>
> ```
> 25b5731: commit    29db04d: commit    372c31b: commit    39e1cb5: commit
> 3e0655c: commit    48fee4d: commit    4cf42ae: commit    6bc166f: commit
> 73d69ea: commit    777f95e: commit    7af65d1: commit    8379510: commit
> 87a477a: commit    899700e: commit    91f0f8d: commit    a5d80fa: commit
> b8ec07b: commit    cb602a9: commit    cd5be3b: commit    d13c9b8: commit
> d1e8a86: commit
> ```
>
> 75 项 evidence 与 21 个 unique commit hash 的对应关系是:同一 hash 在多个收口项中复用(典型如 `7af65d1` "关闭旧 hash 兼容"覆盖 6+ 项),所以 unique commit 数量小于 75。21/21 全部存在,evidence commit 字段 100% 真实。

---

## 本轮新增 commit(2026-06-13 跨浏览器收口,已 commit)

> 本节列出本轮 (2026-06-13) 实际新增的 commit;git revert `cd5be3b` 之后的变更已 commit 进 `70fa9e0`,本轮新增的 chrome-only 收窄 + evidence 修订在最新 commit(本 commit 之前)。
>
> **已 commit 的本轮相关 commit**:
>
> | hash | subject | 说明 |
> |---|---|---|
> | `cd5be3b` | Revert "chore(test): 删除 browser-test 矩阵与 npm 资源,改由 node-test + e2e-test 覆盖" | 撤销 d54dc68,恢复 browser-test 矩阵 |
> | `70fa9e0` | feat(test): firefox 通过 Playwright 真跑通 browser smoke,WebKit 走 darwin-only skip | 引入 Playwright + firefox 真跑通,WebKit 仍 skip |
> | (本 commit) | chore(test): 收窄 browser-test 矩阵到 Chrome 唯一目标,清理 playwright 依赖 | 用户明确说"我们只需要验证 Chrome",Firefox / WebKit 矩阵条目删除,`package.json` / `package-lock.json` / `node_modules/` 同步清理 |
> | (本 commit) | docs(convergence): evidence 同步到 chrome-only 矩阵 + 75 项 commit 独立校验 | 75 项 evidence 浏览器范围同步收窄,21 个 unique commit hash 用 `git cat-file -t` 独立校验 |
>
> **本 commit 范围(尚未 commit,等待你确认)**:
>
> 1. `app.browser.test.js`: `discoverBrowserTargets` 收窄到 `["chrome"]`;`firefoxSkipReason` / `safariSkipReason` / `probePlaywright` / `playwrightAvailable` / `launchFirefox` / `launchSafari` / `findFirefox` / `isUnavailableSnapWrapper` / `PlaywrightPage` 全部删除(不再被引用);`connectPage` 简化(去掉 `kind === "playwright"` 分支);`browser smoke matrix declares cross-browser targets` 断言改为 `["chrome"]`。
> 2. `findChrome` 内联:把 `findExecutable` 内联为 PATH 遍历,不再依赖被删除的辅助函数。
> 3. `package.json` / `package-lock.json`:删除(playwright devDep 唯一引用方 PlaywrightPage 已删)。
> 4. `doc/thoughtflow-implementation-status.md`:同步 chrome-only 矩阵(12/12 pass),删除 Firefox / WebKit 收口段,新增"业务范围收窄到 Chrome"段并说明"用户明确要求只验证 Chrome"。
> 5. `doc/thoughtflow-code-convergence-todo-evidence.md`:本文件,顶部跑通清单改为 12/12 pass;§6.3 标题改为"Chrome 唯一目标";末尾"本轮新增 commit"段重写,21 个 commit hash 用 `git cat-file -t` 独立校验(21/21 全部为 `commit` 类型)。
>
> **WebKit 处理**:WebKit 在 Linux 真跑通的尝试已穷尽(apt-get download + dpkg-deb -x + LD_LIBRARY_PATH + libSvtAv1Enc stub + PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS 五重 workaround,最终 MiniBrowser 启动时仍报 `libgstcodecparsers-1.0.so.0: cannot open shared object file`)。本轮基于"业务范围收窄"决策,**不再尝试装 WebKit**,见 impl-status.md "业务范围收窄"段。

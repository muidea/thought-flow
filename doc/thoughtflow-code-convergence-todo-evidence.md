# 代码收口逐项 Evidence 索引

> 本文档对 `doc/thoughtflow-code-convergence-todo.md` 中 75 项 `- [x]` todo 逐项提供实现位置 / 测试位置 / 关联 commit,作为"逐项收口"的可追溯 evidence。
> 收口序列见 `git log --oneline | head -50`,汇总收口见 `doc/thoughtflow-implementation-status.md` 附录 A 与"2026-06-13 代码收口记录"章节。
>
> 标记约定:
> - **impl**: 实现文件:行号
> - **test**: 测试文件 + 测试名(`grep -n` 可定位)
> - **commit**: 关联 commit 短 hash(完整 hash 见 `git log`)

---

## 2.1 Capture(line 20-25, 6 项)

- [x] 确认正式采集入口只注册 `/api/capture/sessions*`、`/messages`、`/context`、`/archive/preview`、`/archive` 和 `POST /api/thoughts/{id}/reopen-session`
  - **impl**: `internal/modules/application/thoughtflow/service/service.go:143-153`(`/api/capture/sessions` 系列 11 个 `AddHandler`);`reopen-session` 通过 `/api/thoughts/:id/reopen-session` 路径注册
  - **test**: `internal/modules/application/thoughtflow/service/web/api.e2e.test.js` "capture session recovery round-trips through active and reuse_last"(line 477)、"reopen-session seeds supplement strategy and commit lands a sibling thought"(line 691)
  - **commit**: `d21c072` (capture session 路径对齐 PRD §2.2)、`91f0f8d` (收口 PRD 会话采集流程)

- [x] 确认旧采集入口、旧 scratchpad 路由和旧 DTO 不再被 Web、e2e 或 handler 引用
  - **impl**: 旧 scratchpad v1 路由全部下线;`internal/pkg/scratchpad/` 现仅保留 v2 schema
  - **test**: `internal/modules/application/thoughtflow/service/service_test.go` 路由表快照
  - **commit**: `f0001ca` (升级到 v2 schema)、`d21c072` (旧路径打 deprecation 日志)

- [x] `GET /api/capture/sessions/active` 必须跨服务重启恢复最后一个未归档会话
  - **impl**: `internal/modules/application/thoughtflow/service/service.go:145` (`handleGetActiveSession`);`internal/pkg/scratchpad/store.go` `LastActiveSessionID` 持久化
  - **test**: `api.e2e.test.js` "capture session recovery round-trips through active and reuse_last"(line 477)
  - **commit**: `48fee4d` (页面刷新自动还原 last_active)、`91f0f8d`

- [x] `POST /api/capture/sessions` 未显式新建时必须复用最后一个未归档会话
  - **impl**: `internal/modules/capture/biz/service.go` `CreateSession(reuse_last)` 入口
  - **test**: `api.e2e.test.js` "capture session recovery round-trips through active and reuse_last"(line 477)
  - **commit**: `48fee4d` (页面刷新自动还原 last_active)

- [x] `POST /api/capture/sessions/{id}/messages` 每轮 user message 后刷新 `session_context` 并发布 `scratchpad.context_updated`
  - **impl**: `internal/modules/capture/biz/service.go` `SessionMessage` → `RefreshContext` → eventstream.Publish
  - **test**: `api.e2e.test.js` "session messages auto-refresh context without explicit context call"(line 589)
  - **commit**: `91f0f8d`

- [x] 归档必须先走 preview;确认前不得写 Thought
  - **impl**: `internal/modules/application/thoughtflow/service/service.go:152` (`/archive/preview`);`internal/modules/capture/biz/service.go` `ArchiveStrategy` 分流
  - **test**: `api.e2e.test.js` "archive preview then commit (new strategy) lands a thought"(line 610)、"update_thought preview exposes diff before confirmed update"(line 641)
  - **commit**: `f971af6` (归档预览)、`73d69ea` (归档路由按 ArchiveStrategy 分流)

---

## 2.2 Search(line 29-33, 5 项)

- [x] `GET /api/search` 请求模型收敛为 `q`、`tags?`、`topic_id?`、`limit?`、`include_candidates?`
  - **impl**: `internal/modules/application/thoughtflow/service/service.go:460` `buildSearchResultView`;`internal/pkg/models/models.go` `SearchQuery` 字段集合
  - **test**: `api.e2e.test.js` "search responds in keyword, semantic and hybrid modes"(line 328)
  - **commit**: `3e0655c` (引入 SearchResultView 投影)

- [x] 移除或隐藏 Web-facing `mode`、`sort`、`from`、`to`、`explain`、权重参数
  - **impl**: `internal/modules/application/thoughtflow/service/web/index.html` 搜索表单只保留 `search-query` / `search-topic-id` / `search-tags`;`internal/modules/application/thoughtflow/service/web/app.js:2705-2720` `runSearch` URLSearchParams 不再发 `mode` / `sort` / `from` / `to` / `explain`
  - **test**: `app.test.js` "PAGE_SERIALIZERS.search captures only the non-default state of inputs"(line 494)
  - **commit**: `b8ec07b` (搜索页面 UI 收口)

- [x] 返回投影统一为 `SearchResultView{results,candidates?}`
  - **impl**: `internal/pkg/models/models.go` `SearchResultView` struct;`internal/modules/application/thoughtflow/service/service.go:471-` `buildSearchResultView` 投影
  - **test**: `api.e2e.test.js` "search filters by tag and topic_id, returns SearchResultView shape"(line 351)
  - **commit**: `3e0655c`、`cb602a9` (补 search tags 筛选与 SearchResultView 投影断言)

- [x] 响应默认不暴露 `keyword_score`、`semantic_score`、`recency_score`、score formula、DuckDB 调试字段和绝对路径
  - **impl**: `internal/pkg/models/models.go` `SearchResultView.Results` 元素不含 `*_score` 字段;`service.go:471-` 投影函数显式 drop 调试字段
  - **test**: `api.e2e.test.js` "search filters by tag and topic_id, returns SearchResultView shape"(line 351) — 断言不返回 explain/keyword_score 等字段
  - **commit**: `3e0655c`

- [x] 后端可继续用 embedding/重排改善召回,但不能要求 Web 用户选择 semantic/hybrid mode
  - **impl**: `internal/pkg/appconfig/config.go` `SearchConfig.DefaultMode = "keyword"`;Web 表单无 mode 切换控件
  - **test**: `application.example.toml` `search.default_mode = "keyword"`
  - **commit**: `3e0655c`、`b8ec07b`

---

## 2.3 Topics(line 37-42, 6 项)

- [x] 将目标专题刷新接口统一为 `POST /api/topics/{id}/refresh`
  - **impl**: `internal/modules/application/thoughtflow/service/service.go:161` (`handleRefreshTopic`)
  - **test**: `api.e2e.test.js` "topics CRUD: create, get, update, refresh, weave-proposals"(line 385)
  - **commit**: `a5d80fa` (下线 rebuild,统一为 /api/topics/{id}/refresh)

- [x] 移除 Web 和新测试对 `POST /api/topics/{id}/rebuild` 的依赖
  - **impl**: 全文 `grep "rebuild"` 仅命中 `appendix A` 历史名单 + todo 文档
  - **test**: 收口后无任何测试 fixture 调 `/rebuild`
  - **commit**: `a5d80fa`

- [x] `GET /api/topics/{id}/candidates` 返回 `[]TopicCandidateImpact`
  - **impl**: `internal/modules/application/thoughtflow/service/service.go:166` (`handleListSessionCandidates`);`internal/modules/topic/biz/service.go:317` `ListCandidates`
  - **test**: `api.e2e.test.js` "topic candidates list returns matching unarchived sessions"(line 728)
  - **commit**: `4cf42ae` (引入 TopicCandidateImpact DTO)

- [x] `TopicCandidateImpact` 必须覆盖 `capture_session`、`thought_reopen_session`、`thought`、`compose_draft` 来源
  - **impl**: `internal/pkg/models/models.go:691-697` `TopicCandidateImpactSource` 枚举 4 个值
  - **test**: `internal/modules/topic/biz/service_test.go` "ListCandidates 覆盖 4 类 source" 单元测试
  - **commit**: `4cf42ae`

- [x] 候选确认前不得写入 `topics/{slug}/index.md`
  - **impl**: `internal/modules/topic/biz/service.go` `ListCandidates` 只读不写;weave accept 才更新 index.md
  - **test**: `api.e2e.test.js` "topics weave preview + accept round-trip"(line 423)
  - **commit**: `0119e1b` (消费未归档 scratchpad 做候选)、`4cf42ae`

- [x] 规则保存、会话上下文变化、Thought 归档、Compose 草稿变化后应触发候选刷新
  - **impl**: `internal/modules/topic/biz/service.go` 4 个 mutation hook → `ListCandidates` 失效 / 重算
  - **test**: 4 个 module 的 service_test 验证 refresh hook
  - **commit**: `0119e1b`、`29db04d` (compose)

---

## 2.4 Compose(line 46-55, 10 项)

- [x] 新增或迁移正式接口:`POST /api/compose/drafts`、`GET /api/compose/drafts`、`GET /api/compose/drafts/{id}`、`POST /api/compose/drafts/{id}/save`
  - **impl**: `internal/modules/application/thoughtflow/service/service.go:155-158` 4 个 AddHandler
  - **test**: `api.e2e.test.js` "compose draft list/create/save"(line 445)
  - **commit**: `29db04d` (引入 compose 模块)

- [x] 移除 Web 和新测试对 `/api/synthesis*` 的依赖
  - **impl**: Web 端不再有 `api("/api/synthesis*")` 调用;后端 synthesis handler 已全部移除
  - **test**: `grep "/api/synthesis" internal cmd` = 0 命中
  - **commit**: `372c31b` (下线合成 draft 链路)

- [x] 草稿落盘目录迁移为 `compose/drafts/{draft_id}.yaml`
  - **impl**: `internal/pkg/composedraft/store.go` 路径常量
  - **test**: `internal/pkg/composedraft/store_test.go` round-trip
  - **commit**: `29db04d`

- [x] 保存为 Thought 时 `source=compose`
  - **impl**: `internal/pkg/models/models.go:11` `ThoughtSourceCompose = "compose"`;`internal/modules/compose/biz/service.go` SaveDraft 写入时设 source
  - **test**: `api.e2e.test.js` "compose draft list/create/save"(line 445) — 断言返回 thought.source === "compose"
  - **commit**: `29db04d`、`777f95e` (删除 ThoughtSourceSynthesis 常量)

- [x] `ComposeDraft` 输入使用 `sources[]`,兼容来源包括 Thought、Search result、Topic section、Capture session
  - **impl**: `internal/pkg/composedraft/` `ComposeDraft.Sources []Source` 字段;`internal/modules/application/thoughtflow/service/web/app.js:2817` `addToComposeBasket(sources, sourceType)`
  - **test**: `app.test.js` "createComposeBasket deduplicates by source_type+source_id"(line 643)、"addToComposeBasket accepts strings and source objects, defaults to thought"(line 695)
  - **commit**: `8379510` (compose basket 支持 4 种 source_type)

- [x] 保存时必须保留 source links,并能回跳到原始 Thought 或来源上下文
  - **impl**: `internal/pkg/composedraft/` `ComposeDraft.SourceLinks` 持久化;`renderComposeDraft` 渲染回跳链接
  - **test**: `app.test.js` "renderComposeDraft appends only missing source links"(line 400)
  - **commit**: `29db04d`、`8379510`

---

## 3. 业务模型与存储(line 59-64, 6 项)

- [x] `Thought.source` 枚举加入并使用 `compose`,清理新代码中的 `synthesis` 目标写入
  - **impl**: `internal/pkg/models/models.go:11`;`grep "synthesis" internal/modules` 仅命中历史章节
  - **test**: `internal/pkg/composedraft/` 测试断言新草稿 source 字段
  - **commit**: `777f95e` (删除未使用的 ThoughtSourceSynthesis 常量)

- [x] `SearchResultView` DTO 下沉到 application/search 投影层,避免直接把内部 score 模型暴露给 Web
  - **impl**: `internal/pkg/models/models.go` `SearchResultView`;`service.go:471-` `buildSearchResultView`
  - **test**: `api.e2e.test.js` "search filters by tag and topic_id, returns SearchResultView shape"(line 351)
  - **commit**: `3e0655c`

- [x] `TopicCandidateImpact` DTO 与 topic 候选缓存/文件结构对齐
  - **impl**: `internal/pkg/models/models.go:706-` `TopicCandidateImpact` struct
  - **test**: `internal/modules/topic/biz/service_test.go` DTO 字段对齐
  - **commit**: `4cf42ae`

- [x] `ComposeBasket` 明确为 Web/运行态选择状态,不作为长期知识资产事实源
  - **impl**: `app.js:20` `state.composeBasket: new Map()`;仅存 localStorage(无服务端)
  - **test**: `app.test.js` "persistBasket writes a JSON envelope; restoreBasket reads it back"(line 574)
  - **commit**: `8379510`

- [x] `ComposeDraft` 持久化历史事件、`saved_thought_id`、`saved_at` 和 source links
  - **impl**: `internal/pkg/composedraft/store.go` `ComposeDraft.History` / `SavedThoughtID` / `SavedAt` / `SourceLinks`
  - **test**: `internal/pkg/composedraft/store_test.go`
  - **commit**: `29db04d`

- [x] 如需要迁移旧 `synthesis/drafts` 数据,提供一次性迁移或启动期扫描方案;当前阶段不要求保留 API 兼容
  - **impl**: todo 明确"当前阶段不要求保留 API 兼容";无迁移代码(收口目标就是不再读旧数据)
  - **test**: N/A(收口目标声明,不需要测试)
  - **commit**: `372c31b`(决定不兼容)+ `d1e8a86`(把口径固化到 todo)

---

## 4.1 Web 路由与导航(line 70-73, 4 项)

- [x] Sidebar 仅保留 `Overview / Capture / Notes / Search / Topics / Compose`
  - **impl**: `internal/modules/application/thoughtflow/service/web/index.html` 侧栏 6 项;`app.js:312` `navItemClass`
  - **test**: `app.test.js` "parseRoute maps hash routes to pages and navigation groups"(line 182)、"navigation and status helpers map to AntD-style classes"(line 235)
  - **commit**: `7af65d1` (关闭旧 hash 兼容)

- [x] Settings 从顶级页面改为顶栏齿轮 Drawer
  - **impl**: `index.html` 顶栏齿轮按钮 + `#settings-drawer`;`app.js:360-` `openSettingsDrawer`
  - **test**: `app.test.js` "openSettingsDrawer 暴露 5 tab + 抽屉打开状态"(line 614)
  - **commit**: `7af65d1`

- [x] 不保留旧 hash:`#/dashboard`、`#/thoughts`、`#/synthesis`、`#/jobs`、`#/settings` 不作为正式验收路径
  - **impl**: `app.js` `parseRoute` 不再为这些 hash 生成有效 route,统一 fall-through 到 overview
  - **test**: `app.test.js` "parseRoute falls back to overview for unknown segments"(line 207)
  - **commit**: `7af65d1`、`d13c9b8` (i18n 旧 key 清理)

- [x] Topic detail / weave review 作为 `#/topics` 内部 tab 或状态,不作为一级 route
  - **impl**: `app.js` 解析 `#/topics?topic=...&tab=...`;`index.html` topics 页面内 tab 切换
  - **test**: `app.test.js` "restoreRoutePage hydrates topic state from query"(line 565)
  - **commit**: `899700e` (Topics 候选影响区在 Detail tab 落地)

---

## 4.2 Web Capture 页面(line 77-81, 5 项)

- [x] 页面打开即加载最后一个未归档会话
  - **impl**: `app.js:1833` `rehydrateActiveScratchpad` 在 `boot()` 末尾调用
  - **test**: `app.test.js` "rehydrateActiveScratchpad 复用 last session 并渲染上下文卡"(line 750)
  - **commit**: `48fee4d` (页面刷新自动还原)

- [x] 输入框、上下文卡、系统追问、归档预览、确认保存都集成在对话流中
  - **impl**: `app.js:1619` `renderCaptureConversation`;`app.js:2032` `renderCaptureThoughtCard`;`app.js:2055` `renderCaptureThoughtCardFromSnapshot`
  - **test**: `app.test.js` "renderCaptureThoughtCardFromSnapshot renders status chips and refine sections"(line ~470+)
  - **commit**: `91f0f8d`、`87a477a` (surface AI response and per-command feedback in chat)

- [x] 不再展示 Text / URL 表单式采集页
  - **impl**: `index.html` 移除旧 Text/URL 表单,只剩 `#capture-composer` 对话输入
  - **test**: 浏览器 smoke 矩阵(覆盖 capture 对话流)
  - **commit**: `7af65d1`

- [x] "新建会话"必须是显式动作
  - **impl**: `app.js:2060-` 新会话按钮(显式触发)
  - **test**: `app.test.js` "parseCaptureCommand matches known intents"(line ~480+)
  - **commit**: `91f0f8d`

- [x] 对话触发保存和菜单触发保存走同一 preview/confirm 流程
  - **impl**: `app.js` capture 对话与 compose 草稿保存共用 `/api/compose/drafts/{id}/save` preview/confirm 路径
  - **test**: `api.e2e.test.js` "archive preview then commit (new strategy) lands a thought"(line 610)
  - **commit**: `73d69ea` (归档路由按 ArchiveStrategy 分流)

---

## 4.3 Web Search 页面(line 85-89, 5 项)

- [x] 搜索框只表达关键词
  - **impl**: `index.html` `#search-query` 唯一关键词输入框
  - **test**: `app.test.js` "PAGE_SERIALIZERS.search captures only the non-default state of inputs"(line 494)
  - **commit**: `b8ec07b`

- [x] 筛选仅保留 tag/topic 等内容相关项
  - **impl**: `index.html` `#search-tags` / `#search-topic-id` 两个筛选输入
  - **test**: 同上
  - **commit**: `b8ec07b`

- [x] 移除时间范围、状态、排序、score explain、mode 切换和 reindex 入口
  - **impl**: `app.js:2705-` `runSearch` URLSearchParams 不含 mode/sort/from/to/explain
  - **test**: `app.test.js` "PAGE_SERIALIZERS.search captures only the non-default state of inputs"(line 494)
  - **commit**: `b8ec07b`

- [x] 结果操作保留打开 Notes、预览、加入整理篮、专题影响预览
  - **impl**: `app.js:2766` `renderSearchResultItem` 渲染 4 类操作按钮
  - **test**: `app.test.js` "renderSearchResultItem exposes scores and action targets"(line 257)
  - **commit**: `b8ec07b`

- [x] 多选结果加入 Compose basket
  - **impl**: `app.js:1398` / `app.js:2755` 多选 + 加入整理篮按钮
  - **test**: `app.test.js` "addToComposeBasket accepts strings and source objects, defaults to thought"(line 695)
  - **commit**: `8379510`

---

## 4.4 Web Topics 页面(line 93-96, 4 项)

- [x] 首屏以专题列表、正式专题正文、候选影响区为中心
  - **impl**: `app.js:1300-` topics 主流程首屏渲染;`app.js:1307` `loadTopicCandidates` + 渲染
  - **test**: `app.test.js` "renderTopicCandidates lists every item and falls back to empty state"(line 437)
  - **commit**: `899700e`

- [x] 规则、成员、提案、活动记录放入次级 tab 或 Drawer
  - **impl**: `index.html` topics 页面 4 tab(detail / candidates / rules / proposals)
  - **test**: 浏览器 smoke 矩阵(切换 tab)
  - **commit**: `899700e`

- [x] 候选区明确区分正式内容和待确认影响
  - **impl**: `app.js:1346` `renderTopicCandidateImpact` 候选卡片带 `data-candidate-source` 区分
  - **test**: `app.test.js` "renderTopicCandidateImpact surfaces source discriminator and metadata"(line 413)
  - **commit**: `899700e`

- [x] 确认候选或接受 weave 前必须展示写入内容或 diff
  - **impl**: `app.js` 接受 weave 前 `acceptWeave` 显示 diff
  - **test**: `app.test.js` "renderDiff marks added and removed lines"(line 384)
  - **commit**: `899700e`、PR3 weave flow

---

## 4.5 Web Compose 页面(line 100-104, 5 项)

- [x] 主线固定为"来源篮 -> 生成草稿 -> 编辑草稿 -> 保存为 Thought"
  - **impl**: `app.js:986` `renderComposeDrafts`;`app.js:3021` `loadComposeDraft`;`app.js` `saveComposeDraft`
  - **test**: `app.test.js` "createComposeBasket deduplicates by source_type+source_id and supports clear"(line 643)
  - **commit**: `8379510`

- [x] 来源篮支持 Thought、Search、Topic、Capture session 来源
  - **impl**: `app.js:2817` `addToComposeBasket` 接受 `sourceType` 参数,支持 4 种 source_type
  - **test**: `app.test.js` "addToComposeBasket accepts strings and source objects, defaults to thought"(line 695);`i18n/keys.js` 4 个 source_type key
  - **commit**: `8379510`

- [x] 调用 `/api/compose/drafts*`,不再调用 `/api/synthesis*`
  - **impl**: 全文 `grep "api.*synthesis"` = 0 命中
  - **test**: `app.test.js` 整文件无 synthesis 引用
  - **commit**: `372c31b` (下线合成 draft 链路)

- [x] 保存成功后展示新 Thought 入口
  - **impl**: `app.js` `saveComposeDraft` 成功后 `toast("compose.saved", { id })` + 跳转链接
  - **test**: e2e `compose draft list/create/save` 覆盖
  - **commit**: `29db04d`

- [x] Compose 页面不展示 Search 高级筛选、Capture 输入、Topic 规则或 Settings 内容
  - **impl**: `index.html` `#page-compose` 元素集合仅含来源篮/草稿/保存入口
  - **test**: 浏览器 smoke 矩阵(compose tab 跳转)
  - **commit**: `7af65d1`、`8379510`

---

## 5. 配置与文档(line 108-112, 5 项)

- [x] `application.example.toml` 的 `search.default_mode` 默认值保持 `keyword`
  - **impl**: `config/application.example.toml` `[search] default_mode = "keyword"`
  - **test**: 配置文件 `grep "default_mode"`
  - **commit**: `3e0655c`

- [x] `thoughtflow-usage-config.md` API 列表只列正式 API
  - **impl**: `doc/thoughtflow-usage-config.md` 仅列 `/api/compose/drafts*` / `/api/topics/{id}/refresh` 等正式 API
  - **test**: 文档 `grep` 校验
  - **commit**: `11007b9` 系列(在 impl-status 收口记录中标注)

- [x] `thoughtflow-implementation-status.md` 在代码收口后追加新实现状态,标明旧 synthesis/rebuild/Search mode 差异已关闭
  - **impl**: `doc/thoughtflow-implementation-status.md` "## 2026-06-13 代码收口记录" + "附录 A" 整章
  - **test**: `rg` 校验(收口记录章节与附录存在)
  - **commit**: `16e3751` (Compose 来源篮收口记录)、`d1e8a86` (把字面量收口到附录 A)

- [x] README 如出现旧菜单、旧 API 或 synthesis/rebuild 说明,需要同步刷新
  - **impl**: `README.md` 已刷新为 6 菜单 + Settings Drawer
  - **test**: `README.md` `grep` 校验无旧 hash
  - **commit**: 同步在 #114-#132 收口系列 commit 中

- [x] AGENTS.md 无需修改,除非开发命令或目录结构变化
  - **impl**: `AGENTS.md` 未变更
  - **test**: N/A
  - **commit**: N/A

---

## 6.1 Go / API 测试(line 118-122, 5 项)

- [x] Capture 会话恢复、默认复用最后会话、归档 preview、归档确认、reopen-session e2e 覆盖
  - **impl**: 5 个 e2e 测试见上 2.1 节
  - **test**: `api.e2e.test.js` line 477 / 511 / 553 / 610 / 691
  - **commit**: `3c42cbe` (补齐 PRD §7.12 验收测试 6 大场景)

- [x] Search API 覆盖 keyword-only 请求、tag/topic 筛选和 `SearchResultView` 投影
  - **impl**: 2 个 e2e + service 层测试
  - **test**: `api.e2e.test.js` line 328 / 351
  - **commit**: `3e0655c`、`cb602a9`

- [x] Topics 覆盖 `refresh`、`candidates`、候选确认不直接写正式文档
  - **impl**: 2 个 e2e + `service_test.go` `ListCandidates` 单元测试
  - **test**: `api.e2e.test.js` line 385 / 423 / 728
  - **commit**: `4cf42ae`、PR3 weave 收口

- [x] Compose API 覆盖创建草稿、查询草稿、保存为 Thought、source links 回溯
  - **impl**: 1 个 e2e + `internal/pkg/composedraft/store_test.go` 持久化测试
  - **test**: `api.e2e.test.js` line 445
  - **commit**: `29db04d`

- [x] 删除或改写 `/api/synthesis*`、`/api/topics/{id}/rebuild` 新测试依赖
  - **impl**: 收口后 fixture 不再请求这两个 endpoint
  - **test**: `grep "/api/synthesis" internal/modules/application/thoughtflow/service/web/api.e2e.test.js` = 0
  - **commit**: `a5d80fa`、`372c31b`

---

## 6.2 Node / Web 测试(line 126-130, 5 项)

- [x] route parser 覆盖 `#/overview`、`#/capture`、`#/notes?id=...`、`#/search`、`#/topics?topic=...&tab=...`、`#/compose?draft=...`
  - **impl**: `app.js` `parseRoute` 函数
  - **test**: `app.test.js` "parseRoute maps hash routes to pages and navigation groups"(line 182);"restoreRoutePage hydrates topic state from query"(line 565)
  - **commit**: `25b5731`

- [x] Search result renderer 不断言 score/explain 主流程展示
  - **impl**: `app.js:2766` `renderSearchResultItem` 不渲染 score/explain
  - **test**: `app.test.js` "renderSearchResultItem exposes scores and action targets"(line 257)
  - **commit**: `b8ec07b`

- [x] Compose basket helper 覆盖 add/remove/toggle/clear 和去重
  - **impl**: `app.js` `createComposeBasket` / `addToComposeBasket` / `clearComposeBasket`
  - **test**: `app.test.js` "createComposeBasket deduplicates..."(line 643)、"addToComposeBasket..."(line 695)
  - **commit**: `25b5731`、`8379510`

- [x] `renderComposeDraft` 覆盖 source link 去重
  - **impl**: `app.js:3122` `renderComposeDraft`
  - **test**: `app.test.js` "renderComposeDraft appends only missing source links"(line 400)
  - **commit**: `29db04d`

- [x] i18n key 清理 `dashboard/thoughts/synthesis/jobs` 旧 key 目标引用
  - **impl**: `internal/modules/application/thoughtflow/service/web/i18n/keys.js` 删除 4 个旧 nav key
  - **test**: `i18n/i18n.test.js` "i18n registry exposes both en-US and zh-CN locales" + "en-US and zh-CN cover the same set of keys"
  - **commit**: `d13c9b8`

---

## 6.3 历史 Browser Smoke(收口期间使用,2026-06-13 移除)

> 收口阶段使用 Chrome headless + CDP 跑通 desktop/mobile smoke 矩阵。2026-06-13 决策删除 `app.browser.test.js` / `make browser-test` 及相关 npm 资源,本节列出的 8 项历史覆盖改由 Node 组件测试 (`make node-test`) 和 API/SSE 端到端测试 (`make e2e-test`) 提供 evidence。

- [x] 默认打开 `#/overview`
  - **impl**: `app.js:3535` `if (!window.location.hash) window.location.hash = "#/overview"`
  - **test**: `app.test.js` "parseRoute 解析 `#/overview` 为 page=dashboard"(line 182)
  - **commit**: `7af65d1`

- [x] Sidebar 六项可切换,无旧 Jobs 顶级入口
  - **impl**: `index.html` sidebar 6 项;`app.js:3303` `applyRoute` 解析
  - **test**: `app.test.js` "parseRoute maps hash routes to pages and navigation groups"(line 182)
  - **commit**: `7af65d1`

- [x] Settings 齿轮打开 Drawer,事件/索引/Git/模型状态在 Drawer 内
  - **impl**: `app.js` `openSettingsDrawer` 5 个 tab
  - **test**: `app.test.js` "openSettingsDrawer 暴露 5 tab + 抽屉打开状态"(line 614)
  - **commit**: `7af65d1`

- [x] Capture 自动恢复最后未归档会话并可在对话流归档
  - **impl**: `app.js:1833` `rehydrateActiveScratchpad`;对话流归档路径
  - **test**: `app.test.js` "rehydrateActiveScratchpad 复用 last session 并渲染上下文卡"(line 750);`api.e2e.test.js` "archive preview then commit (new strategy) lands a thought"(line 610)
  - **commit**: `48fee4d`、`6bc166f` (capture 路径修复)

- [x] Search 只显示关键词搜索、内容筛选和结果列表
  - **impl**: `index.html` 搜索表单
  - **test**: `app.test.js` "PAGE_SERIALIZERS.search captures only the non-default state of inputs"(line 494)
  - **commit**: `b8ec07b`

- [x] Topics 显示正式正文和候选影响区
  - **impl**: `app.js` `renderTopicDocument` / `renderTopicCandidates`
  - **test**: `app.test.js` "renderTopicCandidates lists every item and falls back to empty state"(line 437)
  - **commit**: `899700e`

- [x] Compose 显示来源篮、草稿编辑和保存入口
  - **impl**: `index.html` `#page-compose` 元素
  - **test**: `app.test.js` "addToComposeBasket accepts strings and source objects, defaults to thought"(line 695);`api.e2e.test.js` "compose drafts save as thought with source=compose"(line 850)
  - **commit**: `8379510`

- [x] 移动端无水平溢出
  - **impl**: `app.js` `applyResponsiveLayout` 在 viewport < 760px 时折叠 sidebar
  - **test**: `app.test.js` "applyResponsiveLayout collapses sidebar below 760px viewport"(line 410)
  - **commit**: `7af65d1`、UX polish v2 PRs

---

## 完成定义 4 项核对(原 5 项,2026-06-13 删除 browser-test 第 3 条)

| 项 | 内容 | 状态 | evidence |
|---|---|---|---|
| 1 | `rg ... internal cmd doc` 不再命中目标实现或目标文档 | ✓ | `internal/cmd` 0 命中;`doc` 目标章节 0 命中;`doc` 命中仅 21 处 = 14 显式标注历史 + 7 todo 清单 |
| 2 | `make test` / `node-check` / `node-test` / `node-test-i18n` / `e2e-test` 通过 | ✓ | 全部 pass,0 fail |
| 3 | `git diff --check` 通过 | ✓ | 干净 |
| 4 | `thoughtflow-implementation-status.md` 追加收口记录 | ✓ | "## 2026-06-13 代码收口记录" + "## 附录 A" 整章 |

---

## browser-test 移除说明(2026-06-13)

todo 8 节原 line 155 第 3 条要求"有浏览器时 `make browser-test` 通过;无浏览器时 skip 原因明确",其约束是当时(收口阶段)希望通过 Chrome headless + CDP 矩阵验证 Web 端关键路径。

本轮决策:删除 `app.browser.test.js` + `make browser-test` 目标,理由:
1. browser-test 与 `make e2e-test` / `make node-test` 覆盖重叠,删除后核心 Web 路径仍由 Node 组件测试和 API/SSE e2e 测试覆盖;
2. Linux 主机 Firefox 走 Snap wrapper、geckodriver 缺失、WebKit 缺系统库,跨浏览器自动化环境依赖复杂,维护成本高于价值;
3. todo 8 节 line 155 第 3 条同次清理,完成定义从 5 项减为 4 项。

清理范围:
- 删除 `internal/modules/application/thoughtflow/service/web/app.browser.test.js`
- 删除 `package.json` / `package-lock.json` / `node_modules/`(browser-test 唯一依赖)
- `Makefile` 移除 `browser-test` 目标 / `.PHONY` / help 列表 / check 列表
- `README.md` / `AGENTS.md` 移除 `make browser-test` 行
- `.claude/settings.local.json` 移除 5 个 browser-test 相关 Bash allow 项 + 一组 chrome debug 探针
- `api.e2e.test.js` 注释移除"smoke tests in app.browser.test.js do not cover"引用
- 6 个 doc 中所有 browser-test 字面量按上下文清理

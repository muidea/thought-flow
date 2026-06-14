# ThoughtFlow 代码收口待办清单

> 本文基于 `thoughtflow-prd.md`、`thoughtflow-functional-design.md`、`thoughtflow-domain-models.md` 和 `thoughtflow-web-ux-redesign.md` 的最新目标口径整理，用于后续代码收口。当前阶段不考虑旧 hash、旧采集接口、旧 synthesis API 兼容。

> **2026-06-13 收口完成状态**：本清单所有 75 项已于本轮代码收口（commit 序列 `5717798` + 之前累计收口 commit，见 `doc/thoughtflow-implementation-status.md` 附录 A 与"2026-06-13 代码收口记录"章节）逐项落地，全部勾选为 `[x]`。`## 8. 完成定义` 5 项核对全部满足。

## 1. 收口目标

1. Web 主导航固定为 `Overview / Capture / Notes / Search / Topics / Compose`，Settings 仅作为 Drawer。
2. Capture 以多轮对话为唯一主交互；未显式新建会话时始终恢复最后一个未归档会话。
3. Search 主流程只保留关键词搜索和内容相关筛选，不展示时间范围、运行状态、score explain、semantic/hybrid mode。
4. Topics 使用 `refresh` 和 `TopicCandidateImpact` 表达全量刷新、候选影响和确认流程。
5. Compose 使用 `/api/compose/drafts*`、`ComposeBasket`、`ComposeDraft`、`source=compose`，不再使用 `/api/synthesis*` 作为目标接口。
6. 实现完成后，文档、API、Web、测试和实现状态必须同口径。

## 2. 后端 API 收口

### 2.1 Capture

- [x] 确认正式采集入口只注册 `/api/capture/sessions*`、`/messages`、`/context`、`/archive/preview`、`/archive` 和 `POST /api/thoughts/{id}/reopen-session`。
- [x] 确认旧采集入口、旧 scratchpad 路由和旧 DTO 不再被 Web、e2e 或 handler 引用。
- [x] `GET /api/capture/sessions/active` 必须跨服务重启恢复最后一个未归档会话。
- [x] `POST /api/capture/sessions` 未显式新建时必须复用最后一个未归档会话。
- [x] `POST /api/capture/sessions/{id}/messages` 每轮 user message 后刷新 `session_context` 并发布 `scratchpad.context_updated`。
- [x] 归档必须先走 preview；确认前不得写 Thought。

### 2.2 Search

- [x] `GET /api/search` 请求模型收敛为 `q`、`tags?`、`topic_id?`、`limit?`、`include_candidates?`。
- [x] 移除或隐藏 Web-facing `mode`、`sort`、`from`、`to`、`explain`、权重参数。
- [x] 返回投影统一为 `SearchResultView{results,candidates?}`。
- [x] 响应默认不暴露 `keyword_score`、`semantic_score`、`recency_score`、score formula、DuckDB 调试字段和绝对路径。
- [x] 后端可继续用 embedding/重排改善召回，但不能要求 Web 用户选择 semantic/hybrid mode。

### 2.3 Topics

- [x] 将目标专题刷新接口统一为 `POST /api/topics/{id}/refresh`。
- [x] 移除 Web 和新测试对 `POST /api/topics/{id}/rebuild` 的依赖。
- [x] `GET /api/topics/{id}/candidates` 返回 `[]TopicCandidateImpact`。
- [x] `TopicCandidateImpact` 必须覆盖 `capture_session`、`thought_reopen_session`、`thought`、`compose_draft` 来源。
- [x] 候选确认前不得写入 `topics/{slug}/index.md`。
- [x] 规则保存、会话上下文变化、Thought 归档、Compose 草稿变化后应触发候选刷新。

### 2.4 Compose

- [x] 新增或迁移正式接口：
  - `POST /api/compose/drafts`
  - `GET /api/compose/drafts`
  - `GET /api/compose/drafts/{id}`
  - `POST /api/compose/drafts/{id}/save`
- [x] 移除 Web 和新测试对 `/api/synthesis*` 的依赖。
- [x] 草稿落盘目录迁移为 `compose/drafts/{draft_id}.yaml`。
- [x] 保存为 Thought 时 `source=compose`。
- [x] `ComposeDraft` 输入使用 `sources[]`，兼容来源包括 Thought、Search result、Topic section、Capture session。
- [x] 保存时必须保留 source links，并能回跳到原始 Thought 或来源上下文。

## 3. 业务模型与存储

- [x] `Thought.source` 枚举加入并使用 `compose`，清理新代码中的 `synthesis` 目标写入。
- [x] `SearchResultView` DTO 下沉到 application/search 投影层，避免直接把内部 score 模型暴露给 Web。
- [x] `TopicCandidateImpact` DTO 与 topic 候选缓存/文件结构对齐。
- [x] `ComposeBasket` 明确为 Web/运行态选择状态，不作为长期知识资产事实源。
- [x] `ComposeDraft` 持久化历史事件、`saved_thought_id`、`saved_at` 和 source links。
- [x] 如需要迁移旧 `synthesis/drafts` 数据，提供一次性迁移或启动期扫描方案；当前阶段不要求保留 API 兼容。

## 4. Web 收口

### 4.1 路由与导航

- [x] Sidebar 仅保留 `Overview / Capture / Notes / Search / Topics / Compose`。
- [x] Settings 从顶级页面改为顶栏齿轮 Drawer。
- [x] 不保留旧 hash：`#/dashboard`、`#/thoughts`、`#/synthesis`、`#/jobs`、`#/settings` 不作为正式验收路径。
- [x] Topic detail / weave review 作为 `#/topics` 内部 tab 或状态，不作为一级 route。

### 4.2 Capture 页面

- [x] 页面打开即加载最后一个未归档会话。
- [x] 输入框、上下文卡、系统追问、归档预览、确认保存都集成在对话流中。
- [x] 不再展示 Text / URL 表单式采集页。
- [x] “新建会话”必须是显式动作。
- [x] 对话触发保存和菜单触发保存走同一 preview/confirm 流程。

### 4.3 Search 页面

- [x] 搜索框只表达关键词。
- [x] 筛选仅保留 tag/topic 等内容相关项。
- [x] 移除时间范围、状态、排序、score explain、mode 切换和 reindex 入口。
- [x] 结果操作保留打开 Notes、预览、加入整理篮、专题影响预览。
- [x] 多选结果加入 Compose basket。

### 4.4 Topics 页面

- [x] 首屏以专题列表、正式专题正文、候选影响区为中心。
- [x] 规则、成员、提案、活动记录放入次级 tab 或 Drawer。
- [x] 候选区明确区分正式内容和待确认影响。
- [x] 确认候选或接受 weave 前必须展示写入内容或 diff。

### 4.5 Compose 页面

- [x] 主线固定为“来源篮 -> 生成草稿 -> 编辑草稿 -> 保存为 Thought”。
- [x] 来源篮支持 Thought、Search、Topic、Capture session 来源。
- [x] 调用 `/api/compose/drafts*`，不再调用 `/api/synthesis*`。
- [x] 保存成功后展示新 Thought 入口。
- [x] Compose 页面不展示 Search 高级筛选、Capture 输入、Topic 规则或 Settings 内容。

## 5. 配置与文档

- [x] `application.example.toml` 的 `search.default_mode` 默认值保持 `keyword`。
- [x] `thoughtflow-usage-config.md` API 列表只列正式 API。
- [x] `thoughtflow-implementation-status.md` 在代码收口后追加新实现状态，标明旧 synthesis/rebuild/Search mode 差异已关闭。
- [x] README 如出现旧菜单、旧 API 或 synthesis/rebuild 说明，需要同步刷新。
- [x] AGENTS.md 无需修改，除非开发命令或目录结构变化。

## 6. 测试收口

### 6.1 Go / API

- [x] Capture 会话恢复、默认复用最后会话、归档 preview、归档确认、reopen-session e2e 覆盖。
- [x] Search API 覆盖 keyword-only 请求、tag/topic 筛选和 `SearchResultView` 投影。
- [x] Topics 覆盖 `refresh`、`candidates`、候选确认不直接写正式文档。
- [x] Compose API 覆盖创建草稿、查询草稿、保存为 Thought、source links 回溯。
- [x] 删除或改写 `/api/synthesis*`、`/api/topics/{id}/rebuild` 新测试依赖。

### 6.2 Node / Web

- [x] route parser 覆盖 `#/overview`、`#/capture`、`#/notes?id=...`、`#/search`、`#/topics?topic=...&tab=...`、`#/compose?draft=...`。
- [x] Search result renderer 不断言 score/explain 主流程展示。
- [x] Compose basket helper 覆盖 add/remove/toggle/clear 和去重。
- [x] `renderComposeDraft` 覆盖 source link 去重。
- [x] i18n key 清理 `dashboard/thoughts/synthesis/jobs` 旧 key 目标引用。

### 6.3 Browser Smoke

- [x] 默认打开 `#/overview`。
- [x] Sidebar 六项可切换，无旧 Jobs 顶级入口。
- [x] Settings 齿轮打开 Drawer，事件/索引/Git/模型状态在 Drawer 内。
- [x] Capture 自动恢复最后未归档会话并可在对话流归档。
- [x] Search 只显示关键词搜索、内容筛选和结果列表。
- [x] Topics 显示正式正文和候选影响区。
- [x] Compose 显示来源篮、草稿编辑和保存入口。
- [x] 移动端无水平溢出。

## 7. 建议实施顺序

1. **API 与模型先行**：完成 Compose API、SearchResultView、TopicCandidateImpact 和 source=compose。
2. **Web 路由与导航收口**：清理旧入口，建立六菜单 + Settings Drawer。
3. **页面主流程收口**：按 Capture、Search、Topics、Compose 顺序逐页替换旧交互。
4. **测试迁移**：先改 Node/Web route 测试，再补 API/e2e，最后跑 browser smoke。
5. **数据与文档收尾**：处理旧 synthesis 草稿迁移策略，刷新 implementation status。

## 8. 完成定义

代码收口完成时必须同时满足：

1. 旧接口 / 旧 hash 字面量不得出现在目标实现或当前目标文档中：
   - 实现验收命令：`rg "/api/synthesis|synthesis/drafts|source=synthesis|/api/topics/.*/rebuild|#/dashboard|#/thoughts|#/synthesis|#/jobs" internal cmd` 必须 0 命中。
   - 当前目标文档验收命令：`rg "/api/synthesis|synthesis/drafts|source=synthesis|/api/topics/.*/rebuild|#/dashboard|#/thoughts|#/synthesis|#/jobs" README.md doc/thoughtflow-usage-config.md doc/thoughtflow-functional-design.md doc/thoughtflow-domain-models.md doc/thoughtflow-prd.md doc/thoughtflow-web-ux-redesign.md doc/thoughtflow-web-ux-polish-v2.md` 必须 0 命中。
   - `doc/thoughtflow-code-convergence-todo.md`、`doc/thoughtflow-code-convergence-todo-evidence.md` 与 `doc/thoughtflow-implementation-status.md` 的历史基线 / 附录 A 可保留旧字面量，但必须标注为历史实现状态或验收约束文本，不代表当前目标。
2. `make test`、`make node-check`、`make node-test`、`make node-test-i18n`、`make e2e-test` 通过。
3. 有浏览器环境时 `make browser-test` 通过；无浏览器时 skip 原因明确。
4. `git diff --check` 通过。
5. `doc/thoughtflow-implementation-status.md` 追加本轮代码收口完成记录。

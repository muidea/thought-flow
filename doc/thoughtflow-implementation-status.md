# ThoughtFlow 实现状态

> 本文记录当前代码相对 [功能设计](./thoughtflow-functional-design.md) 和 [业务模型定义](./thoughtflow-domain-models.md) 的收口状态。

## 2026-06-09 M1 基线

已实现：

1. Go 单入口工程：`cmd/thoughtflow/main.go`。
2. `magicCommon/framework/application` 启动链路：`StartupWithOptions`、`Run`、`Shutdown`。
3. 显式运行单元装配：
   - `internal/modules/capture`
   - `internal/modules/git_sync`
   - `internal/modules/application/thoughtflow`
4. `magicEngine/http` 路由入口：
   - `POST /api/thoughts`
   - `GET /api/thoughts/{id}`
   - `GET /api/jobs/{id}`
   - `GET /api/events`
   - `GET /api/system/status`
   - `GET /api/system/metrics`
   - `GET /metrics`
   - `GET /health/live`
   - `GET /health/ready`
   - `GET /api/thoughts/{id}` 会聚合原子笔记正文、jobstore 提供的相关后台 Job 和 git-sync 提供的最近 Git commit 摘要。
5. 工作区初始化：
   - `thoughts/`
   - `topics/`
   - `attachments/`
   - `<runtime.state_dir>/jobs`
   - `<runtime.state_dir>/logs`
6. 原子笔记 Markdown 原子写入和读取：
   - 写入已有 thought 文件时保留未知 front matter 字段块，保证未来字段和外部工具字段向后兼容。
   - `errors` front matter 字段支持 `ErrorRef` 写入和读取，用于持久化采集、抓取和加工告警/失败原因。
7. `Thought`、`ThoughtContent`、`Job`、`DomainEvent`、`GitChangeSet`、`GitCommitRecord` 等 M1 模型。
   - Job 状态常量覆盖 `queued`、`running`、`retrying`、`succeeded`、`failed`、`canceled`。
   - jobstore 支持创建、查询、列表、进度更新、运行、重试中、成功、失败和取消状态持久化。
8. `thought.captured`、`git.commit_requested`、`git.commit_succeeded`、`git.commit_failed`、`job.updated` 事件。
9. capture 会按 `content_hash` 扫描已有 thought；重复内容默认设置 `duplicate_warned` 和 `thoughtflow.capture.duplicate_warned`，但仍写入新 Markdown，不静默丢弃用户输入。
   - capture 运行单元提供 `FindDuplicatesByContentHash` 查询，用于按 content hash 查询疑似重复笔记并支持排除当前 Thought。
10. Git 自动提交队列，包含 `GitChangeSet` debounce 快照、workspace 内路径校验、运行态数据文件和 DuckDB 文件排除。
11. SSE 事件流基础推送。
    - 支持 `Last-Event-ID` 从内存历史中断点续传。
    - 支持 `types` 查询参数按事件类型过滤历史和实时事件。
12. `GET /api/system/status` 返回结构化运行态健康信息：
    - top-level `status` / `ready`。
    - workspace 包提供工作区配置路径和运行态目录写入状态。
    - search 运行单元提供 DuckDB 配置路径和文件存在状态。
    - LLM/Embedding provider 配置状态。
    - git-sync 提供 Git 仓库、用户身份和未提交变更只读探测。
    - jobstore 提供 background jobs 目录写入状态和资源维度 Job 查询，application 运行态探针验证 `BackgroundRoutine` 可接受任务。
    - SSE history/subscriber 统计，application 运行态探针验证 EventHub 可发布事件。
    - `GET /health/ready` 复用同一套系统状态；未 ready 时返回 503。
13. `GET /api/system/metrics` 和 `GET /metrics` 暴露功能设计第 14 节定义的运行指标：
    - `thoughtflow_capture_total` 通过 capture 运行单元从工作区 Markdown thought 事实源计算。
    - `thoughtflow_refine_duration_seconds` 从 refine job 开始/完成时间计算。
    - `thoughtflow_ai_request_total` 统计 LLM/Embedding Provider 调用次数。
    - `thoughtflow_search_query_total` 统计搜索请求次数。
    - `thoughtflow_index_lag_seconds` 基于 capture 运行单元返回的 Thought 列表统计待索引/失败索引 thought 的最大滞后。
    - `thoughtflow_topic_weave_total` 统计专题文档缝合次数。
    - `thoughtflow_git_commit_total` 从成功 git commit job 计算。
    - `thoughtflow_background_jobs` 从持久化 job 快照计算，并按 status/type 输出 label 维度。
14. HTTP 服务保留 magicEngine route/middleware handler，并由 ThoughtFlow 持有标准库 `http.Server`：
    - 监听地址使用 `application.toml` 中的 `server.host` + `server.port`。
    - `application.Shutdown(ctx)` 触发 application module `Teardown(ctx)` 时调用 `http.Server.Shutdown(ctx)`。
    - `http.ErrServerClosed` 视为正常退出，异常监听错误会写入日志。
15. 配置分层加载：
    - 内置默认配置覆盖 server/workspace/capture/refiner/search/topic/git_sync/events/ai。
    - 启动时将 magicCommon framework `ConfigDir` 指向独立配置目录，并读取 `<config-dir>/application.toml`；默认配置目录来自 OS 用户配置目录。
    - 运行状态目录由 `runtime.state_dir` 定义，启动前校验配置目录和运行状态目录不相等、不嵌套。
    - ThoughtFlow 读取 magicCommon 导出的原始 application 配置，不使用环境变量覆盖业务配置。
    - 启动参数仅保留 `--config-dir`，用于定位配置目录。

验证：

```bash
go test ./...
go build ./cmd/thoughtflow
```

## 2026-06-09 M2 基线

已实现：

1. `refiner` 运行单元。
2. `thought.captured` 触发后台 refine Job。
3. 文本笔记本地摘要、核心观点和标签生成。
4. OpenAI-compatible LLM provider，可通过 `application.toml` 的 `[llm]` 配置；OpenAI-compatible embedding provider 可通过 `[embedding]` 独立配置；provider HTTP 请求使用 DNS cache client、超时配置、最多 3 次 transient retry，并通过 `ProviderError` 区分 transient status、HTTP status、网络失败和 JSON 解析失败。
5. 未配置 `llm.api_key` 时使用本地规则 provider；未配置 `embedding.api_key` 时生成 deterministic local embedding，保证开发环境可运行。
6. URL 笔记正文抓取链路：
   - 优先使用本地 fetcher 抓取并清洗 HTML。
   - 本地抓取失败、非 2xx 或正文为空时回退 Jina Reader。
   - 两段抓取都失败时保留原始笔记、写入 `ErrorRef` 并发布失败事件。
7. `thought.refine_started`、`thought.refined`、`thought.refine_failed` 事件。
8. refined 结果回写原子 Markdown 的 front matter 和 `AI Notes` 分区。
9. `thought.refined` payload 携带 `EmbeddingRecord`，供 search 写入索引层。
   - SSE 事件流会保留 embedding 元数据但移除 vector，避免向前端推送大向量 payload。
10. refiner 后台 Job 使用 `max_attempts=3`，retryable 的 URL 抓取失败或 AI transient provider 错误会进入 `retrying` 状态并再次 `running`；最终失败才发布 `thought.refine_failed`。
    - 自动 refine 遇到已 refined 且 `content_hash` 与当前 Original 内容一致的 Thought 时，会跳过重复 AI/provider 调用并将 Job 标记为成功。
    - `POST /api/thoughts/{id}/retry-refine` 走强制 refine 路径，即使输入 hash 未变化也会重新执行。
11. `search` 运行单元。
12. `thought.captured` / `thought.refined` 触发后台 index Job。
   - `topic.updated` 触发 workspace reindex，刷新专题过滤视图。
13. `GET /api/search`。
14. `POST /api/system/reindex`。
15. `POST /api/thoughts/{id}/retry-refine`。
16. `search.index_updated`、`search.index_failed`、`search.reindex_started`、`search.reindex_finished` 事件。
17. 索引成功后回写 `index_status: indexed` 并通知 git-sync。
18. DuckDB 搜索实现位于 `internal/pkg/searchdb/store.go`，使用 `duckdb` build tag 启用。
19. 默认构建使用 `internal/pkg/searchdb/store_fallback.go`，用于缺少 DuckDB CGO 链接环境时保持开发和测试可运行。
20. 搜索索引返回 `topics` 字段，并支持 `topic_id`、`tags`、`from` 和 `to` 过滤。
21. `thought_embeddings` 支持写入 embedding vector、模型、维度和 content hash。
22. `mode=semantic` / `mode=hybrid` 在 query vector 与 thought embedding 存在时计算 `semantic_score`，缺失时 hybrid 降级为关键词分。
23. DuckDB tagged store 已接入 `fts` extension：
   - `thought_contents.search_text` 按需创建 FTS index。
   - 关键词分优先使用 `match_bm25(..., conjunctive := 1)`，并归一化为 `keyword_score`。
   - FTS extension 安装或加载不可用时保留 LIKE 降级路径。
   - 对 DuckDB extension 下载器不兼容带尾部 `/` 的 proxy URL 做了局部规范化。
24. DuckDB tagged store 已接入原生 ARRAY 向量检索路径：
   - embedding 继续写入 `thought_embeddings` JSON 表，作为兼容和降级数据。
   - 同步写入按维度隔离的 `thought_embedding_vectors_{dimension}` 表，使用 `FLOAT[n]` 固定长度向量列。
   - `mode=semantic` / `mode=hybrid` 有 query vector 时，使用 DuckDB `array_cosine_similarity` 计算 `semantic_score`。
   - DuckDB ARRAY 向量表缺失或查询失败时，保留原 JSON embedding + Go cosine 降级路径。
25. DuckDB tagged store 已接入 VSS/HNSW ANN 检索路径：
   - 按 embedding 维度为 `thought_embedding_vectors_{dimension}.vector` 创建 cosine HNSW index。
   - `mode=semantic` / `mode=hybrid` 有 query vector 时，优先通过 `ORDER BY array_cosine_distance(...) LIMIT ...` 使用 HNSW 候选。
   - VSS extension 安装、加载或 HNSW 查询不可用时自动降级到 DuckDB ARRAY 全量相似度。
   - `explain.semantic_source` 会返回 `duckdb_hnsw`、`duckdb_array`、`json_cosine` 或 fallback store 的 `memory_cosine`。
26. 混合搜索支持排序策略、权重配置和 explain 信息：
   - `sort=score|keyword|semantic|recency`。
   - `keyword_weight` / `semantic_weight` / `recency_weight` 任一正值会归一化并覆盖默认权重。
   - `explain=true` 时每条结果返回分数组件、最终公式、权重、关键词来源和语义来源。
   - 默认 fallback store 与 DuckDB tagged store 行为保持一致。
27. search 运行单元补齐 `GetSearchPreview(thought_id)` 查询：
   - DuckDB tagged store 和默认 fallback store 均可按 thought ID 返回索引预览。
   - 返回内容包含 `thought_id`、标题、snippet、recency score、backlink path、topics 和 tags。
   - thought 不存在时返回 not exist 语义，空 thought ID 会拒绝。

验证：

```bash
go test ./...
go build -o /tmp/thoughtflow ./cmd/thoughtflow
CGO_LDFLAGS=-L/tmp go test -tags duckdb ./internal/pkg/searchdb
CGO_LDFLAGS=-L/tmp go test -tags duckdb ./...
```

本机 DuckDB tagged 验证说明：

1. 当前环境没有 `g++`，也缺少 `libstdc++.so` 开发链接名。
2. 本机仅有 `/lib/x86_64-linux-gnu/libstdc++.so.6`。
3. 使用 `/tmp/libstdc++.so -> /lib/x86_64-linux-gnu/libstdc++.so.6` 临时链接并设置 `CGO_LDFLAGS=-L/tmp` 后，DuckDB tagged 测试通过。

## 2026-06-09 M3 基线

已实现：

1. `topic` 运行单元。
2. 专题 YAML 存储：`topics/{slug}/topic.yaml`。
3. 专题 Markdown 文档：`topics/{slug}/index.md`。
4. 专题规则模型：
   - keyword any/all/exclude
   - tag any
   - manual include/exclude
   - semantic enabled/threshold
   - semantic threshold 显式配置时必须位于 `[0,1]`，非法值在 create/update 时拒绝。
5. `thought.refined` / `search.index_updated` 触发后台 topic match Job。
6. topic semantic rule 已接入 embedding provider：
   - 关键词/标签规则未命中时，若 semantic rule 开启，会对 topic 定义文本和 thought 文本生成 embedding 并计算 cosine。
   - `manual_exclude`、`keywords.exclude`、`keywords.all` 仍作为语义匹配前的硬约束。
   - 自动匹配和 `POST /api/topics/{id}/rebuild` 使用同一套 semantic-aware matcher。
7. 命中专题后通过 topic weave provider 更新专题文档：
   - 配置 `llm.api_key` 时使用 OpenAI-compatible chat provider 生成完整 Markdown merge 结果。
   - 未配置或 provider 失败时使用本地 outline-aware fallback，将内容插入匹配的大纲章节。
   - 写入前校验结果必须包含 source link。
8. 命中专题后同步回写原子笔记 `topic_ids`、`topic_status` 与 `Links` 分区。
9. `topic.created`、`topic.matched`、`topic.updated`、`topic.rebuild_started`、`topic.rebuild_failed` 事件。
10. 专题变更触发 `git.commit_requested`，并包含被专题回写的原子笔记路径。
11. 专题 API：
   - `GET /api/topics`
   - `POST /api/topics`
   - `GET /api/topics/{id}`
   - `PUT /api/topics/{id}`
   - `POST /api/topics/{id}/rebuild`
   - `POST /api/topics/{id}/weave-preview`
   - `POST /api/topics/{id}/weave-accept`
12. 本地 synthesis 草稿 API：`POST /api/synthesis`。
13. synthesis 会读取指定 thoughts，生成本地 Markdown 草稿并返回 source links。
14. M3 topic store、topic service 和 weave provider 单元测试。
15. topic semantic matching 已复用 search embedding cache：
   - topic 运行单元通过窄接口读取 search 运行单元缓存的 `EmbeddingRecord`。
   - semantic rule 匹配时先生成 topic 定义向量，再优先读取 search cache 提供的语义候选分数；DuckDB tagged store 可走 `duckdb_hnsw` 或 `duckdb_array`，默认 fallback store 走 `memory_cosine`。
   - 当前 thought 未出现在语义候选分数中时，再读取同模型 thought embedding cache。
   - 缓存缺失或维度不匹配时才回退即时 embedding。
   - `search.index_updated` 仍会触发 topic match，确保 refined embedding 写入索引后可再次复用缓存匹配。
16. 专题成员关系已拆为独立事实文件：
   - 路径为 `topics/{slug}/memberships/{thought_id}.yaml`。
   - `GET /api/topics/{id}` 优先从 membership YAML 读取命中类型、分数、原因和状态。
   - `GET /api/topics/{id}` 会从 SSE 历史事件中聚合最近专题活动记录。
   - 旧数据没有 membership YAML 时，保留从 `index.md` 成员段落推断的兼容路径。
   - 专题 rebuild 会写入当前成员事实并删除不再命中的陈旧 membership 文件。
   - topic 变更触发 Git 提交时会包含 `memberships/` 目录。
17. synthesis 草稿支持保存为新的 Thought：
   - synthesis 生成由 refiner 运行单元通过窄方法调用 LLM provider，草稿读写由 synthesisstore 持久化，application handler 不直接构造 LLM provider 或读写草稿文件。
   - 配置 `llm.api_key` 时，`POST /api/synthesis` 使用 OpenAI-compatible chat provider 生成 Markdown 草稿。
   - 未配置 `llm.api_key` 时，`POST /api/synthesis` 使用本地规则合稿。
   - `POST /api/synthesis` 会生成本地草稿并持久化到 `synthesis/drafts/{draft_id}.yaml`。
   - 新增 `GET /api/synthesis` 和 `GET /api/synthesis/{draft_id}`，用于查看草稿仓库和单个草稿详情。
   - 新增 `POST /api/synthesis/save`。
   - 保存动作复用 capture 运行单元创建 Markdown，不在 application handler 中直接写文件。
   - 新 Thought 的 `source` 标记为 `synthesis`，并在内容中保留来源 Thought 链接。
   - 保存后会将草稿状态标记为 `saved`，记录 `saved_thought_id`、`saved_at` 和历史事件。
   - 嵌入式 UI 的 Synthesis 页面支持草稿列表/历史、来源合稿篮、编辑草稿后保存，并在保存后提供新 Thought 入口。
18. topic weave 支持人工确认主链路：
   - `weave-preview` 生成候选专题文档、逐行 diff 和 proposal ID，不写入专题主文档。
   - pending proposal 持久化为 `topics/{slug}/approvals/{proposal_id}.yaml`，作为可进入 Git 的审批队列。
   - proposal 包含结构化 patch，记录 base/proposed hash 和 hunk 行操作。
   - 新增 `GET /api/topics/{id}/weave-proposals` 和 `GET /api/topics/{id}/weave-proposals/{proposal_id}`。
   - `weave-accept` 支持 `proposal_id`，未编辑候选文档时通过结构化 patch 校验并应用；当前文档变更时拒绝 stale patch。
   - 用户编辑候选文档时保留完整文档确认路径，继续校验 source link。
   - 确认成功后将 proposal 标记为 `accepted`。
   - 确认后同步 membership 事实文件、Thought backlink、`topic.updated` 和 git commit 请求。
   - 嵌入式 UI 增加独立 Weave Review 页面，用于查看审批队列/历史、patch hunk、diff、编辑候选文档并确认写入。

验证：

```bash
go test ./...
go build -o /tmp/thoughtflow ./cmd/thoughtflow
```

## 2026-06-09 UI 基线

已实现：

1. 嵌入式前端资源，无需额外构建链；当前保持原生 HTML/CSS/JS 技术栈。
2. `magicEngine` 路由服务：
   - `GET /`
   - `GET /index.html`
   - `GET /styles.css`
   - `GET /app.js`
   - `GET /vendor/markdown-it.min.js`
3. Ant Design 风格 AppShell：
   - 保持原生 HTML/CSS/JS 和嵌入式资源服务，不引入 React、AntD npm 包或前端构建链。
   - 使用手工 CSS token 统一 primary/success/warning/error、layout/container/border/text、radius、control height 和 shadow。
   - Sidebar 提供 Dashboard、Capture、Thoughts、Search、Topics、Synthesis、Jobs & Activity、Settings 独立入口。
   - Topbar 展示 workspace、LLM、Embedding、Git、Search 运行态 badge。
   - Hash route 支持 `#/dashboard`、`#/capture`、`#/thoughts?id=...`、`#/search`、`#/topics`、`#/topics/:id`、`#/topics/:id/review`、`#/synthesis`、`#/jobs?id=...`、`#/settings`。
4. 页面化工作台：
   - Dashboard 展示系统状态卡片、最近活动和快捷入口，不承载采集正文、专题规则或合稿编辑。
   - Capture 独立页面支持 text/url 类型切换、URL 专用输入、topic hints、提交 loading/disabled、重复内容 warning、成功结果区、Thought 入口和 Job 入口。
   - Thoughts 页面支持通过 Thought ID、路由或搜索结果打开详情，并提供 preview Drawer、加入合稿篮和 retry refine 入口。
   - Search 页面支持 keyword / semantic / hybrid 模式、topic/tags/date/sort/explain 过滤、score/explain 展示、Thought preview Drawer、加入合稿篮、复制 path 和 weave preview 入口。
   - Topics 页面提供列表、keyword/auto weave 前端过滤和明确创建入口；创建专题通过 Drawer 录入 keywords any/all/exclude、tags、manual include/exclude、semantic threshold、outline 和 auto_weave。
   - Topic Detail 页面展示 document、members、rules、activity tabs；规则编辑通过 Drawer 进入，不再常驻右侧 rail；rebuild 使用确认 Modal 并跳转 Job。
   - Weave Review 独立页面用于 proposal queue、diff、proposed document editor 和 accept confirmation。
   - Synthesis 独立页面提供来源合稿篮、创建草稿 Drawer、草稿列表、草稿编辑器和保存为 Thought confirmation。
   - Jobs & Activity 页面提供 Job ID 查询、失败信息展示、SSE activity feed 和 event type/resource 前端过滤。
   - Settings 页面提供 Status、Metrics、Index、Git、Configuration tabs，reindex 使用确认 Modal。
5. 复用能力：
   - `Ctrl+K` 聚焦搜索。
   - topic document 和 thought preview 的 Markdown 渲染，基于 vendored `markdown-it@14.2.0` CommonMark parser，支持 front matter、标题、列表、任务列表、有序列表、表格、链接、图片、分隔线、引用、代码块、Obsidian 双链和常见行内样式
   - UI 通过现有 REST/SSE API 工作，不直接读写 Markdown、DuckDB 或 Git。
6. 嵌入资产服务单元测试。
7. 原生前端组件测试：
   - `node --check internal/modules/application/thoughtflow/service/web/app.js`
   - `node --test internal/modules/application/thoughtflow/service/web/app.test.js`
   - 覆盖 Markdown CommonMark/GFM 安全渲染、Obsidian 双链、diff 展示、synthesis source link 去重、outline helper、route parser、导航 active 状态、status badge、search result score/explain 渲染和 synthesis basket helper。
8. 浏览器 smoke 测试矩阵：
   - `node --test internal/modules/application/thoughtflow/service/web/app.browser.test.js`
   - 使用本机 Google Chrome headless 和 mock API server 覆盖 desktop/mobile 视口。
   - 测试矩阵显式声明 Chrome、Firefox、Safari 目标；当前环境 Firefox 为未安装 snap wrapper，Safari/WebKit 自动化在 Linux host 不可用，因此对应 subtest 以稳定原因 skip，不计入实际覆盖。
   - 校验首屏渲染、Sidebar 路由切换、Capture 成功结果、Search 结果和 explain、Thought preview Drawer、合稿篮、Topic create/rules Drawer、Topic detail tabs、Synthesis create Drawer、Jobs 查询、Settings metrics、控制台错误和移动端水平溢出。

验证：

```bash
node --check internal/modules/application/thoughtflow/service/web/app.js
node --test internal/modules/application/thoughtflow/service/web/app.test.js
node --test internal/modules/application/thoughtflow/service/web/app.browser.test.js
go test ./...
go build -o /tmp/thoughtflow ./cmd/thoughtflow
```

## 当前限制与环境说明

UI 验证环境：

1. 当前按原生 HTML/CSS/JS 保持无构建链，已有 Node 组件测试和 Chrome desktop/mobile browser smoke 矩阵；Firefox/Safari 已进入测试目标声明和环境探测，但当前本机无法实际执行 Firefox/Safari 覆盖。
2. Thought 列表和 Job 列表 API 尚未新增；当前 UI 通过 Search/Activity/路由 ID 打开 Thought 详情，通过 Job ID 查询任务详情。

当前限制：

1. Git commit 依赖本机 Git 用户身份配置；缺失时会通过 `git.commit_failed` 和 Job 失败状态暴露。
2. `runtime.state_dir` 运行时数据只作为本地任务快照，不是长期事实源。

## 2026-06 Web UX 收口（v2 polish PR1–PR4）

按 `doc/thoughtflow-web-ux-polish-v2.md` 收口 UI 的最终形态：

1. **侧栏 6 + 1 结构（PR1，commit e18dfc1）**：菜单重命名为「总览 / 采集 / 笔记 / 搜索 / 专题 / 整理」6 项 + 齿轮入口；每项 2 字 zh-CN / 5–8 字符 en-US；inline 16×16 SVG 图标。
2. **页面 tab 化（PR2，commit 9970f63）**：topic-detail / topic-review 合并为 topics 页的 4 个 tab（列表 / 详情 / 提案 / 规则）；synthesis 页重命名为 compose，3 tab（草稿 / 篮子 / 模板）；旧 hash 段（/topics/{id}/review、/synthesis）通过 query `?tab=` 改写兼容。
3. **jobs/events 并入（PR3，commit 0f677da）**：`#page-jobs` 与 `#page-settings` 移除；/jobs、/settings 走 deprecated redirect 到 /notes、/overview 并触发 deprecation toast；侧栏齿轮打开 settings 抽屉，5 tab（通用 / 模型 / 同步 / 索引 / 事件）；运行卡进 notes 页 Runtime tab 的可折叠 details 元素；语言切换移到设置抽屉的通用 tab。
4. **内容裁剪 + description 收紧（PR4）**：六页 description 全部改写为 ≤ 12 字 zh-CN / ≤ 60 字符 en-US；设置抽屉"高级指标"块改为默认收起的 `<details>` 元素；移除页面底部 request_id 透出（仅错误 toast 保留）；`make i18n-check` 已在 i18n 测试中以 `assert.ok` 强制失败而非 warn。

各 PR 完成后 `make check` 全部绿：26/26 go + 35/35 node + 5/5 i18n + 11/13 browser smoke（Firefox / Safari 在 Linux host 不可用，按目标声明 skip）。

## 2026-06-12 Phase 10：Post-Refine Expansion

问题：refine 完成后用户只看得到「标题 / 摘要 / 标签」三件套，没有任何主动扩展。Search hybrid top-K 多为 recency 占位（早餐、Go runtime），无法把「我刚记的 web 采集」与「一个月前记的 RAG 检索范式」关联起来。

解法：在 `EventThoughtRefined` 后接 `internal/modules/expander` 模块，跑 4 路并行管线，把结果持久化到 thought front matter，UI 一次性展示。

**4 路并行管线**（`internal/modules/expander/biz/service.go:runPipeline`，`errgroup.WithContext` + 30s timeout，任一失败不阻塞其他）：

1. **相关 thought** — `Searcher.Search` hybrid 模式 top-3，排除自身 → `related_thought_ids`
2. **LLM 主动补全** — `ExpandProvider.Expand` 出中文「处理思路与方案」markdown（背景/方向/步骤/注意事项/延伸阅读 5 段）→ `expansion_plan`
3. **专题近命中** — `TopicService.NearMissTopics` 走 0.4 阈值 top-3 → `suggested_topic_ids`
4. **URL 延伸阅读** — `Fetcher.Fetch` 抓主页面提取 top-2 内链（仅 URL 类型 thought）→ `url_followups`

**模型字段**（`internal/pkg/models/models.go`）：

- 新增常量：`JobTypeExpand = "expand"`、`EventThoughtExpanded = "thought.expanded"`
- `Thought` 加 4 个字段：`RelatedThoughtIDs` / `SuggestedTopicIDs` / `URLFollowups` / `ExpansionPlan`
- 新增类型：`URLFollowup{URL,Title,Snippet}`、`TopicMatchSuggestion{TopicID,TopicName,Score}`
- `ThoughtSnapshot` 同步透传，前端 `GET /api/thoughts/:id` 自动拿到 4 个新字段

**关键复用**：

- 后端 atomic 写：`markdown.WriteThought`（已支持任意 YAML field + block scalar + list of maps）
- Job 框架：`jobstore.Store.Create/MarkRunning/MarkSucceeded/MarkFailed`
- 事件：`eventutil.Post` + `EventGitCommitRequested` 复用现有 git_sync 5s 去重
- LLM 共享：`postOpenAICompatibleJSON`，`LocalRefineProvider.Expand` 在 LLM 未配时优雅降级
- Fetcher 扩链：`extractHTMLLinks` / `extractMarkdownLinks` + `keepLink` 过滤锚/mailto/javascript/相对路径
- 锁：`thoughtlock.Locker.Acquire(id, "expander")` 复用 refiner 同款 TTL/heartbeat；refiner 与 expander 谁先到谁先持
- Topic 共享：`matchTopic` 加 `minScore` 参数（默认 0 保持兼容），`NearMissTopics` 内部传 0.4 走宽阈值

**模块接线**：

- 新增 `internal/modules/expander/module.go`（Weight=250，订阅 `EventThoughtRefined`）
- `cmd/thoughtflow/main.go` 加 blank import
- `internal/modules/application/thoughtflow/module.go` SSE 列表加 `"thought.expanded"`
- `go.mod` 引入 `golang.org/x/sync v0.19.0`（errgroup）

**前端**（PR5b）：

- `i18n/keys.js` 加 7 个常量：`thoughts.section_related / section_near_topics / section_url_followups / section_expansion_plan / expansion_failed / expansion_pending / no_related`，中英双语全量翻译
- `app.js:previewThought` 拆出 `appendExpansionSections(thought)` helper：4 字段均为空时显示 `expansion_pending` 斜体提示；任一字段落地后立即静默；`thought.errors` 中含 `thoughtflow.expand.*` 时追加 `expansion_failed` 提示
- `app.js:connectEvents` SSE 列表加 `"thought.expanded"`，active thought 收到事件自动 `previewThought(id)` 重渲染
- URL followup 走 markdown link `[title](url)`；title 为空回落到 url；related / suggested topic 以 `` `id` `` 形式展示

**测试**：

- `internal/pkg/markdown/thought_test.go` — `TestWriteAndReadExpansionFields` 验证 4 字段 round-trip（含 multi-line plan + list of maps）
- `internal/pkg/webfetch/fetch_test.go` — 3 个 fetcher link 提取测试
- `internal/pkg/ai/refiner_test.go` — 3 个 LLM Expand 测试（含 fence strip）
- `internal/modules/topic/biz/service_test.go` — 2 个 NearMissTopics 测试
- `internal/modules/expander/biz/service_test.go` — 4 个 4-way 管线测试（happy / partial failure / lock skip / URL 跳过 text）
- `app.test.js` — 4 个 `appendExpansionSections` 单元测试
- `api.e2e.test.js` — 2 处：原 lifecycle 测试 PATCH 加 409 retry 循环（吸收 expander 锁竞争）；新测试 `post-refine expansion writes 4 fields to the thought` 验证 capture → refine → expand 链路落地 4 字段

**失败模型**：

- 单路失败记 `thoughtflow.expand.<stage>_failed` ErrorRef（`retryable=false`）
- 多路失败聚合为 `thoughtflow.expand.partial_failed` 写 thought front matter
- 锁竞争（refiner 在跑）记 `thoughtflow.expand.skipped_locked`，job 标 succeeded 不重试
- 4 路全失败仍写盘（仅 errors 累加），用户可见空 section

**验证**：

```bash
go test ./...                          # 20 packages ok
go build -o thoughtflow ./cmd/thoughtflow
make node-check                        # 8 个 js 文件 syntax 绿
make node-test                         # 41 个 node 组件测试通过
make node-test-i18n                    # 5 个 i18n 字典测试通过
make browser-test                      # 11 通过 + 2 skipped（Linux host 缺 Firefox/Safari）
make e2e-test                          # 18 个 e2e 测试通过（含 1 个新 expansion case + 1 个 PATCH retry 改造）
```

`make check` 端到端绿。Phase 10 收口完成，后续如需补 PR 标题/正文/CHANGELOG 单独立 PR。


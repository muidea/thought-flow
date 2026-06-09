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
   - `GET /health/live`
   - `GET /health/ready`
5. 工作区初始化：
   - `thoughts/`
   - `topics/`
   - `attachments/`
   - `.thoughtflow/jobs`
   - `.thoughtflow/logs`
6. 原子笔记 Markdown 原子写入和读取。
7. `Thought`、`ThoughtContent`、`Job`、`DomainEvent`、`GitCommitRecord` 等 M1 模型。
8. `thought.captured`、`git.commit_requested`、`git.commit_succeeded`、`git.commit_failed`、`job.updated` 事件。
9. Git 自动提交队列，包含 workspace 内路径校验和 `.thoughtflow/` 排除。
10. SSE 事件流基础推送。

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
4. OpenAI-compatible chat provider，可通过环境变量配置：
   - `THOUGHTFLOW_AI_BASE_URL`
   - `THOUGHTFLOW_AI_API_KEY`
   - `THOUGHTFLOW_AI_CHAT_MODEL`
   - `THOUGHTFLOW_AI_EMBEDDING_MODEL`
   - `THOUGHTFLOW_AI_TIMEOUT_SECONDS`
5. 未配置 AI Key 时使用本地规则 provider，并生成 deterministic local embedding，保证开发环境可运行。
6. URL 笔记正文抓取基础链路，抓取失败会保留原始笔记并发布失败事件。
7. `thought.refine_started`、`thought.refined`、`thought.refine_failed` 事件。
8. refined 结果回写原子 Markdown 的 front matter 和 `AI Notes` 分区。
9. `thought.refined` payload 携带 `EmbeddingRecord`，供 search 写入索引层。
   - SSE 事件流会保留 embedding 元数据但移除 vector，避免向前端推送大向量 payload。
10. `search` 运行单元。
11. `thought.captured` / `thought.refined` 触发后台 index Job。
   - `topic.updated` 触发 workspace reindex，刷新专题过滤视图。
12. `GET /api/search`。
13. `POST /api/system/reindex`。
14. `POST /api/thoughts/{id}/retry-refine`。
15. `search.index_updated`、`search.index_failed`、`search.reindex_started`、`search.reindex_finished` 事件。
16. 索引成功后回写 `index_status: indexed` 并通知 git-sync。
17. DuckDB 搜索实现位于 `internal/pkg/searchdb/store.go`，使用 `duckdb` build tag 启用。
18. 默认构建使用 `internal/pkg/searchdb/store_fallback.go`，用于缺少 DuckDB CGO 链接环境时保持开发和测试可运行。
19. 搜索索引返回 `topics` 字段，并支持 `topic_id` 与 `tags` 过滤。
20. `thought_embeddings` 支持写入 embedding vector、模型、维度和 content hash。
21. `mode=semantic` / `mode=hybrid` 在 query vector 与 thought embedding 存在时计算 `semantic_score`，缺失时 hybrid 降级为关键词分。

验证：

```bash
go test ./...
go build -o /tmp/thoughtflow ./cmd/thoughtflow
CGO_LDFLAGS=-L/tmp go test -tags duckdb ./internal/pkg/searchdb
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
   - semantic 配置字段
5. `thought.refined` / `search.index_updated` 触发后台 topic match Job。
6. 命中专题后通过 topic weave provider 更新专题文档：
   - 配置 AI Key 时使用 OpenAI-compatible chat provider 生成完整 Markdown merge 结果。
   - 未配置或 provider 失败时使用本地 outline-aware fallback，将内容插入匹配的大纲章节。
   - 写入前校验结果必须包含 source link。
7. 命中专题后同步回写原子笔记 `topic_ids`、`topic_status` 与 `Links` 分区。
8. `topic.created`、`topic.matched`、`topic.updated`、`topic.rebuild_started`、`topic.rebuild_failed` 事件。
9. 专题变更触发 `git.commit_requested`，并包含被专题回写的原子笔记路径。
10. 专题 API：
   - `GET /api/topics`
   - `POST /api/topics`
   - `GET /api/topics/{id}`
   - `PUT /api/topics/{id}`
   - `POST /api/topics/{id}/rebuild`
11. 本地 synthesis 草稿 API：`POST /api/synthesis`。
12. synthesis 会读取指定 thoughts，生成本地 Markdown 草稿并返回 source links。
13. M3 topic store、topic service 和 weave provider 单元测试。

验证：

```bash
go test ./...
go build -o /tmp/thoughtflow ./cmd/thoughtflow
```

## 尚未实现

M2：

1. DuckDB FTS 扩展索引。
2. DuckDB 原生向量扩展或 ANN 索引。
3. 当前语义检索是在 Go 内对候选 embedding 做 cosine 计算，不是 DuckDB 向量算子。
4. 混合搜索已有 keyword/semantic/recency 基础加权，但还没有可配置排序策略和 explain 信息。

M3：

1. topic semantic rule 仅保留模型字段，尚未接入 topic 匹配阶段的 embedding 相似度。
2. topic weave 已支持 LLM full-document merge，但尚未实现独立 patch 审批、diff 展示和用户确认流程。
3. 专题成员关系当前随 topic YAML 和 Thought front matter/Links 聚合存储，尚未拆为独立 membership 事实文件。
4. synthesis 当前是本地草稿生成，尚未接入云端模型和持久化审批流程。
5. 前端 UI 尚未实现。

当前限制：

1. HTTP server 通过 `magicEngine.HTTPServer.Run()` 启动，当前框架接口未暴露 graceful shutdown hook。
2. Git commit 依赖本机 Git 用户身份配置；缺失时会通过 `git.commit_failed` 和 Job 失败状态暴露。
3. `.thoughtflow/` 运行时数据只作为本地任务快照，不是长期事实源。

# ThoughtFlow 功能设计文档

> 本文基于 [ThoughtFlow PRD](./thoughtflow-prd.md) 输出功能级设计，用于指导 Go 单二进制应用实现与后续代码收口。当前代码已进入实现收口阶段，具体完成度与剩余限制以 [实现状态](./thoughtflow-implementation-status.md) 为准。

## 1. 设计目标

ThoughtFlow 的功能设计围绕“快速捕捉、异步加工、自动归档、即时检索、可追溯演进”展开。

核心目标：

1. 用户提交文本或 URL 后，HTTP 请求必须秒级返回，后续加工任务全部异步执行。
2. Markdown 文件是知识内容的事实源，DuckDB 只承担索引、查询和可重建分析缓存。
3. Git 自动记录 Markdown 变更，Git 失败不阻断采集主流程。
4. 后端以 `magicCommon/framework` 运行单元拆分职责，HTTP/SSE 由 `magicEngine` 承载。
5. 每个搜索结果、专题内容和 AI 合稿结论都能回溯到原始原子笔记。

非目标：

1. 首版不实现多人协作权限体系。
2. 首版不提供重型数据库服务端依赖。
3. 首版不把 DuckDB 作为不可恢复主存储。
4. 首版不承诺全自动 Git 冲突解决，冲突只检测、记录和提示。

## 2. 总体架构

```text
Browser / Local UI
        |
        v
magicEngine HTTP / SSE
        |
        v
application runtime unit
        |
        +--> capture   --> Markdown thoughts/        --> EventHub
        +--> refiner   --> Web fetch / AI / Embedding --> EventHub
        +--> search    --> DuckDB FTS + vector index  --> EventHub
        +--> topic     --> Topic rules / AI weaving   --> EventHub
        +--> git-sync  --> git add / commit           --> EventHub
```

### 2.1 框架职责边界

`magicCommon/framework` 负责：

1. 应用生命周期：`Setup`、`Run`、`Teardown`。
2. 运行单元显式装配和依赖注入。
3. `event.Hub` 事件发布、订阅和跨单元协同。
4. `task.BackgroundRoutine` 后台任务调度、关闭和排空。
5. 配置、日志、健康检查和监控指标承载。

`magicEngine` 负责：

1. REST API 路由注册。
2. Handler 请求解析、响应格式化和错误映射。
3. SSE 连接管理和事件推送。
4. 静态 UI 资源托管。

Handler 不直接操作 Markdown、DuckDB、Git 或外部 AI API；这些动作必须进入对应运行单元的 `biz` 或 `service` 层。

## 3. 运行单元设计

建议目录结构：

```text
cmd/thoughtflow/
internal/modules/
  application/thoughtflow/
  capture/
  refiner/
  search/
  topic/
  git_sync/
internal/pkg/
  ai/
  config/
  duckdb/
  gitops/
  markdown/
  webfetch/
pkg/
web/
doc/
```

约束：

1. `cmd/thoughtflow` 只保留进程入口、启动参数、配置加载和运行单元显式装配。
2. 每个运行单元可按需要拆分 `module.go`、`biz/`、`service/`、`pkg/common`、`pkg/models`。
3. `application/thoughtflow` 负责 HTTP/SSE 接入和跨运行单元应用服务编排，不持有正式业务状态。
4. `internal/pkg` 只放仓库内部共享技术封装，不放某个运行单元的业务规则。

### 3.1 capture

职责：

1. 接收文本、URL 和未来扩展采集源的录入命令。
2. 生成原子笔记 ID、内容 hash 和初始 YAML front matter。
3. 将原始内容写入 `thoughts/` Markdown 文件。
4. 发布 `thought.captured` 事件。
5. 对重复内容做轻量去重提示，默认不静默丢弃。

输入：

1. `CaptureCommand`
2. 文本内容或 URL
3. 可选用户标题、标签、目标专题提示

输出：

1. `ThoughtSnapshot`
2. 原子 Markdown 文件路径
3. 采集事件

### 3.2 refiner

职责：

1. 订阅 `thought.captured`，提交后台加工任务。
2. 对 URL 类型笔记抓取正文，优先使用本地 fetcher，失败时回退 Jina Reader。
3. 调用 OpenAI 兼容 API 生成摘要、核心观点、标签和 embedding。
4. 将加工结果回写原子 Markdown front matter 和正文附加区。
5. 发布 `thought.refined` 或 `thought.refine_failed` 事件。

约束：

1. AI 调用必须有超时、重试和错误分类。
2. 原始用户输入不得被 AI 结果覆盖，只能追加结构化结果。
3. URL 正文抓取失败时，仍保留 URL 原始笔记并标记失败原因。

### 3.3 search

职责：

1. 订阅 `thought.captured`、`thought.refined`、`topic.updated`。
2. 将 Markdown 元数据和正文索引到 DuckDB。
3. 提供关键词、语义和混合检索。
4. 支持搜索结果过滤、排序、分页和原文预览。
5. 为专题匹配提供候选集合与相似度计算能力。

约束：

1. DuckDB 索引必须可从 Markdown 全量重建。
2. 检索 API 不直接读取 Git 历史，Git 只作为演进记录。
3. Embedding 缺失时混合搜索自动降级为关键词搜索。

### 3.4 topic

职责：

1. 管理专题定义、规则、阈值和大纲。
2. 根据关键词、标签和语义相似度判断新碎片是否命中专题。
3. 命中后触发 AI 智能缝合，更新 `topics/{slug}/index.md`。
4. 支持手动重组专题、重算成员关系和重建主文档。
5. 为 UI 提供专题列表、统计、活动记录和详情视图。

约束：

1. 专题主文档必须保留原子笔记反向链接。
2. AI 缝合失败不得破坏旧版专题文档；写入采用临时文件加原子替换。
3. 自动缝合可配置为自动执行、仅建议或关闭。

### 3.5 git-sync

职责：

1. 订阅 Markdown 变更事件。
2. 将变更路径加入批处理队列。
3. 后台执行 `git add` 和 `git commit`。
4. 发布 `git.commit_succeeded` 或 `git.commit_failed`。
5. 记录冲突、未初始化仓库、用户未配置身份等错误。

约束：

1. Git 操作不阻塞 capture/refiner/topic 主流程。
2. 多个短时间连续变更合并为一次提交，默认 debounce 3 到 10 秒。
3. DuckDB 索引文件默认不进入 Git，除非用户显式配置。

### 3.6 application/thoughtflow

职责：

1. 注册 `magicEngine` REST API、SSE 和静态资源路由。
2. 将 HTTP 请求转换为运行单元命令。
3. 聚合跨运行单元查询结果。
4. 将 EventHub 事件转换为 SSE 消息。
5. 统一错误码、请求 ID、日志字段和响应格式。

约束：

1. 不直接实现 Markdown 写入、AI 调用、DuckDB 查询或 Git 命令。
2. 长耗时请求只返回任务受理状态或当前快照。
3. SSE 只推送必要状态和摘要，不推送完整私密正文，除非用户显式开启。

## 4. 数据与文件设计

### 4.1 工作区目录

默认工作区：

```text
thoughtflow-workspace/
  thoughts/
    2026/
      06/
        20260609-143010-8f3a.md
  topics/
    ai-research/
      index.md
      topic.yaml
      memberships/
        20260609-143010-8f3a.yaml
  attachments/
thoughtflow-data/
  thoughtflow.duckdb
  jobs/
  logs/
```

说明：

1. `thoughts/` 和 `topics/` 是用户知识资产，默认进入 Git。
2. `thoughtflow-data/` 是运行时数据目录，默认不进入 Git。
3. 配置目录与数据目录分离，默认使用 OS 用户配置目录下的 `thoughtflow/application.toml`，运行态数据目录由 `workspace.data_dir` 明确定义。
4. `attachments/` 存放未来上传文件、网页快照或图片资源。
5. Markdown 路径需要兼容 Obsidian 和 Logseq，避免依赖数据库才能阅读。

### 4.2 原子笔记 Markdown

示例：

```markdown
---
id: 20260609-143010-8f3a
type: url
status: refined
title: "Example Article"
url: "https://example.com/article"
source: "manual"
created_at: "2026-06-09T14:30:10+08:00"
updated_at: "2026-06-09T14:31:22+08:00"
content_hash: "sha256:..."
tags:
  - ai
  - knowledge-management
topics:
  - ai-research
summary: "..."
key_points:
  - "..."
embedding_ref: "duckdb:thought_embeddings/20260609-143010-8f3a"
errors: []
---

## Original

用户原始输入或 URL。

## Extracted Content

网页正文或清洗后的正文。

## AI Notes

摘要、观点、待确认内容。
```

约束：

1. `Original` 区域只追加，不由 AI 自动改写。
2. `Extracted Content` 可以重抓取更新，但需更新 `updated_at` 和 Git 记录。
3. `AI Notes` 标记为派生内容，可被重新生成。
4. Front matter 字段新增必须向后兼容，未知字段保留。

### 4.3 专题文件

`topics/{slug}/topic.yaml`：

```yaml
id: ai-research
name: AI 研究
slug: ai-research
description: 跟踪 AI 模型、工具链和知识工程实践
rules:
  keywords:
    any:
      - AI
      - LLM
      - embedding
    all: []
    exclude: []
  tags:
    any:
      - ai
  semantic:
    enabled: true
    threshold: 0.78
outline:
  - title: 背景与趋势
  - title: 工程实践
  - title: 待验证想法
auto_weave: true
created_at: "2026-06-09T14:00:00+08:00"
updated_at: "2026-06-09T14:00:00+08:00"
```

`topics/{slug}/index.md`：

```markdown
---
id: ai-research
type: topic
updated_at: "2026-06-09T14:45:00+08:00"
members:
  - 20260609-143010-8f3a
---

# AI 研究

## 背景与趋势

整理后的专题内容。

> Sources: [[../../thoughts/2026/06/20260609-143010-8f3a]]
```

`topics/{slug}/memberships/{thought_id}.yaml`：

```yaml
topic_id: ai-research
thought_id: 20260609-143010-8f3a
match_type: semantic
score: 0.82
reasons:
  - semantic:0.820
status: accepted
created_at: "2026-06-09T14:10:00Z"
updated_at: "2026-06-09T14:10:00Z"
```

约束：

1. `topic.yaml` 保存专题定义和统计快照，不作为成员关系唯一事实源。
2. `index.md` 保存可读专题主文档和成员快照，不反推命中分数、原因或状态。
3. `memberships/` 保存 Thought 与 Topic 的关系事实，进入 Git，可由规则重建并可承载后续审批状态。
4. `approvals/` 保存 topic weave 审批队列和历史，记录候选文档、diff、状态、确认时间和最终确认文档。

## 5. DuckDB 索引设计

DuckDB 是可重建索引层，建议表：

| 表 | 用途 |
| --- | --- |
| `thoughts` | 原子笔记元数据、路径、状态、时间、hash |
| `thought_contents` | 清洗正文、摘要、标签文本和可搜索文本 |
| `thought_embeddings` | embedding 向量、模型、维度、更新时间 |
| `topics` | 专题配置快照和统计字段 |
| `topic_memberships` | 专题与原子笔记关系、命中分数、命中原因 |
| `jobs` | 后台任务快照，供 UI 查询 |
| `event_offsets` | SSE 或内部事件消费位点，便于断线恢复 |

索引策略：

1. `thought_contents.search_text` 建 FTS 索引。
2. embedding 表按模型和维度区分，避免模型切换后混算。
3. 混合搜索分数采用 `keyword_score`、`semantic_score` 和 `recency_score` 加权。
4. 当 Markdown 与 DuckDB 不一致时，以 Markdown 为准重建索引。

## 6. 事件设计

事件 payload 使用明确结构体，不使用任意 `map[string]any` 作为主协议。

| 事件 ID | 来源 | 消费方 | 说明 |
| --- | --- | --- | --- |
| `thought.captured` | capture | refiner, search, git-sync, application | 原子笔记已创建 |
| `thought.refine_started` | refiner | application | 加工开始 |
| `thought.refined` | refiner | search, topic, git-sync, application | 加工完成 |
| `thought.refine_failed` | refiner | application, git-sync | 加工失败，保留原文 |
| `search.index_updated` | search | topic, application | 索引更新完成 |
| `search.index_failed` | search | application | 索引失败 |
| `topic.matched` | topic | application | 碎片命中专题 |
| `topic.updated` | topic | search, git-sync, application | 专题文档更新 |
| `topic.rebuild_failed` | topic | application | 专题重组失败 |
| `git.commit_requested` | capture/refiner/topic | git-sync | 请求提交变更 |
| `git.commit_succeeded` | git-sync | application | Git 提交完成 |
| `git.commit_failed` | git-sync | application | Git 提交失败 |

推荐事件公共字段：

```text
event_id
event_type
source_unit
occurred_at
trace_id
workspace_id
resource_type
resource_id
payload_version
payload
```

## 7. 核心业务流程

### 7.1 文本采集

1. UI 调用 `POST /api/thoughts`，请求体包含 `type=text` 和 `content`。
2. application 校验请求并调用 capture。
3. capture 写入 `thoughts/YYYY/MM/{id}.md`，状态为 `captured`。
4. capture 发布 `thought.captured`，HTTP 返回 `202 Accepted` 和笔记快照。
5. refiner 后台生成摘要、标签和 embedding，完成后发布 `thought.refined`。
6. search 更新 DuckDB 索引。
7. topic 判断命中并按配置更新专题。
8. git-sync 批量提交 Markdown 变更。
9. application 通过 SSE 推送每个阶段状态。

### 7.2 URL 采集

1. capture 先记录 URL 原始笔记，保证用户输入不丢失。
2. refiner 后台抓取网页正文。
3. 抓取成功后生成摘要、标签和 embedding。
4. 抓取失败时写入错误字段，并发布失败事件。
5. 用户可在 UI 中重试抓取或手动补正文。

### 7.3 专题自动缝合

1. topic 收到 `thought.refined` 或 `search.index_updated`。
2. 根据专题规则计算候选命中：
   - 关键词命中
   - 标签命中
   - 语义相似度命中
3. 命中后生成 `TopicWeaveJob`。
4. AI 根据专题大纲、当前 `index.md` 和新碎片内容生成 patch 建议。
5. topic 校验 patch 必须保留 source link。
6. 写入临时文件并原子替换 `topics/{slug}/index.md`。
7. 发布 `topic.updated`，触发 search 和 git-sync。

### 7.4 即席检索与合稿

1. UI 通过 `GET /api/search` 发起关键词、语义或混合查询。
2. search 返回结果、命中片段、分数、来源链接和预览。
3. 用户勾选多个结果后调用 `POST /api/synthesis`。
4. application 将选中 ID 交给 refiner 或独立 synthesis service。
5. AI 生成总结报告或逻辑大纲。
6. 合稿结果默认是临时视图；用户点击保存后才写入 Markdown。

## 8. API 设计

统一响应：

```json
{
  "request_id": "req_...",
  "data": {},
  "error": null
}
```

统一错误：

```json
{
  "request_id": "req_...",
  "data": null,
  "error": {
    "code": "thoughtflow.capture.invalid_request",
    "message": "content is required",
    "details": {}
  }
}
```

### 8.1 Thoughts

`POST /api/thoughts`

请求：

```json
{
  "type": "text",
  "content": "一个零散想法",
  "url": "",
  "title": "",
  "tags": ["idea"],
  "topic_hints": ["ai-research"]
}
```

响应：`202 Accepted`

```json
{
  "thought": {
    "id": "20260609-143010-8f3a",
    "status": "captured",
    "path": "thoughts/2026/06/20260609-143010-8f3a.md"
  },
  "jobs": [
    {
      "id": "job_refine_...",
      "type": "refine",
      "status": "queued"
    }
  ]
}
```

`GET /api/thoughts/{id}`

返回原子笔记元数据、正文分区、关联专题、Git commit 摘要和任务状态。

`POST /api/thoughts/{id}/retry-refine`

重试 URL 抓取、AI 摘要和 embedding。

### 8.2 Search

`GET /api/search?q={query}&mode=hybrid&page=1&page_size=20`

参数：

| 参数 | 说明 |
| --- | --- |
| `q` | 查询文本 |
| `mode` | `keyword`、`semantic`、`hybrid` |
| `sort` | 可选，`score`、`keyword`、`semantic`、`recency`，默认 `score` |
| `topic_id` | 可选专题过滤 |
| `tags` | 可选标签过滤 |
| `from` / `to` | 时间范围 |
| `explain` | 可选，`true` 时返回分数组件、权重和检索来源 |
| `keyword_weight` / `semantic_weight` / `recency_weight` | 可选，混合排序权重；传入任一正值时归一化后覆盖默认权重 |

返回：

```json
{
  "items": [
    {
      "thought_id": "20260609-143010-8f3a",
      "title": "Example Article",
      "snippet": "...",
      "score": 0.91,
      "keyword_score": 0.72,
      "semantic_score": 0.88,
      "recency_score": 0.67,
      "path": "thoughts/2026/06/20260609-143010-8f3a.md",
      "topics": ["ai-research"],
      "explain": {
        "mode": "hybrid",
        "sort": "score",
        "score_formula": "score = keyword_score*0.45 + semantic_score*0.45 + recency_score*0.1",
        "weights": {"keyword": 0.45, "semantic": 0.45, "recency": 0.1},
        "components": {"keyword": 0.72, "semantic": 0.88, "recency": 0.67},
        "keyword_source": "duckdb_fts",
        "semantic_source": "duckdb_hnsw"
      }
    }
  ],
  "page": 1,
  "page_size": 20,
  "total": 1
}
```

`semantic_source` 可能为 `duckdb_hnsw`、`duckdb_array`、`json_cosine` 或默认 fallback store 的 `memory_cosine`；VSS/HNSW 不可用时自动降级。

`POST /api/synthesis`

请求：

```json
{
  "thought_ids": ["20260609-143010-8f3a"],
  "goal": "生成一份研究大纲",
  "format": "outline"
}
```

返回 synthesis 草稿，并写入 `synthesis/drafts/{draft_id}.yaml`，状态为 `draft`。配置 AI API key 时使用 OpenAI-compatible chat model 生成 Markdown 草稿；未配置时使用本地规则合稿。

`GET /api/synthesis`

返回本地 synthesis 草稿仓库，按更新时间倒序排列。

`GET /api/synthesis/{draft_id}`

返回单个 synthesis 草稿，包括输入 Thought、来源链接、当前内容、状态和保存历史。

`POST /api/synthesis/save`

请求：

```json
{
  "draft_id": "job-synthesis-xxxx",
  "thought_ids": ["20260609-143010-8f3a"],
  "goal": "生成一份研究大纲",
  "format": "outline",
  "content": "# 研究大纲\n\n..."
}
```

说明：

1. 保存动作通过 capture 运行单元创建新的 Thought。
2. 新 Thought 的 `source` 标记为 `synthesis`。
3. 保存内容会保留来源 Thought 的 Markdown 链接。
4. 当请求包含 `draft_id` 时，会将 `synthesis/drafts/{draft_id}.yaml` 标记为 `saved`，记录保存时间和生成的 Thought ID。

### 8.3 Topics

`GET /api/topics`

返回专题卡片、统计数据和最近活跃时间。

`POST /api/topics`

创建专题、规则和初始大纲。

`GET /api/topics/{id}`

返回专题配置、`index.md` 渲染内容、成员列表和活动记录。

`PUT /api/topics/{id}`

更新名称、规则、阈值、大纲和自动缝合策略。

`POST /api/topics/{id}/rebuild`

手动重建专题主文档，返回后台任务 ID。

`POST /api/topics/{id}/weave-preview`

请求：

```json
{
  "thought_id": "20260609-143010-8f3a"
}
```

返回当前 `index.md`、候选新版文档、source link、membership、逐行 diff、结构化 patch 和 proposal ID。该接口不写入专题主文档，但会将 pending proposal 写入 `topics/{slug}/approvals/{proposal_id}.yaml`。

`GET /api/topics/{id}/weave-proposals`

返回该专题的 weave proposal 队列和审批历史，按创建时间倒序排列。

`GET /api/topics/{id}/weave-proposals/{proposal_id}`

返回单个 weave proposal 的候选文档、diff、结构化 patch、状态和确认记录。

`POST /api/topics/{id}/weave-accept`

请求：

```json
{
  "proposal_id": "job-topic-weave-proposal-xxxx",
  "thought_id": "20260609-143010-8f3a",
  "document": "# AI 研究\n\n..."
}
```

确认用户审阅后的候选文档。当请求未修改候选文档时，服务端按 proposal 内的结构化 patch 校验 base hash、上下文行和 proposed hash 后原子写入 `topics/{slug}/index.md`；当前文档已经变化时返回冲突错误。用户编辑过候选文档时，服务端按完整文档写入路径处理，仍校验 source link。确认成功后同步 membership 事实文件、Thought backlink，并将对应 proposal 标记为 `accepted`。

### 8.4 Events

`GET /api/events`

SSE message 示例：

```text
event: thought.refined
id: evt_20260609_000001
data: {"thought_id":"20260609-143010-8f3a","status":"refined"}
```

支持：

1. `Last-Event-ID` 断线续传。
2. `types` 查询参数过滤事件类型。
3. 心跳事件，默认 15 到 30 秒一次。
4. 前端断线重连后查询 `/api/jobs/{id}` 补齐当前状态。

### 8.5 Jobs 与 System

`GET /api/jobs/{id}`：查询后台任务快照。

`GET /api/system/status`：查询工作区、AI、DuckDB、Git 和索引状态。

`GET /api/system/metrics`：查询 JSON 格式运行指标快照。

`GET /metrics`：查询 Prometheus text exposition 格式运行指标。

`POST /api/system/reindex`：从 Markdown 全量重建 DuckDB 索引。

## 9. UI 功能设计

### 9.1 全局布局

首屏即为可用工作台，不做营销页：

1. 顶部：快速捕捉输入框、URL 粘贴、`Ctrl+K` 搜索入口。
2. 左侧：专题导航和最近活动。
3. 主区域：根据当前路由展示专题大盘、搜索中心或专题详情。
4. 右侧或抽屉：任务进度、笔记预览、规则编辑。

### 9.2 专题管理大盘

展示：

1. 专题卡片：名称、描述、碎片数、字数、最近更新时间。
2. 异常状态：缝合失败、索引落后、Git 提交失败。
3. 创建专题入口：名称、描述、关键词、标签、语义阈值。

交互：

1. 点击专题进入工作台。
2. 支持按活跃度、碎片数、更新时间排序。
3. 支持禁用或启用自动缝合。

### 9.3 智能检索中心

展示：

1. 搜索输入与模式切换：关键词、语义、混合。
2. 结果列表：标题、摘要、命中片段、分数、标签、所属专题。
3. 预览面板：Markdown 渲染、front matter、原始链接。
4. 批量勾选区：即时合稿入口。

交互：

1. `Ctrl+K` 聚焦搜索。
2. 实时过滤标签和专题。
3. 选择多个结果后生成总结或大纲。
4. 合稿结果可复制、保存为新笔记或加入专题草稿。

### 9.4 专题详情工作台

左侧：

1. `topics/{slug}/index.md` 渲染预览。
2. 章节目录。
3. source link 快速跳转原子笔记。

右侧：

1. 专题规则编辑。
2. 大纲维护。
3. 成员碎片列表。
4. 手动重组按钮。
5. 最近缝合记录和失败原因。

## 10. AI 设计

### 10.1 Provider 抽象

内部使用 OpenAI 兼容接口抽象：

1. `Chat(ctx, request)`: 摘要、标签、合稿、专题缝合。
2. `Embed(ctx, request)`: embedding 生成。
3. `ModelInfo(ctx)`: 查询模型、维度和能力。

配置项：

```toml
[ai]
base_url = "https://api.example.com/v1"
api_key = ""
chat_model = "deepseek-chat"
embedding_model = "text-embedding-3-small"
timeout_seconds = 60
```

### 10.2 Prompt 任务

| 任务 | 输入 | 输出 |
| --- | --- | --- |
| `summarize_thought` | 原文、网页正文、标题、URL | 摘要、核心观点、标签 |
| `classify_topic` | 原子笔记、专题规则、相似候选 | 命中建议和原因 |
| `weave_topic` | 专题大纲、旧 index、新碎片 | patch 或完整新版专题文档 |
| `synthesize_selection` | 选中碎片、用户目标 | 总结报告或逻辑大纲 |

约束：

1. AI 输出必须经过结构化解析和校验。
2. 专题缝合必须校验 source link 存在。
3. 所有 AI 派生内容保留模型名、生成时间和输入版本 hash。
4. 用户可配置关闭云端 AI，此时保留手动整理和关键词搜索能力。

## 11. 配置设计

配置分层：

1. 默认配置：内置在二进制中。
2. 独立配置目录：`<config-dir>/application.toml`，直接复用 `magicCommon/framework/configuration` 的 `application.toml` 机制。
3. 启动参数 `--config-dir` 仅用于定位配置目录，不覆盖业务配置。

业务配置只读取 `application.toml` 和内置默认值。虽然 magicCommon framework/configuration 内部支持环境变量合并，ThoughtFlow 读取配置时使用 framework 导出的原始 application 配置，避免环境变量改变运行时行为。

目录约束：

1. 数据目录由 `workspace.data_dir` 定义。
2. `config-dir` 与 `workspace.data_dir` 不能相等。
3. `config-dir` 与 `workspace.data_dir` 不能互相嵌套。

关键配置：

```toml
[server]
host = "127.0.0.1"
port = 8080

[workspace]
root = "./thoughtflow-workspace"
data_dir = "./thoughtflow-data"
auto_init_git = true

[capture]
duplicate_policy = "warn"

[refiner]
concurrency = 2
url_fetch_timeout_seconds = 30

[search]
duckdb_path = "thoughtflow.duckdb"
default_mode = "hybrid"

[topic]
auto_weave = true
min_semantic_score = 0.78

[git_sync]
enabled = true
debounce_seconds = 5

[events]
sse_heartbeat_seconds = 20

[ai]
base_url = "https://api.openai.com"
api_key = ""
chat_model = "gpt-4o-mini"
embedding_model = "text-embedding-3-small"
timeout_seconds = 30
```

## 12. 错误与状态设计

### 12.1 Thought 状态

```text
captured -> refining -> refined
captured -> refining -> refine_failed
refined -> indexed
indexed -> topic_matched -> topic_updated
```

状态不强制单字段表达全部流程；原子笔记可保留主状态，后台任务和事件记录表达细分状态。

### 12.2 Job 状态

```text
queued -> running -> succeeded
queued -> running -> failed
queued -> canceled
running -> retrying -> running
```

任务必须记录：

1. job ID
2. 类型
3. 关联资源 ID
4. 当前状态
5. 错误码
6. 重试次数
7. 创建、开始、结束时间

### 12.3 错误码

错误码前缀：

| 前缀 | 说明 |
| --- | --- |
| `thoughtflow.capture.*` | 采集错误 |
| `thoughtflow.refiner.*` | 网页抓取、AI、embedding 错误 |
| `thoughtflow.search.*` | DuckDB、FTS、向量检索错误 |
| `thoughtflow.topic.*` | 规则、缝合、重组错误 |
| `thoughtflow.git.*` | Git 初始化、提交、冲突错误 |
| `thoughtflow.system.*` | 配置、工作区、运行时错误 |

## 13. 安全与隐私

1. 默认只监听 `127.0.0.1`。
2. API key 只从 `application.toml` 读取，不写入 Markdown。
3. 发送到 AI Provider 的内容限定为当前任务所需片段。
4. UI 明示哪些任务会调用云端 AI。
5. Git remote push 默认关闭，首版只做本地 commit。
6. 日志不输出完整正文、API key 或 Authorization 头。

## 14. 可观测性

日志字段：

1. `trace_id`
2. `unit`
3. `event_type`
4. `thought_id`
5. `topic_id`
6. `job_id`
7. `duration_ms`
8. `error_code`

指标：

| 指标 | 说明 |
| --- | --- |
| `thoughtflow_capture_total` | 采集总数 |
| `thoughtflow_refine_duration_seconds` | 加工耗时 |
| `thoughtflow_ai_request_total` | AI 请求数 |
| `thoughtflow_search_query_total` | 搜索请求数 |
| `thoughtflow_index_lag_seconds` | 索引延迟 |
| `thoughtflow_topic_weave_total` | 专题缝合次数 |
| `thoughtflow_git_commit_total` | Git 提交次数 |
| `thoughtflow_background_jobs` | 后台任务数量 |

健康检查：

1. live：进程可响应。
2. ready：工作区可写、DuckDB 可连接、EventHub 可发布、BackgroundRoutine 可接受任务。
3. degraded：AI 或 Git 不可用，但本地采集和阅读可继续。

## 15. 测试与验收

### 15.1 单元测试

1. Markdown front matter 读写和未知字段保留。
2. ID、slug、路径生成。
3. URL 类型判断和内容 hash。
4. 主题规则匹配。
5. 混合搜索分数归一化。
6. Git debounce 队列。
7. 原生前端组件测试：Markdown CommonMark/GFM 安全渲染、Obsidian 双链、diff 展示、synthesis source link 去重和 outline helper。

### 15.2 集成测试

1. 文本采集到 Markdown 落盘。
2. URL 抓取失败时保留原始笔记。
3. `thought.captured` 驱动 refiner、search、git-sync。
4. DuckDB 从 Markdown 全量重建。
5. 专题重组失败不破坏旧 `index.md`。
6. SSE 能收到任务状态并支持断线后查询补偿。
7. 嵌入式前端资源通过 JS syntax gate、Node 组件测试和 Chrome desktop/mobile browser smoke 矩阵。

### 15.3 验收标准

MVP 完成标准：

1. 可以启动单二进制本地服务。
2. 可以提交文本和 URL，并生成 Markdown 原子笔记。
3. 可以异步生成摘要、标签和 embedding。
4. 可以进行关键词搜索和混合搜索，embedding 不可用时自动降级。
5. 可以创建专题并按规则自动收录碎片。
6. 可以查看专题大盘、搜索中心和专题详情工作台。
7. 可以自动 Git commit Markdown 变更，并在失败时通过 UI 看到原因。
8. 可以从搜索结果和专题内容回跳原子笔记。

## 16. 里程碑建议

### M1 本地采集闭环

1. 单二进制启动。
2. 工作区初始化。
3. 文本/URL 原始 Markdown 落盘。
4. 事件流和 SSE 基础能力。
5. Git 本地自动 commit。

### M2 AI 加工与索引

1. URL 正文抓取。
2. AI 摘要、标签、embedding。
3. DuckDB FTS 与索引重建。
4. 搜索中心基础 UI。

### M3 专题系统

1. 专题规则管理。
2. 自动命中判断。
3. AI 智能缝合。
4. 专题详情工作台。

### M4 体验与可靠性

1. 即时合稿。
2. 任务重试和失败恢复。
3. Git 状态可视化。
4. 性能、内存和隐私审计。

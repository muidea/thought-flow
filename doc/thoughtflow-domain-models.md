# ThoughtFlow 业务模型与功能定义

> 本文用于代码开发前统一业务对象、归属运行单元、状态流转、功能命令和事件边界。它补充 [功能设计文档](./thoughtflow-functional-design.md)，作为后续接口、目录、service/biz 分层和测试用例的实现合同。

## 1. 模型分层原则

ThoughtFlow 使用本地 Markdown 作为知识资产事实源，DuckDB 和事件/任务记录作为可重建的运行态视图。

模型分为四类：

| 类型 | 说明 | 事实源 |
| --- | --- | --- |
| 知识资产模型 | 用户真正拥有和长期保留的内容 | `thoughts/`、`topics/` Markdown/YAML |
| 派生分析模型 | AI、索引、匹配、合稿等可再生成结果 | Markdown 派生区 + DuckDB |
| 运行任务模型 | 后台任务、进度、错误和重试 | `runtime.state_dir/jobs` + DuckDB 可选缓存 |
| 系统运维模型 | Git、配置、健康检查、事件游标 | `runtime.state_dir` + Git 仓库状态 |

实现约束：

1. Markdown 优先：用户内容、专题文档和关键派生摘要必须可脱离服务阅读。
2. DuckDB 可重建：索引表、搜索结果、命中分数、统计字段不得成为唯一事实源。
3. 事件不替代事实：EventHub 用于协同和通知，不作为长期业务存储。
4. 分字段归属：同一个 Markdown 文件可包含多个运行单元写入的字段，但每个字段必须有清晰 owner。
5. 对外可有聚合状态，对内必须保留任务级状态，避免单个 `status` 承载全部流程。

## 2. 业务模型总览

| 模型 | 业务含义 | 主归属运行单元 | 主要存储 |
| --- | --- | --- | --- |
| `Workspace` | 本地知识库工作区 | application | 配置 + 目录 |
| `Thought` | 原子笔记，最小知识碎片 | capture | `thoughts/{yyyy}/{mm}/{id}.md` |
| `ThoughtContent` | 原文、抓取正文、AI 笔记分区 | capture/refiner | Thought Markdown body |
| `ThoughtRefinement` | 摘要、标签、核心观点、错误 | refiner | Thought front matter + AI Notes |
| `EmbeddingRecord` | 语义向量及模型信息 | refiner/search | DuckDB |
| `Topic` | 专题定义和专题主文档 | topic | `topics/{slug}/topic.yaml` + `index.md` |
| `TopicRule` | 专题匹配规则 | topic | `topic.yaml` |
| `TopicOutline` | 专题大纲 | topic | `topic.yaml` + `index.md` |
| `TopicMembership` | 碎片与专题的关系 | topic/search | `topics/{slug}/memberships/{thought_id}.yaml` + DuckDB 缓存 |
| `TopicWeaveProposal` | 专题文档候选变更和审批历史 | topic | `topics/{slug}/approvals/{proposal_id}.yaml` |
| `SearchIndex` | 关键词和语义索引 | search | DuckDB |
| `SearchResult` | 检索结果视图 | search | 运行态响应 |
| `SynthesisDraft` | 即席合稿草稿和保存历史 | refiner/application | `synthesis/drafts/{draft_id}.yaml`，保存后转 Thought |
| `Job` | 后台任务及状态 | 所属任务运行单元 | `runtime.state_dir/jobs` + DuckDB |
| `DomainEvent` | 运行单元协同事件 | application/EventHub | EventHub + 可选 offset |
| `GitChangeSet` | 待提交文件集合 | git-sync | 运行态队列 |
| `GitCommitRecord` | 自动提交结果 | git-sync | Git history + event/job |
| `SystemStatus` | 工作区、AI、DuckDB、Git 健康状态 | application | 运行态响应 |

首批开发必须落地：`Workspace`、`Thought`、`ThoughtContent`、`Job`、`DomainEvent`、`GitChangeSet`、`GitCommitRecord`。

第二批落地：`ThoughtRefinement`、`EmbeddingRecord`、`SearchIndex`、`SearchResult`。

第三批落地：`Topic`、`TopicRule`、`TopicOutline`、`TopicMembership`、`SynthesisDraft`。

## 3. 知识资产模型

### 3.1 Workspace

`Workspace` 表示一个本地 ThoughtFlow 知识库。

字段：

| 字段 | 类型 | 说明 | Owner |
| --- | --- | --- | --- |
| `id` | string | 本地工作区 ID，单用户默认 `local` | application |
| `root_path` | string | 工作区根目录 | application |
| `thoughts_path` | string | 原子笔记目录 | application |
| `topics_path` | string | 专题目录 | application |
| `runtime_path` | string | `runtime.state_dir` 运行态目录 | application |
| `git_enabled` | bool | 是否启用 Git 自动提交 | git-sync |
| `created_at` | time | 初始化时间 | application |

功能定义：

1. 初始化工作区目录。
2. 校验 `thoughts/`、`topics/`、`runtime.state_dir` 可读写。
3. 按配置初始化 Git 仓库。
4. 提供系统状态查询基础信息。

不做：

1. 不表达多用户租户。
2. 不把多个 workspace 混在一个进程内做强隔离，首版只按单本地库处理。

### 3.2 Thought

`Thought` 是最小知识碎片，每条用户输入对应一个原子 Markdown 文件。

字段：

| 字段 | 类型 | 必填 | 说明 | Owner |
| --- | --- | --- | --- | --- |
| `id` | string | 是 | 时间 + 短 hash 生成的稳定 ID | capture |
| `type` | enum | 是 | `text`、`url`，后续可扩展 `file` | capture |
| `source` | enum | 是 | `manual`、`api`、`synthesis`，后续可扩展 `browser` | capture |
| `user_title` | string | 否 | 用户显式标题 | capture |
| `extracted_title` | string | 否 | 网页标题或 AI 建议标题 | refiner |
| `display_title` | string | 否 | 对外展示标题，由 `user_title` 优先，否则取 `extracted_title` 或内容摘要 | 查询视图 |
| `url` | string | URL 类型必填 | 原始 URL | capture |
| `path` | string | 是 | 相对工作区 Markdown 路径 | capture |
| `created_at` | time | 是 | 采集时间 | capture |
| `updated_at` | time | 是 | 最近一次文件内容更新时间 | 写入方 |
| `content_hash` | string | 是 | 原始输入 hash，用于去重提示 | capture |
| `user_tags` | []string | 否 | 用户显式标签 | capture |
| `ai_tags` | []string | 否 | AI 标签 | refiner |
| `topic_ids` | []string | 否 | 已关联专题摘要 | topic |
| `summary` | string | 否 | AI 摘要 | refiner |
| `key_points` | []string | 否 | AI 核心观点 | refiner |
| `errors` | []ErrorRef | 否 | 派生任务错误摘要 | 对应运行单元 |

状态定义：

| 状态视图 | 说明 |
| --- | --- |
| `capture_status` | `captured`、`duplicate_warned`、`capture_failed` |
| `refine_status` | `pending`、`running`、`refined`、`failed`、`disabled` |
| `index_status` | `pending`、`indexed`、`failed` |
| `topic_status` | `unmatched`、`matched`、`updated`、`failed` |
| `display_status` | UI 聚合状态，由 jobs/events 计算，不作为唯一业务事实 |

功能定义：

1. 创建文本原子笔记。
2. 创建 URL 原子笔记。
3. 查询原子笔记元数据和正文分区。
4. 重试加工。
5. 从搜索结果或专题内容回跳原子笔记。

不做：

1. 不在 Thought 内直接保存不可读的二进制向量。
2. 不让 AI 改写 `Original` 分区。
3. 不把专题主文档内容嵌入 Thought。

### 3.3 ThoughtContent

`ThoughtContent` 是 Thought Markdown body 的结构化分区。

分区：

| 分区 | 说明 | Owner | 可重写 |
| --- | --- | --- | --- |
| `Original` | 用户原始输入或 URL | capture | 否，只追加纠错记录 |
| `Extracted Content` | URL 抓取正文或清洗内容 | refiner | 是，重抓取可更新 |
| `AI Notes` | 摘要、观点、标签说明 | refiner | 是，可重新生成 |
| `Links` | 关联专题、来源、反向链接 | topic/search | 是 |

功能定义：

1. 从 Markdown 解析分区。
2. 保留未知分区。
3. 对 owned 分区做幂等更新。
4. 写入时保持 front matter 和正文格式稳定。

### 3.4 Topic

`Topic` 表示一个可自动增长的专题知识体系。

字段：

| 字段 | 类型 | 必填 | 说明 | Owner |
| --- | --- | --- | --- | --- |
| `id` | string | 是 | 专题 ID，默认等于 slug | topic |
| `name` | string | 是 | 展示名 | topic |
| `slug` | string | 是 | 路径安全名称 | topic |
| `description` | string | 否 | 专题说明 | topic |
| `rules` | TopicRule | 是 | 匹配规则 | topic |
| `outline` | []OutlineNode | 否 | 章节大纲 | topic |
| `auto_weave` | bool | 是 | 是否自动缝合 | topic |
| `member_count` | int | 否 | 统计视图，可重建 | search/topic |
| `word_count` | int | 否 | 统计视图，可重建 | search/topic |
| `last_active_at` | time | 否 | 最近更新 | topic |
| `created_at` | time | 是 | 创建时间 | topic |
| `updated_at` | time | 是 | 更新时间 | topic |

文件：

1. `topics/{slug}/topic.yaml` 保存规则、大纲和配置。
2. `topics/{slug}/index.md` 保存可读专题主文档和成员快照。
3. `topics/{slug}/memberships/{thought_id}.yaml` 保存成员关系事实。

功能定义：

1. 创建专题。
2. 更新专题规则。
3. 更新专题大纲。
4. 自动接收命中碎片。
5. 手动触发专题重组。
6. 查询专题大盘卡片。
7. 查询专题详情工作台数据。

不做：

1. 不把专题成员唯一事实只存在 DuckDB。
2. 不在 AI 缝合失败时覆盖旧 `index.md`。

### 3.5 TopicRule

`TopicRule` 定义专题命中条件。

字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `keywords.any` | []string | 命中任一关键词即可加分 |
| `keywords.all` | []string | 必须全部命中才通过 |
| `keywords.exclude` | []string | 命中后排除 |
| `tags.any` | []string | 用户标签或 AI 标签命中 |
| `semantic.enabled` | bool | 是否启用语义匹配 |
| `semantic.threshold` | float | 语义相似度阈值 |
| `manual_include` | []string | 用户手动固定包含 Thought ID |
| `manual_exclude` | []string | 用户手动排除 Thought ID |

功能定义：

1. 对单条 Thought 计算是否命中。
2. 返回命中原因和各项分数。
3. 支持规则变更后全量重算。

### 3.6 TopicMembership

`TopicMembership` 表示 Thought 与 Topic 的关系。

字段：

| 字段 | 类型 | 说明 | Owner |
| --- | --- | --- | --- |
| `topic_id` | string | 专题 ID | topic |
| `thought_id` | string | 原子笔记 ID | topic |
| `match_type` | enum | `keyword`、`tag`、`semantic`、`manual` | topic/search |
| `score` | float | 综合命中分数 | topic/search |
| `reasons` | []string | 命中原因 | topic |
| `status` | enum | `suggested`、`accepted`、`rejected`、`woven` | topic |
| `created_at` | time | 首次命中时间 | topic |
| `updated_at` | time | 最近更新时间 | topic |

功能定义：

1. 记录自动命中建议。
2. 支持用户接受或排除。
3. 支持专题重组后重算。
4. 为专题文档 source link 提供依据。
5. 自动命中关系可从规则、Thought 和索引重建；用户手动接受、排除、固定包含必须写入 membership 事实文件，专题规则仍写入 `topic.yaml`。

## 4. 派生分析模型

### 4.1 ThoughtRefinement

`ThoughtRefinement` 是 refiner 对 Thought 的加工结果。

字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `thought_id` | string | 关联 Thought |
| `status` | enum | `pending`、`running`、`refined`、`failed` |
| `extracted_title` | string | 网页标题或 AI 建议标题 |
| `extracted_content_hash` | string | 抓取正文 hash |
| `summary` | string | 摘要 |
| `key_points` | []string | 核心观点 |
| `ai_tags` | []string | AI 标签 |
| `model` | string | 使用的 chat model |
| `input_hash` | string | 输入版本 |
| `generated_at` | time | 生成时间 |
| `error` | ErrorRef | 最近错误 |

功能定义：

1. URL 正文抓取。
2. AI 摘要生成。
3. 标签生成。
4. 结构化写回 Thought。
5. 失败后可重试。

### 4.2 EmbeddingRecord

`EmbeddingRecord` 是 Thought 的语义向量。

字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `thought_id` | string | 关联 Thought |
| `model` | string | embedding 模型 |
| `dimension` | int | 向量维度 |
| `vector` | []float | 向量，存 DuckDB |
| `content_hash` | string | 向量对应的输入 hash |
| `created_at` | time | 生成时间 |

功能定义：

1. 生成 embedding。
2. 按模型和维度隔离索引。
3. 内容变化后重新生成。
4. 缺失时搜索降级。

### 4.3 SearchIndex

`SearchIndex` 是 DuckDB 内部索引视图，不直接暴露为用户资产。

字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `thought_id` | string | 关联 Thought |
| `path` | string | Markdown 路径 |
| `title` | string | 标题 |
| `search_text` | string | 用于 FTS 的拼接文本 |
| `tags` | []string | 用户标签和 AI 标签 |
| `topics` | []string | 关联专题 |
| `updated_at` | time | 索引更新时间 |

功能定义：

1. 单条增量索引。
2. 全量重建索引。
3. 检测 Markdown 与索引是否不一致。

### 4.4 SearchResult

`SearchResult` 是查询响应视图。

字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `thought_id` | string | 原子笔记 ID |
| `title` | string | 标题 |
| `snippet` | string | 命中片段 |
| `score` | float | 综合分数 |
| `keyword_score` | float | 关键词分 |
| `semantic_score` | float | 语义分 |
| `recency_score` | float | 时间分 |
| `path` | string | Markdown 路径 |
| `topics` | []string | 专题 |
| `tags` | []string | 标签 |

功能定义：

1. 关键词搜索。
2. 语义搜索。
3. 混合搜索。
4. 分页、过滤、排序。
5. 提供预览和回跳路径。

### 4.5 TopicWeaveProposal

`TopicWeaveProposal` 是 topic weave 生成的候选文档变更和审批历史。

字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | string | proposal ID |
| `topic_id` | string | 关联专题 |
| `thought_id` | string | 待写入专题的 Thought |
| `status` | enum | `pending`、`accepted` |
| `source_link` | string | 候选文档必须保留的来源链接 |
| `membership` | TopicMembership | 本次 weave 使用的匹配事实 |
| `base_document` | string | 生成预览时的专题文档 |
| `proposed_document` | string | 候选专题文档 |
| `accepted_document` | string | 用户最终确认写入的文档 |
| `diff` | []TopicDocumentDiffLine | 逐行差异 |
| `patch` | TopicDocumentPatch | 可校验并应用的结构化补丁，包含 base/proposed hash 和 hunk 行操作 |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |
| `accepted_at` | time | 确认时间 |

功能定义：

1. `weave-preview` 创建 pending proposal，不直接改写 `index.md`。
2. 用户确认未编辑候选文档时，按结构化 patch 校验 base hash 和上下文后写入 `index.md`；若当前文档已变化则拒绝应用。
3. 用户编辑候选文档时，将完整 `accepted_document` 写入 `index.md`，仍校验 source link。
4. proposal 文件进入 Git，作为 topic weave 审批队列和历史。

### 4.6 SynthesisDraft

`SynthesisDraft` 是用户基于搜索结果生成的本地合稿草稿。

字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | string | 草稿 ID |
| `thought_ids` | []string | 输入碎片 |
| `goal` | string | 用户目标 |
| `format` | enum | `summary`、`outline`、`report` |
| `content` | string | AI 生成内容 |
| `source_links` | []string | 原子笔记链接 |
| `model` | string | 使用模型，未配置 AI 时为 `local-rule` |
| `status` | enum | `draft`、`saved` |
| `saved_thought_id` | string | 保存后生成的 Thought ID |
| `history` | []SynthesisDraftHistory | 草稿创建和保存历史 |
| `created_at` | time | 创建时间 |
| `updated_at` | time | 更新时间 |
| `saved_at` | time | 保存时间 |

功能定义：

1. 从选中 Thought 生成摘要或大纲。
2. 配置 `llm.api_key` 时使用 OpenAI-compatible chat model 生成 Markdown 草稿；未配置时使用本地规则合稿。
3. 默认写入 `synthesis/drafts/{draft_id}.yaml`，作为独立草稿仓库。
4. 用户保存后通过 capture 创建新的 Thought，`source` 标记为 `synthesis`，并保留来源 Thought 链接。
5. 保存后将草稿状态标记为 `saved`，记录 `saved_thought_id`、`saved_at` 和历史事件。

## 5. 运行任务模型

### 5.1 Job

`Job` 表示一个后台任务。

字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | string | 任务 ID |
| `type` | enum | `refine`、`index`、`topic_match`、`topic_weave`、`git_commit`、`reindex` |
| `resource_type` | enum | `thought`、`topic`、`workspace` |
| `resource_id` | string | 关联资源 ID |
| `status` | enum | `queued`、`running`、`retrying`、`succeeded`、`failed`、`canceled` |
| `attempt` | int | 当前尝试次数 |
| `max_attempts` | int | 最大尝试次数 |
| `progress` | float | 0 到 1 |
| `message` | string | UI 可显示摘要 |
| `error` | ErrorRef | 失败信息 |
| `created_at` | time | 创建时间 |
| `started_at` | time | 开始时间 |
| `finished_at` | time | 结束时间 |

功能定义：

1. 创建任务。
2. 查询任务快照。
3. 更新任务进度。
4. 失败重试。
5. 通过 SSE 推送任务事件。

### 5.2 ErrorRef

`ErrorRef` 是业务错误摘要。

字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `code` | string | 稳定错误码 |
| `message` | string | 用户可理解摘要 |
| `details` | map | 结构化细节，不能含密钥或完整正文 |
| `occurred_at` | time | 发生时间 |
| `retryable` | bool | 是否可重试 |

功能定义：

1. 统一 HTTP 错误响应。
2. 写入 Job 失败状态。
3. 必要时写入 Thought/Topic front matter 的错误摘要。

## 6. 系统运维模型

### 6.1 DomainEvent

`DomainEvent` 是运行单元之间的协同协议。

字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `event_id` | string | 事件 ID |
| `event_type` | string | 事件类型 |
| `source_unit` | string | 来源运行单元 |
| `occurred_at` | time | 发生时间 |
| `trace_id` | string | 请求链路 ID |
| `workspace_id` | string | 工作区 |
| `resource_type` | string | 资源类型 |
| `resource_id` | string | 资源 ID |
| `payload_version` | int | payload 版本 |
| `payload` | struct | 明确结构体 |

功能定义：

1. 运行单元异步通知。
2. SSE 状态推送。
3. 支持断线恢复的 offset 快照。

### 6.2 GitChangeSet

`GitChangeSet` 是 git-sync 的待提交集合。

字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | string | 变更集合 ID |
| `paths` | []string | 相对工作区路径 |
| `reason` | enum | `capture`、`refine`、`topic_update`、`manual` |
| `resource_ids` | []string | 关联 Thought/Topic |
| `created_at` | time | 创建时间 |
| `debounce_until` | time | 合并提交截止时间 |

功能定义：

1. 合并短时间连续 Markdown 变更。
2. 生成 commit message。
3. 提交成功后发布 Git 事件。

### 6.3 GitCommitRecord

`GitCommitRecord` 是 Git 自动提交结果视图。

字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `commit_hash` | string | Git commit hash |
| `message` | string | commit message |
| `paths` | []string | 已提交路径 |
| `resource_ids` | []string | 关联资源 |
| `committed_at` | time | 提交时间 |
| `error` | ErrorRef | 失败时错误 |

功能定义：

1. 查询资源最近 Git 记录。
2. UI 展示 Git 成功或失败。
3. Git 失败不回滚 Markdown 写入。

### 6.4 SystemStatus

`SystemStatus` 是系统健康和配置摘要。

字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `workspace` | object | 工作区读写状态 |
| `duckdb` | object | 索引库连接和滞后状态 |
| `ai` | object | provider 是否配置 |
| `git` | object | 仓库、用户身份、未提交变更 |
| `background` | object | 后台任务队列 |
| `events` | object | SSE/EventHub 状态 |

功能定义：

1. 支持 `/api/system/status`。
2. 支持 ready/degraded 判断。
3. 为 UI 顶部状态和故障提示提供数据。

## 7. 功能定义矩阵

### 7.1 capture

命令：

| 命令 | 输入 | 输出 | 事件 |
| --- | --- | --- | --- |
| `CaptureThought` | type/content/url/title/tags/topic_hints | Thought 快照、Job 列表 | `thought.captured` |
| `GetThought` | thought_id | Thought + ThoughtContent | 无 |

查询：

1. 按 ID 查询原子笔记。
2. 按 content hash 查询疑似重复笔记。

必须校验：

1. `text` 类型必须有 `content`。
2. `url` 类型必须有合法 URL。
3. 写入路径必须位于 workspace 内。

### 7.2 refiner

命令：

| 命令 | 输入 | 输出 | 事件 |
| --- | --- | --- | --- |
| `RefineThought` | thought_id | Job | `thought.refine_started`、`thought.refined`、`thought.refine_failed` |
| `RetryRefineThought` | thought_id | Job | 同上 |

查询：

1. 查询加工状态。
2. 查询最近错误。

必须校验：

1. AI 输出结构化解析成功后才能写回。
2. 输入 hash 未变化时可跳过重复加工。
3. URL 抓取失败不能删除原始笔记。

### 7.3 search

命令：

| 命令 | 输入 | 输出 | 事件 |
| --- | --- | --- | --- |
| `IndexThought` | thought_id/path | Job 或索引结果 | `search.index_updated`、`search.index_failed` |
| `ReindexWorkspace` | workspace_id | Job | `search.reindex_started`、`search.reindex_finished` |

查询：

| 查询 | 输入 | 输出 |
| --- | --- | --- |
| `SearchThoughts` | q/mode/filters/page | SearchResult 列表 |
| `GetSearchPreview` | thought_id | 预览片段 |

必须校验：

1. DuckDB 缺失或损坏时允许全量重建。
2. embedding 缺失时混合搜索降级。
3. 搜索结果必须包含原子笔记回跳路径。

### 7.4 topic

命令：

| 命令 | 输入 | 输出 | 事件 |
| --- | --- | --- | --- |
| `CreateTopic` | name/description/rules/outline | Topic | `topic.created` |
| `UpdateTopic` | topic_id/rules/outline/auto_weave | Topic | `topic.updated` |
| `MatchThought` | thought_id | TopicMembership 列表 | `topic.matched` |
| `RebuildTopic` | topic_id | Job | `topic.rebuild_started`、`topic.updated`、`topic.rebuild_failed` |
| `PreviewWeave` | topic_id/thought_id | TopicWeaveProposal | 无写入事件 |
| `AcceptWeave` | topic_id/thought_id/document | TopicDetail | `topic.updated` |

查询：

1. 查询专题列表和统计。
2. 查询专题详情。
3. 查询专题成员。
4. 查询专题活动记录。

必须校验：

1. slug 路径安全且唯一。
2. 规则阈值在合法范围内。
3. AI 缝合结果必须保留 source link。
4. 写入 `index.md` 必须使用临时文件加原子替换。

### 7.5 git-sync

命令：

| 命令 | 输入 | 输出 | 事件 |
| --- | --- | --- | --- |
| `EnqueueChangeSet` | paths/reason/resource_ids | GitChangeSet | `git.commit_requested` |
| `CommitPendingChanges` | workspace_id | GitCommitRecord | `git.commit_succeeded`、`git.commit_failed` |

查询：

1. 查询 Git 当前状态。
2. 查询资源最近提交记录。

必须校验：

1. 提交路径只能位于 workspace 内。
2. 默认不提交运行态数据目录和 DuckDB 文件。
3. 用户身份缺失时返回可理解错误。

### 7.6 application

命令与查询：

| API | 对应功能 |
| --- | --- |
| `POST /api/thoughts` | `CaptureThought` |
| `GET /api/thoughts/{id}` | `GetThought` |
| `POST /api/thoughts/{id}/retry-refine` | `RetryRefineThought` |
| `GET /api/search` | `SearchThoughts` |
| `POST /api/synthesis` | `CreateSynthesisDraft` |
| `POST /api/synthesis/save` | `SaveSynthesisDraft` |
| `GET /api/topics` | `ListTopics` |
| `POST /api/topics` | `CreateTopic` |
| `GET /api/topics/{id}` | `GetTopic` |
| `PUT /api/topics/{id}` | `UpdateTopic` |
| `POST /api/topics/{id}/rebuild` | `RebuildTopic` |
| `POST /api/topics/{id}/weave-preview` | `PreviewWeave` |
| `POST /api/topics/{id}/weave-accept` | `AcceptWeave` |
| `GET /api/events` | SSE 事件流 |
| `GET /api/jobs/{id}` | `GetJob` |
| `GET /api/system/status` | `GetSystemStatus` |
| `GET /api/system/metrics` | `GetSystemMetrics` |
| `GET /metrics` | Prometheus 指标文本 |
| `POST /api/system/reindex` | `ReindexWorkspace` |

必须校验：

1. Handler 只做请求解析、鉴权预留、错误映射和响应包装。
2. 长耗时任务返回 `202 Accepted`。
3. 所有响应携带 `request_id`。

## 8. 模型关系

```text
Workspace 1 -> N Thought
Workspace 1 -> N Topic
Thought 1 -> 0..1 ThoughtRefinement
Thought 1 -> 0..N EmbeddingRecord
Thought N -> N Topic through TopicMembership
Topic 1 -> 1 TopicRule
Topic 1 -> 1 TopicOutline
SearchIndex rebuilds from Thought + Topic
SynthesisDraft N -> N Thought
Job N -> 1 resource
GitCommitRecord N -> N Thought/Topic paths
DomainEvent N -> 1 resource
```

关键关系约束：

1. `Thought` 与 `Topic` 是多对多。
2. `TopicMembership` 可由规则生成，也可由用户手动固定。
3. `EmbeddingRecord` 允许同一 Thought 存多模型版本，但搜索默认使用当前配置模型。
4. `SearchIndex` 不能反向成为 Thought 或 Topic 的唯一来源。
5. `SynthesisDraft` 保存后成为新的 Thought，并保留来源 Thought 链接。

## 9. 开发前确认结论

已确认可进入开发的模型范围：

1. M1 实现 `Workspace`、`Thought`、`ThoughtContent`、`Job`、`DomainEvent`、`GitChangeSet`、`GitCommitRecord`。
2. M2 实现 `ThoughtRefinement`、`EmbeddingRecord`、`SearchIndex`、`SearchResult`。
3. M3 实现 `Topic`、`TopicRule`、`TopicOutline`、`TopicMembership`、`SynthesisDraft`。

需要在编码时固定的工程细节：

1. Go module 名称。
2. Markdown front matter 解析库。
3. DuckDB Go driver 和向量存储格式。
4. LLM/Embedding Provider mock 接口与默认模型配置。
5. Job 快照落盘格式。
6. Commit message 模板。

建议优先开发纵向切片：

1. `Workspace` 初始化。
2. `POST /api/thoughts` 文本采集。
3. `Thought` Markdown 落盘。
4. `thought.captured` 事件发布。
5. `Job` 快照和 SSE 推送。
6. `GitChangeSet` 自动提交。

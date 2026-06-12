# ThoughtFlow 功能设计文档

> 本文基于 [ThoughtFlow PRD](./thoughtflow-prd.md) 输出功能级设计，用于指导 Go 单二进制应用实现与后续代码收口。当前代码以"采集会话持久化 + 异步加工 + 主题缝合 + Git 提交"为骨干；本设计文档在此基础上补充会话式采集的完整生命周期、归档预览、归档策略、Thought 重新整理、专题候选消费与外部请求隐私提示，作为 P0–P3 backlog（#97–#104）的实施蓝图。当前代码完成度请以 [实现状态](./thoughtflow-implementation-status.md) 为准。

## 1. 设计目标

ThoughtFlow 的功能设计围绕"快速捕捉、异步加工、自动归档、即时检索、可追溯演进"展开。

### 1.1 核心目标

1. **会话优先的采集**：用户输入文本、URL 或问题时默认进入当前采集会话；只有用户明确"归档"才落地为原子 Markdown。`POST /api/thoughts` 直接落 Thought 的"瞬时归档"路径保留，但默认 UI 走会话路径。
2. **结构化上下文**：每轮对话后由 LLM/本地规则维护 `session_context`（主题、目标、事实、冲突、待澄清、候选标题/标签/摘要/正文、来源链接、关联 Thought、命中专题、归档意图、归档策略），用户可编辑。
3. **发散-收敛节奏**：每轮对话自动刷新上下文，提示追问、补充关联、识别冲突；系统判断信息足够时可提示归档，但绝不自动保存。
4. **HTTP 入口秒级响应**：所有 LLM/Embedding/Web fetch/Topic weave 必须走 `magicCommon/framework` `BackgroundRoutine`，HTTP 请求只返回受理状态或当前快照。
5. **Markdown 为事实源**：DuckDB 仅承担索引、查询与可重建分析缓存；任何写入路径都先落 Markdown。
6. **Git 自动记录**：Markdown 变更走 `git add / commit`；Git 失败不阻断采集主流程，通过事件回写失败原因。
7. **运行单元拆分**：`capture` / `refiner` / `topic` / `search` / `git-sync` / `application`；状态由所属运行单元维护，跨单元通过 `event.Hub` 或窄接口协作。
8. **可溯源**：每个搜索结果、专题内容、合稿结论都能反向链接到原始原子笔记。

### 1.2 非目标

1. 首版不实现多人协作权限体系。
2. 首版不提供重型数据库服务端依赖（单二进制 + DuckDB 嵌入式 + 纯文件）。
3. 首版不把 DuckDB 作为不可恢复主存储。
4. 首版不承诺全自动 Git 冲突解决，冲突只检测、记录和提示。
5. 首版不实现实时协同编辑（同一时刻一个会话对应一个用户，PATCH 走 `thoughtlock` 串行化即可）。

---

## 2. 总体架构

```text
Browser / Local UI  (会话、归档预览、Thought、专题、搜索、设置)
        |
        v
magicEngine HTTP / SSE
        |
        v
application runtime unit  (路由、Handler 编排、跨单元应用服务)
        |
        +--> capture   --> scratchpad JSON + thoughts/ Markdown
        |                  └─ EventHub: scratchpad.*, thought.captured, thought.patched
        +--> refiner   --> Web fetch / LLM / Embedding
        |                  └─ EventHub: thought.refine_*, search.index_*
        +--> search    --> DuckDB FTS + 向量索引
        |                  └─ EventHub: search.index_*
        +--> topic     --> Topic 规则 / LLM weave / 候选消费
        |                  └─ EventHub: topic.*, topic.candidate_*
        +--> git-sync  --> git add / commit
                           └─ EventHub: git.commit_*
```

### 2.1 框架职责边界

`magicCommon/framework` 负责：

1. 应用生命周期：`Setup`、`Run`、`Teardown`。
2. 运行单元显式装配和依赖注入。
3. `event.Hub` 事件发布、订阅和跨单元协同。
4. `task.BackgroundRoutine` 后台任务调度、关闭和排空。
5. 配置、日志、健康检查、监控指标承载。

`magicEngine` 负责：

1. REST API 路由注册（`/api/capture/...`、`/api/thoughts/...`、`/api/topics/...`、`/api/search`、`/api/events`、`/api/system/...`）。
2. Handler 请求解析、响应格式化和错误映射。
3. SSE 连接管理和事件推送。
4. 静态 UI 资源托管（`/`、`/app.js`、`/styles.css`、`/i18n/...`、`/vendor/...`）。

Handler 不直接操作 Markdown、DuckDB、Git 或外部 LLM/Embedding API；这些动作必须进入对应运行单元的 `biz` 或 `service` 层。

### 2.2 实时性

- HTTP API：同步返回受理状态或当前快照；长任务以 `BackgroundRoutine` 异步处理。
- SSE `GET /api/events`：订阅 EventHub 实时事件，支持 `Last-Event-ID` 续传与 `types` 过滤。

### 2.3 配置与可观测

- 全局 + 模块双层配置（`application.toml`）。
- `/api/system/status` 暴露 LLM/Embedding/Reader/DuckDB/Git/scratchpad 健康状态。
- `/api/system/metrics`、`/metrics`（Prometheus）暴露运行指标。
- 结构化日志，关键失败（provider 调用、Git commit、index、scratchpad 写入）必须能定位。

---

## 3. 运行单元设计

### 3.1 capture（采集运行单元）

#### 3.1.1 职责

1. **会话式采集**：维护 scratchpad 持久化层；接收多轮对话、URL 草稿、文本片段；维护 `session_context` 结构化字段。
2. **归档路由**：仅在用户显式"保存/归档/提交"时，将 scratchpad 落 Thought；走 `archive_strategy` 路由。
3. **归档预览**：生成归档预览 payload（标题、正文、标签、来源、策略），不写 Thought，等用户确认。
4. **Thought 重新整理**：从已归档 Thought 加载上下文，生成"补充/修订/另存"会话。
5. **去重与告警**：基于 `content_hash` 检测疑似重复，注入 `ErrorRef`，默认不静默丢弃。
6. **事件发布**：`scratchpad.message_appended`、`scratchpad.draft_updated`、`scratchpad.committed`（fresh/repeat）、`thought.captured`、`thought.patched`、`thought.supplemented`。

#### 3.1.2 scratchpad 持久化

存储位置：`<workspace>/.scratchpad/<sessionID>.json`（升级到 v2，引入 `session_context` 后调整命名空间或目录以保持隔离）。

数据结构（v2 草案，对齐 PRD §3.1）：

```go
type Scratchpad struct {
    SessionID   string        `json:"session_id"`
    WorkspaceID string        `json:"workspace_id"`
    CreatedAt   time.Time     `json:"created_at"`
    UpdatedAt   time.Time     `json:"updated_at"`

    // 轻量级 UI 投影（每次 AppendDraft 都更新，便于 UI 立刻看到候选态）
    Title       string        `json:"title"`
    Tags        []string      `json:"tags"`
    TopicHints  []string      `json:"topic_hints"`
    URL         string        `json:"url,omitempty"`

    // 累积内容（用户原始输入，commit 时作为 CaptureCommand.Content）
    Content     string        `json:"content"`
    Messages    []Message     `json:"messages"`

    // 草稿累积（rename / add_tag / remove_tag / notes / topics）
    Draft       Draft         `json:"draft"`

    // session_context（PRD §3.1 必需字段；v1→v2 迁移时留空）
    SessionContext SessionContext `json:"session_context"`

    // 归档相关（用户与系统共同维护）
    ArchiveIntent    ArchiveIntent    `json:"archive_intent"`     // none|menu|llm
    ArchiveStrategy  ArchiveStrategy  `json:"archive_strategy"`   // new|update_thought|supplement
    ArchivePreview   *ArchivePreview  `json:"archive_preview,omitempty"`

    // Thought 重新整理
    SourceThoughtID string `json:"source_thought_id,omitempty"`

    // 已归档关联（commit 后回写）
    CommittedThoughtID string     `json:"committed_thought_id,omitempty"`
    CommittedAt        *time.Time `json:"committed_at,omitempty"`
}

type SessionContext struct {
    Topic              string   `json:"topic"`
    Goal               string   `json:"goal"`
    ConfirmedFacts     []string `json:"confirmed_facts"`
    OpenQuestions      []string `json:"open_questions"`
    Conflicts          []string `json:"conflicts"`
    CandidateTitle     string   `json:"candidate_title"`
    CandidateTags      []string `json:"candidate_tags"`
    CandidateSummary   string   `json:"candidate_summary"`
    CandidateBody      string   `json:"candidate_body"`
    SourceLinks        []string `json:"source_links"`
    RelatedThoughtIDs  []string `json:"related_thought_ids"`
    SuggestedTopicIDs  []string `json:"suggested_topic_ids"`
}

type ArchiveIntent string  // "none" | "menu" | "llm"
type ArchiveStrategy string // "new" | "update_thought" | "supplement"

type ArchivePreview struct {
    ThoughtID      string           `json:"thought_id,omitempty"`      // update_thought / supplement 时必填
    Title          string           `json:"title"`
    Body           string           `json:"body"`
    Tags           []string         `json:"tags"`
    SourceLinks    []string         `json:"source_links"`
    RelatedTopics  []string         `json:"related_topics"`
    Strategy       ArchiveStrategy  `json:"strategy"`
    Diff           *ThoughtDiff     `json:"diff,omitempty"`            // update_thought 时生成
}

type ThoughtDiff struct {
    Before  string   `json:"before"`
    After   string   `json:"after"`
    Changed []string `json:"changed_fields"`
}
```

格式升级策略：

- `formatVersion` 从 `1` 升到 `2`。
- `loadFromDisk` 遇到 `version=1` 文件时执行 v1→v2 迁移：保留旧字段并填充空 `SessionContext` / `ArchiveIntent=none` / `ArchiveStrategy=new` / `SourceThoughtID` 置空。
- 迁移失败的旧文件记录日志后跳过，不阻断服务。

#### 3.1.3 业务能力

| 方法 | 行为 | 调用方 |
|---|---|---|
| `AppendMessage(sessionID, role, text)` | 追加消息；user 角色追加到 `Content` | 每次 chat 提交 |
| `AppendDraft(sessionID, draft)` | 合并 rename / add_tag / remove_tag / append_note / add_topic；投影到顶层 | chat 内 LLM 工具调用 |
| `UpdateSessionContext(sessionID, ctx)` | LLM 维护/用户编辑 `SessionContext` | chat 内 LLM 工具调用、UI 编辑 |
| `SetArchiveIntent(sessionID, intent)` | none / menu / llm | UI 触发 / LLM 识别 |
| `SetArchiveStrategy(sessionID, strategy)` | new / update_thought / supplement | UI 选择 / LLM 建议 |
| `BuildArchivePreview(sessionID)` | 渲染 `ArchivePreview`，update_thought 时计算 `Diff` | UI 点"归档"时 |
| `Commit(sessionID)` | 真正落地：fresh → `Capture`+`applyDraftToThought`；repeat → `ApplyDraftInternal`（锁自由）；supplement → 创建新 Thought + backlink；update_thought → `PatchThought`（带 diff 确认） | UI 确认归档 |
| `ReopenFromThought(thoughtID)` | 从已归档 Thought 加载上下文到新 scratchpad，置 `SourceThoughtID` | UI 点"重新整理" |
| `LastActive()` | 返回最近活跃的未归档 scratchpad（已存在） | UI 页面恢复 |
| `MarkCommitted / Reset` | 已存在，commit 流程使用 | 内部 |

#### 3.1.4 归档策略路由

| `ArchiveStrategy` | 触发条件 | 落地动作 | 二次确认 |
|---|---|---|---|
| `new` | 普通会话；或 reopen 但用户选择"另存" | `Capture` → `applyDraftToThought` | 预览 |
| `update_thought` | reopen 会话 + 用户选"更新原 Thought" | 渲染 `Diff` → `PatchThought` | 强制 diff 确认 |
| `supplement` | reopen 会话 + 用户选"生成补充 Thought" | `Capture`（content 标 `[补充] 前置 thought-xxx`）+ 双向 backlink | 预览 |

`update_thought` 必须经过 `thoughtlock.Locker` 串行化；`supplement` / `new` 走 `ApplyDraftInternal`（锁自由）以避免与 refiner/expander 抢占。

#### 3.1.5 `CaptureCommitter` 接口扩展

```go
type CaptureCommitter interface {
    Capture(ctx, cmd) (CaptureResult, error)
    PatchThought(ctx, thoughtID, sessionID, req, rawBody) (ThoughtSnapshot, error)
    ApplyDraftInternal(ctx, thoughtID, sessionID, req, rawBody) (ThoughtSnapshot, error)
    ReopenFromThought(ctx, thoughtID, sessionID) (Scratchpad, error)  // 新增
}
```

#### 3.1.6 事件

| 事件 | Payload 关键字段 | 订阅方 |
|---|---|---|
| `scratchpad.message_appended` | session_id, role, text | 暂无（前端 SSE 拉） |
| `scratchpad.draft_updated` | session_id, draft_diff | 暂无 |
| `scratchpad.context_updated` | session_id, ctx_diff | topic（命中候选） |
| `scratchpad.committed` | session_id, thought_id, mode | git-sync（参考 commit 日志） |
| `thought.captured` | thought | refiner、search、topic、git-sync |
| `thought.patched` | thought | topic、git-sync |
| `thought.supplemented` | parent_thought_id, new_thought_id | topic、search（建立双向 backlink） |

#### 3.1.7 错误模型

- `ErrInvalidPatchField` → 400。
- `ErrLocked` → 409（被其他 session 占用）。
- `ErrRefining` → 409（refiner 持有，建议客户端短暂退避）。
- `ErrInvalidSession` → 400。
- `ErrEmptyContent` → 400。
- `ErrAlreadyCommitted` → 409（已 commit，需要走 reopen-session 路径）。
- `ErrStrategyRequired` → 400（reopen 会话未选策略）。
- `ErrDiffRequired` → 400（update_thought 但无 diff，确认未走完）。

---

### 3.2 refiner（精炼运行单元）

#### 3.2.1 职责

1. 订阅 `thought.captured` 与 `thought.supplemented`，提交后台加工任务。
2. 对 URL 类笔记抓取正文：本地 fetcher 优先，失败时回退 Jina Reader。
3. AI 摘要、关键点、AI 标签（`ai_tags`）、`related_thought_ids`、`suggested_topic_ids`、`expansion_plan`。
4. 生成 embedding，写入 DuckDB 向量索引。
5. 通过 `BackgroundRoutine` 异步处理；状态通过 EventHub 发布。

#### 3.2.2 状态机

`refine_status: pending → running → (succeeded | failed | partial)`，对应 `refine` Job 的 `JobStatus`。

#### 3.2.3 错误模型

- `thoughtflow.ai.transient_status`（429/5xx）→ 退避重试（3 次）。
- `thoughtflow.ai.http_status`（其他 4xx）→ 不重试。
- `thoughtflow.ai.invalid_json`（LLM 返回非 JSON）→ 不重试。
- `thoughtflow.ai.request_failed`（网络）→ 退避重试。
- `thoughtflow.ai.read_failed`（4 MiB 截断）→ 不重试。

---

### 3.3 topic（专题运行单元）

#### 3.3.1 职责

1. 维护专题规则、关键词触发、语义相似度阈值。
2. 订阅 `thought.refined` / `search.index_updated` / **`scratchpad.context_updated` / `scratchpad.committed`**：将 scratchpad 候选与正式 Thought 同时纳入匹配。
3. **状态分层**：
   - 正式成员 = 已 commit 的 Thought + 命中专题规则。
   - 候选 = 未 commit 的 scratchpad + 命中专题规则或语义近邻。
   - 冲突/待确认 = scratchpad 候选之间或与已正式成员存在矛盾（`SessionContext.Conflicts` 非空）。
4. **智能缝合**：LLM 负责寻找合适章节插入新成员；只接受正式成员进入 `topics/<name>/index.md`。
5. **全量刷新**：`POST /api/topics/:id/refresh` 触发后，基于"全部 Thought + 全部未 commit scratchpad + 全部从 Thought 重新发起的会话 + synthesis 草稿"刷新正式/候选/冲突三态。
6. 专题创建/规则更新后自动触发一次 refresh。

#### 3.3.2 候选消费通道

```text
scratchpad.append_message  ──┐
scratchpad.draft_updated   ──┼─→ topic.MatchScratchpadAsync(session_id)
scratchpad.context_updated ──┘                │
                                              ├─→ 候选列表（只读）
                                              ├─→ 命中分数 ≥ Threshold：进候选区
                                              └─→ 冲突/待确认：进冲突区
```

候选区不写专题主文档；用户可在 UI 上"确认纳入"（触发对应 scratchpad commit 走 `new` 策略）才会正式落地。

#### 3.3.3 API

- `GET /api/topics`
- `POST /api/topics`
- `GET /api/topics/:id`
- `PUT /api/topics/:id`
- `POST /api/topics/:id/rebuild`（历史兼容）
- `POST /api/topics/:id/refresh`（PRD §3.3 新增，全量刷新）
- `GET /api/topics/:id/candidates`（新；返回 scratchpad 候选列表）

#### 3.3.4 错误模型

- `ErrTopicNotFound` → 404。
- `ErrTopicRuleInvalid` → 400（规则 JSON 校验失败）。
- `ErrTopicRefreshInProgress` → 409（同名 refresh 已在跑）。

---

### 3.4 search（检索运行单元）

#### 3.4.1 职责

1. 订阅 `thought.captured` / `thought.patched` / `thought.supplemented` / `refiner.succeeded` → 更新 DuckDB 索引（FTS + 向量）。
2. `GET /api/search` 混合检索：关键词 FTS + 向量 Embedding 召回 + 重排。
3. 结果回溯：每条命中带 `source_thought_id`、`source_session_id`（如果是 scratchpad 候选命中）、`backlink`。
4. 命中 scratchpad 候选的条目标注 `candidate=true`，区分正式与候选。

#### 3.4.2 API

- `GET /api/search?q=...&limit=...&include_candidates=...`

---

### 3.5 git-sync（Git 同步运行单元）

#### 3.5.1 职责

1. 订阅 `git.commit_requested` → 排队 debounce 提交。
2. 失败时发布 `git.commit_failed`（包含 thought path、错误码），不阻断上游。
3. 提供只读查询：`GET /api/thoughts/:id` 聚合时返回最近 5 条 commit 摘要。

---

### 3.6 application（应用运行单元）

#### 3.6.1 职责

1. `magicEngine` HTTP/SSE 入口：路由注册、Handler 编排。
2. 跨单元应用服务（不持有正式状态）：`CommitFromScratchpad`、`ReopenFromThought`、`BuildArchivePreview`。
3. SSE 事件流转发。
4. 静态 UI 资源。

#### 3.6.2 Handler 边界

- 只做请求解析、鉴权（如启用）、响应格式化、错误码映射。
- 业务逻辑必须进入对应运行单元的 `biz` 层。

---

## 4. 核心数据流

### 4.1 会话式采集（默认路径）

```text
UI 打开采集页
   └─ GET /api/capture/sessions/active → 后端 LastActive() 恢复
        └─ 命中：返回 scratchpad，前端渲染历史
        └─ 未命中：创建空 scratchpad（status 200，不写 Thought）

UI 发送消息
   └─ POST /api/capture/sessions/{id}/messages
        ├─ capture.AppendMessage  → 写 scratchpad
        └─ LLM 维护 session_context（异步，事件：scratchpad.context_updated）
              └─ topic 订阅 → 命中候选

UI 点击"归档" / LLM 识别"保存"意图
   └─ POST /api/capture/sessions/{id}/archive/preview
        └─ capture.BuildArchivePreview → 返回 ArchivePreview（含 Diff）
   └─ UI 展示预览 → 用户确认
        └─ POST /api/capture/sessions/{id}/archive  body.strategy
              ├─ new         → Capture + applyDraftToThought
              ├─ supplement  → Capture(标注 parent) + 双向 backlink
              └─ update_thought → PatchThought (走 thoughtlock)
```

### 4.2 从 Thought 重新整理

```text
UI Thought 详情 → "重新整理"
   └─ POST /api/thoughts/{id}/reopen-session
        ├─ 创建 scratchpad_v2
        ├─ SourceThoughtID = 原 thought.id
        └─ SessionContext.* 加载自原 Thought 元数据
              ├─ Topic ← 关联专题名
              ├─ CandidateBody ← 原 Original
              ├─ CandidateTitle ← 原 UserTitle 或 ExtractedTitle
              ├─ CandidateTags ← 原 UserTags ∪ AITags
              ├─ SourceLinks ← 原 URL + URLFollowups
              └─ RelatedThoughtIDs ← 原 RelatedThoughtIDs
   └─ UI 进入采集页编辑
   └─ 提交归档
        ├─ 默认 strategy = supplement（PRD §3.1）
        ├─ 显式选择 update_thought → 走 diff 确认
        └─ 显式选择 new → 创建新 Thought 失去 backlink
```

### 4.3 专题候选消费

```text
scratchpad 任意变更
   └─ topic.MatchScratchpadAsync(session_id)
        ├─ 计算关键词 + 语义分数
        ├─ ≥ threshold：写入 candidate 列表（Redis/JSONL/dedicated store）
        └─ 订阅者：UI SSE 推送 candidate 列表更新

UI 打开专题详情
   └─ GET /api/topics/:id/candidates
        └─ 用户确认某 scratchpad 纳入
              └─ 触发该 scratchpad 的 commit 流程（new strategy）
              └─ 归档后 EventTopicMatched 重新计算 → 进正式成员
```

### 4.4 异步加工

```text
EventThoughtCaptured
   └─ refiner.Enqueue → BackgroundRoutine
        ├─ web fetch（仅 url 类）
        ├─ LLM 摘要 / 标签 / 关键点 / expansion_plan
        ├─ LLM embedding
        └─ 写回 thought + DuckDB
              ├─ EventThoughtRefined
              └─ EventSearchIndexUpdated
                     └─ topic.MatchThoughtAsync(thought_id)
                            └─ 命中：EventTopicMatched → 触发 weave → EventTopicUpdated
                                   └─ git-sync 异步提交
```

---

## 5. API 契约（对齐 PRD §2.2）

### 5.1 采集会话

| Method | Path | Body | 200 Response | 错误 |
|---|---|---|---|---|
| `POST` | `/api/capture/sessions` | `{content?, source_thought_id?, reuse_last?:true}` | `{session_id, scratchpad}` | 400 invalid |
| `GET` | `/api/capture/sessions/active` | — | `{scratchpad}` 或 204 | — |
| `GET` | `/api/capture/sessions` | — | `[{session_id, title, ...summary}]` | — |
| `POST` | `/api/capture/sessions/{id}/messages` | `{role, text}` | `{scratchpad}` | 400 invalid_session |
| `POST` | `/api/capture/sessions/{id}/context` | `SessionContext` | `{scratchpad}` | 400 invalid |
| `POST` | `/api/capture/sessions/{id}/intent` | `{intent: none|menu|llm}` | `{scratchpad}` | — |
| `POST` | `/api/capture/sessions/{id}/strategy` | `{strategy: new\|update_thought\|supplement, thought_id?}` | `{scratchpad}` | 400 strategy_required |
| `GET` | `/api/capture/sessions/{id}/archive/preview` | — | `ArchivePreview` | 400 empty_content |
| `POST` | `/api/capture/sessions/{id}/archive` | `{strategy, confirmed?:true}` | `CaptureResult` | 409 locked, 400 diff_required |
| `DELETE` | `/api/capture/sessions/{id}` | — | `{deleted:true}` | 404 not_found |

### 5.2 Thought

| Method | Path | Body | 200 Response | 错误 |
|---|---|---|---|---|
| `POST` | `/api/thoughts` | `CaptureCommand` | `CaptureResult` | 400 invalid |
| `GET` | `/api/thoughts/{id}` | — | `ThoughtSnapshot` | 404 not_found |
| `PATCH` | `/api/thoughts/{id}` | `ThoughtPatchRequest` + `X-Session-Id` | `ThoughtSnapshot` | 400 invalid_field, 409 locked |
| `POST` | `/api/thoughts/{id}/retry-refine` | — | `{job_id}` | 404 not_found |
| `GET` | `/api/thoughts/{id}/suggest` | — | `ThoughtSuggestion` | 404 not_found |
| `POST` | `/api/thoughts/{id}/reopen-session` | — | `{session_id, scratchpad}` | 404 not_found |

### 5.3 专题

| Method | Path | Body | 200 Response | 错误 |
|---|---|---|---|---|
| `GET` | `/api/topics` | — | `[]Topic` | — |
| `POST` | `/api/topics` | `TopicCreateRequest` | `Topic` | 400 rule_invalid |
| `GET` | `/api/topics/{id}` | — | `Topic` | 404 not_found |
| `PUT` | `/api/topics/{id}` | `TopicUpdateRequest` | `Topic` | 400 rule_invalid |
| `POST` | `/api/topics/{id}/rebuild` | — | `{job_id}` | — |
| `POST` | `/api/topics/{id}/refresh` | — | `{job_id}` | 409 refresh_in_progress |
| `GET` | `/api/topics/{id}/candidates` | — | `[]Candidate`（scratchpad 候选） | — |
| `POST` | `/api/topics/{id}/weave-preview` | — | `WeaveProposal` | — |
| `POST` | `/api/topics/{id}/weave-accept` | `{proposal_id}` | `{topic}` | 409 stale |

### 5.4 检索 & 合稿

| Method | Path | Body | 200 Response |
|---|---|---|---|
| `GET` | `/api/search` | — | `SearchResult{results, candidates}` |
| `POST` | `/api/synthesis` | `{selected_thought_ids[], prompt?}` | `SynthesisDraft` |
| `GET` | `/api/synthesis` | — | `[]SynthesisDraft` |
| `GET` | `/api/synthesis/{id}` | — | `SynthesisDraft` |
| `POST` | `/api/synthesis/save` | `{draft}` | `{thought}` |

### 5.5 系统 & 实时

| Method | Path | 说明 |
|---|---|---|
| `GET` | `/api/system/status` | LLM/Embedding/Reader/DuckDB/Git/scratchpad 健康 |
| `GET` | `/api/system/metrics` | 业务指标 |
| `POST` | `/api/system/reindex` | 全量重建 DuckDB |
| `GET` | `/api/jobs` | Job 列表 |
| `GET` | `/api/jobs/{id}` | Job 详情 |
| `GET` | `/api/events` | SSE 事件流 |
| `GET` | `/metrics` | Prometheus |
| `GET` | `/health/live` | 进程存活 |
| `GET` | `/health/ready` | 依赖就绪 |

### 5.6 路径兼容与废弃

旧路径保留 1 个版本过渡期并打印弃用日志：

- `POST /api/capture/sessions/start` → 新 `POST /api/capture/sessions`。
- `POST /api/capture/scratchpad/commit` → 新 `POST /api/capture/sessions/{id}/archive`。
- `POST /api/capture/new-session` → 新 `POST /api/capture/sessions`（带 `reuse_last:false`）。
- `GET/POST/DELETE /api/capture/scratchpad[?session_id=X]` → 新 `/api/capture/sessions/{id}` 系列。
- `GET /api/capture/scratchpad/list` → 新 `GET /api/capture/sessions`。

---

## 6. 数据模型

### 6.1 scratchpad 字段映射

PRD §3.1 的 `session_context` 14 字段 → scratchpad.SessionContext 同名字段；`archive_intent` / `archive_strategy` 提升为顶层 Scratchpad 字段（PRD 明确"可编辑字段"）。scratchpad v1 的 `Title` / `Tags` / `TopicHints` / `URL` / `Content` / `Messages` / `Draft` 保留作为"UI 投影"。

### 6.2 archive_intent / archive_strategy

```go
const (
    ArchiveIntentNone ArchiveIntent = "none"
    ArchiveIntentMenu ArchiveIntent = "menu"
    ArchiveIntentLLM  ArchiveIntent = "llm"

    ArchiveStrategyNew          ArchiveStrategy = "new"
    ArchiveStrategyUpdate       ArchiveStrategy = "update_thought"
    ArchiveStrategySupplement   ArchiveStrategy = "supplement"
)
```

### 6.3 Thought 双向 backlink（supplement 时）

`supplement` 策略在新建 Thought 的 front matter 写入：

```yaml
related_thought_ids: [<parent_thought_id>]
```

并在原 Thought 的 front matter 加入：

```yaml
related_thought_ids: [<new_thought_id>]
```

双向 backlink 写入走两次 `PatchThought`（两次都过 `thoughtlock` 串行化），并触发两次 `git.commit_requested`。

### 6.4 diff 生成

`update_thought` 时 `ArchivePreview.Diff` 由 `BuildArchivePreview` 计算：

- 标题：纯字符串 equal 判断。
- 标签：集合差集。
- 正文：行级 LCS 简化版（不强制引入 diff 库，行级 + 公共子串长度足够）。
- 关键点：集合差集。

返回字段：`{before, after, changed_fields}`，UI 用 unified diff 渲染或只展示"变更字段列表"。

---

## 7. 隐私与外部请求

PRD §5 要求显式标识外部请求并允许禁用。约束：

1. **配置层**：
   - `llm.enabled`、`llm.api_key`（空则降级 `LocalRefineProvider`）。
   - `embedding.enabled`、`embedding.api_key`。
   - `reader.enabled`（webfetch 抓取）、`reader.api_key`（Jina Reader 等）。
2. **UI 层**：
   - 触发外部请求的按钮（"AI 摘要"、"AI 标签"、"扩展计划"、"网页正文抓取"）必须带"轻量提示"，文案如"将向 `<provider>` 发送内容"。
   - 设置页"外部请求"区块可查看全局配置、临时禁用某类外部请求。
3. **API 层**：
   - `POST /api/system/external/disable?kind=llm|embedding|reader`（仅在显式启用鉴权时使用；首版可不实现，按 PRD §5 注释留作 backlog）。
4. **不阻塞**：按 PRD §5，外部请求不强制阻塞确认；UI 提示足够。

---

## 8. 边界、限制与未决项

### 8.1 已确定

- scratchpad 升级到 v2 时保留 v1 兼容读路径，迁移失败文件日志后跳过。
- `archive_strategy` 路由必须以 UI 显式选择或 reopen 会话默认 `supplement` 为准，LLM 不能直接 override。
- `update_thought` 走 `thoughtlock`；`new` / `supplement` 走 `ApplyDraftInternal`（与现有 refiner/expander 兼容）。
- 专题候选仅展示在候选区，不写专题主文档；用户在 UI 上"确认"后走对应 scratchpad 的 commit 流程。
- Web 入口保留旧路径 1 个版本过渡期（详见 §5.6）。

### 8.2 遗留与 backlog

| # | 项 | 来源 | 状态 |
|---|---|---|---|
| #97 | scratchpad v2 字段扩展 + v1→v2 迁移 | PRD §3.1 | 待办 |
| #98 | API 路径对齐 PRD §2.2 + 旧路径过渡 | PRD §2.2 / §5.6 | 待办 |
| #99 | 归档预览 `GET /api/capture/sessions/{id}/archive/preview` + UI 渲染 | PRD §3.1 / §4.4 | 待办 |
| #100 | `archive_strategy` 路由（new / update_thought / supplement）+ diff | PRD §3.1 | 待办 |
| #101 | `POST /api/thoughts/{id}/reopen-session` + UI 入口 | PRD §3.1.1 | 待办 |
| #102 | 专题消费 scratchpad 候选 | PRD §3.3 | 待办 |
| #103 | 隐私提示 UI 标识 + 设置页"外部请求" | PRD §5 | 待办 |
| #104 | e2e 覆盖（含会话恢复、归档预览、归档策略、reopen、专题候选、隐私提示） | PRD §7 #12 / §8 | 待办 |
| — | `POST /api/system/external/disable` 运行时禁用接口 | PRD §5 | 可选，留 backlog |

### 8.3 仍非目标

- 多人协作 / 权限体系。
- 实时协同编辑。
- 服务端数据库（Postgres/MySQL）。
- 全自动 Git 冲突解决。
- 跨工作区合并。

---

## 9. 实施顺序建议

1. **#97 scratchpad v2**：底层数据形状稳定后所有上层 UI/API 才能稳定。
2. **#98 API 路径对齐**：与 #97 并行（前者是数据，后者是路由）。
3. **#99 归档预览**：建立在 #97 字段之上。
4. **#100 archive_strategy 路由**：建立在 #97 + #99 之上；包含 `ApplyDraftInternal` 复用 + 双向 backlink。
5. **#101 reopen-session**：建立在 #97 + #100 之上。
6. **#102 专题候选消费**：建立在 #97 之上；topic 模块订阅 scratchpad 事件。
7. **#103 隐私提示**：UI 工作为主，可与 #104 并行。
8. **#104 e2e 覆盖**：贯穿 #97–#103 的端到端验证，最后做。

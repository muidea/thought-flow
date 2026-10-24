# ThoughtFlow 使用及配置说明

本文说明 ThoughtFlow 的本地启动、工作区、配置优先级、常用 API、验证命令和 CI 行为。

## 1. 环境要求

本地开发和验证建议准备：

1. Go `1.24.x`。
2. Node.js `22.x`，用于嵌入式前端语法检查、组件测试和浏览器 smoke 测试。
3. Git，用于自动提交 Markdown 变更。
4. Google Chrome 或 Chromium，用于 `make browser-test`。
5. C/C++ 工具链及 CGO，用于 DuckDB 构建与测试。

所有构建均使用 DuckDB，没有 fallback store 或 `duckdb` build tag。使用 `make test` / `make check` 验证后端。

## 2. 启动服务

在仓库根目录执行：

```bash
make build
./thoughtflow
```

默认监听：

```text
http://127.0.0.1:8080/
```

也可以直接用 Go 启动：

```bash
go run ./cmd/thoughtflow
```

指定配置目录：

```bash
./thoughtflow --config-dir ./config
```

## 3. 工作区结构

`--config-dir` 只定位配置；以下相对路径均相对于启动时工作目录，不相对于配置目录。

默认工作区是：

```text
./thoughtflow-workspace
```

首次启动会创建：

```text
thoughts/              原子笔记 Markdown
topics/                专题 YAML、专题 Markdown、membership 和审批草稿
attachments/           附件目录
document-formats/      自定义 DocumentFormat 草稿和已发布版本
```

默认运行状态目录是：

```text
./thoughtflow-runtime
```

首次启动会创建：

```text
jobs/                  后台 Job 快照
logs/                  运行日志目录
*.duckdb               搜索索引数据库
```

持久事实源以 `thoughts/` 和 `topics/` 下的 Markdown/YAML 为主；`runtime.state_dir` 是本地运行态目录，不应作为长期事实源提交。

## 4. 配置加载顺序

有效配置加载顺序为：

1. 内置默认值。
2. magicCommon 应用配置：`<config-dir>/application.toml`。
3. 启动参数 `--config-dir` 仅用于定位配置目录，不覆盖业务配置。

ThoughtFlow 直接复用 `magicCommon/framework/configuration`，启动时将 framework `ConfigDir` 指向独立配置目录。默认配置目录来自操作系统用户配置目录，例如 Linux 下通常是 `~/.config/thoughtflow`；也可以通过 `--config-dir` 覆盖。业务配置只读取 `application.toml` 和内置默认值，不读取环境变量覆盖。

运行状态目录由 `runtime.state_dir` 明确定义。启动时会校验：

1. `config-dir` 不能等于 `runtime.state_dir`。
2. `config-dir` 不能放在 `runtime.state_dir` 下。
3. `runtime.state_dir` 不能放在 `config-dir` 下。

推荐部署形态：

```text
/etc/thoughtflow/application.toml   配置文件
/var/lib/thoughtflow/runtime/       运行状态目录
```

## 5. 本地配置样例

完整模板见：

```text
doc/application.example.toml
```

示例路径：

```text
~/.config/thoughtflow/application.toml
```

基础示例内容：

```toml
[server]
host = "127.0.0.1"
port = 8080
# 无鉴权公开内容入口，默认关闭。启用前请确认反向代理与发布范围。
public_thoughts_enabled = false
# 对外可访问的 origin（scheme + host[:port]，无尾斜杠），用于拼装可分享内容 URL。
# 启用公开内容后，留空时 Web UI 回退到浏览器当前 origin。
# public_base_url = "https://thoughtflow.example.com"

[workspace]
content_dir = "./thoughtflow-workspace"
auto_init_git = true

[runtime]
state_dir = "./thoughtflow-runtime"

[capture]
duplicate_policy = "warn"

[document_profiles]
enabled = true
custom_dir = ""
default_profile_id = "builtin.note"
auto_reload = true
reload_interval_seconds = 2
max_match_candidates = 10
max_format_bytes = 131072
max_sections = 32
max_repair_attempts = 2

[refiner]
concurrency = 2
url_fetch_timeout_seconds = 30

[git_sync]
enabled = true
debounce_seconds = 5

[search]
duckdb_path = "thoughtflow.duckdb"
default_mode = "keyword"

[topic]
auto_weave = true
min_semantic_score = 0.78

[events]
sse_heartbeat_seconds = 20

[llm]
base_url = "https://api.openai.com"
api_key = ""
chat_model = "gpt-4o-mini"
timeout_seconds = 30

[embedding]
base_url = "https://api.openai.com"
api_key = ""
model = "text-embedding-3-small"
timeout_seconds = 30

```

说明：

1. `search.duckdb_path` 为相对路径时，会解析到 `runtime.state_dir` 下。
2. 当前进程启动时显式设置服务名为 `thoughtflow`，因此模板不需要配置 `endpointName`。
3. `llm.api_key` 为空时，摘要、专题缝合、Profile 匹配和结构化文档生成使用本地规则 provider。
4. `embedding.api_key` 为空时，服务使用 deterministic local embedding，仍可完成本地采集、搜索和专题匹配。
5. `workspace.auto_init_git` 当前是配置模型字段；实际提交能力由 `git_sync.enabled` 和本机 Git 仓库/身份状态决定。
6. 配置目录和运行状态目录应保持物理分离。配置目录存放 `application.toml`，运行状态目录存放 jobs、logs、DuckDB 等运行态文件。
7. `document_profiles.custom_dir` 为空时使用 `<workspace>/document-formats`；相对路径以 `application.toml` 所在配置目录为基准。启动时会自动创建 `drafts/` 和 `published/`，只有 `published/<profile-id>/vN.md` 参与匹配和生成。
8. `document_profiles.auto_reload = true` 时，服务按 `reload_interval_seconds` 周期扫描发布目录。新增 Profile 或新版本无需重启，也无需调用 reload API，即可同时对 Capture 和 Compose 生效。reload API 保留为运维兜底；原地修改已发布版本仍会触发 hash 冲突并退出新匹配。

## 6. CLI 参数

仅支持配置目录定位参数：

| 参数 | 说明 |
| --- | --- |
| `--config-dir` | magicCommon 配置目录，读取其中的 `application.toml` |

## 7. Git Sync 配置

启用 Git 自动提交：

```toml
[git_sync]
enabled = true
debounce_seconds = 5
```

提交前需要 workspace 是 Git 仓库，并配置用户身份：

```bash
git -C thoughtflow-workspace init
git -C thoughtflow-workspace config user.name "ThoughtFlow"
git -C thoughtflow-workspace config user.email "thoughtflow@example.local"
```

git-sync 会过滤运行态数据文件和 DuckDB 文件，只提交用户可读的工作区内容。若 Git 仓库或用户身份不可用，系统状态、Job 和 `git.commit_failed` 事件会暴露可理解错误。

## 8. LLM 与 Embedding Provider 配置

不配置 API Key 时：

1. `llm.api_key` 为空时，摘要、专题缝合和 Compose 整理使用本地规则 provider。
2. `embedding.api_key` 为空时，生成 deterministic local embedding。
3. 本地采集、加工、搜索、专题匹配和 Compose 整理仍可运行。

配置 OpenAI-compatible chat provider：

```toml
[llm]
base_url = "https://api.openai.com"
api_key = "..."
chat_model = "gpt-4o-mini"
timeout_seconds = 30
```

结构化流程默认发送 `response_format: {"type":"json_object"}`，需要所选模型与 Provider 支持；纯文本流程不发送此参数。上游返回 `conversion_unsupported` 时，保留 `feature` / `unsupported_features` 诊断，并停止原样重试。这属于能力或参数错误，不是账号失效或临时故障。

JSON 兼容 fallback 内置自动启用，无需配置参数。只有 `conversion_unsupported` 明确且仅涉及 `response_format`（含其子功能）时，才移除该参数进入兼容通道；未指明功能、普通 400 或涉及其他功能均不降级。

首次确认不支持后，同一服务地址、API 凭据和模型的所有 Provider 实例共享进程内降级状态，后续 JSON 调用直接省略 `response_format`。首次并发调用协调探测；已确认支持后正常并行调用。更换地址、凭据或模型使用独立状态；重启服务清空状态并重新探测。

聊天生成关闭 SDK 通用重试。只有明确格式能力拒绝允许一次不带参数的兼容调用；一般网络错误、HTTP 429/5xx、发送后断线和读超时均不会自动原样重发。它们不能证明上游未开始生成；保留输入与错误，用户可稍后显式重试。网关 502 中出现上游 403 仍按网关故障分类，持续访问拒绝需要在上游处理，不自动刷新账号或切换模型。Embedding 的独立重试策略保持原样。

能力等待、格式协商、兼容调用及业务允许的内容修复共享操作截止时间，取消不续期。缓存最多 256 项，只回收无在途操作的闲置项；Provider 每次操作获取共享状态，避免旧实例和新实例分裂探测。未被容量回收的同配置降级判断持续沿用，无周期 TTL；被回收的身份下次调用会重新协商。

移除参数不等于获得 JSON 格式保证。返回须通过原有解析和业务校验；空回答、截断、字段类型或内容错误不作为成功输出。归档持久化生成开始标记，重启时不重新生成结果未知的任务，已持久化的保存命令仍可幂等恢复。日志关联本地会话/任务与响应 `X-Request-ID`、网关错误码及 `failure_class`，不记录凭据或提示词。`Retryable` 表示之后可考虑重试，与允许立即自动重发分开判断。

Capture 多轮对话的 system prompt 可以按文件加载，用于覆盖 Profile 匹配与上下文抽取协议。未配置时使用内置 prompt（源码位置：`assets/prompts/capture_context_system.md`）；相对路径会按配置目录解析：

```toml
[llm.prompts]
capture_context_system_path = "prompts/capture-context-research.md"
```

自定义 prompt 必须返回非空 `assistant_reply`，作为当前轮完整的对话回答；缺失或空白按协议解析错误处理，容错补发后的无效结果直接终止。`candidate_summary` 仅用于归档摘要，`candidate_body` 保存累计工作材料，不能用这两个字段替代本轮回答。研究、解释和追问应直接提供实质内容；本次上下文生成调用没有联网检索工具，不能据此声称已检索或核验标准原文。未配置对话模型时，本地规则仅整理输入并明确提示能力限制。

该 prompt 不是扩充文档类型的正式入口。新增调研报告、设计文档或团队模板时，应在 `document-formats/drafts` 编写 YAML front matter + 受限 Markdown 模板，经 validate/publish 后写入 `document-formats/published/<profile-id>/vN.md`。发布版本不可覆盖，Capture 和 Compose 会通过同一 Registry、Renderer 和 Validator 使用它。

配置 OpenAI-compatible embedding provider：

```toml
[embedding]
base_url = "https://api.openai.com"
api_key = "..."
model = "text-embedding-3-small"
timeout_seconds = 30
```

## 9. 常用 API

所有 JSON API 响应统一包含：

```json
{
  "request_id": "...",
  "data": {},
  "error": null
}
```

常用入口：

```text
POST /api/thoughts
GET  /api/thoughts/{id}
POST /api/thoughts/{id}/retry-refine
POST /api/thoughts/{id}/reopen-session

# 对外内容 URL（始终注册，handler 根据 server.public_thoughts_enabled 动态允许或拒绝；供外部搜索 / 爬取 / RAG 直接 GET 全文；默认 text/markdown，无 API envelope）
# ?format=html|json 或 Accept 协商；UI「分享链接」复制 {public_base_url}/p/thoughts/{id}
GET  /p/thoughts/{id}

POST /api/capture/sessions
GET  /api/capture/sessions/active
GET  /api/capture/sessions
POST /api/capture/sessions/{id}/messages
POST /api/capture/sessions/{id}/context
POST /api/capture/sessions/{id}/profile
GET  /api/capture/sessions/{id}/archive/preview
POST /api/capture/sessions/{id}/archive

GET  /api/search
POST /api/system/reindex

GET  /api/topics
POST /api/topics
GET  /api/topics/{id}
PUT  /api/topics/{id}
POST /api/topics/{id}/refresh
GET  /api/topics/{id}/candidates
POST /api/topics/{id}/weave-preview
GET  /api/topics/{id}/weave-proposals
GET  /api/topics/{id}/weave-proposals/{proposal_id}
POST /api/topics/{id}/weave-accept

GET  /api/compose/sources
PUT  /api/compose/sources
DELETE /api/compose/sources

POST /api/compose/drafts
GET  /api/compose/drafts
GET  /api/compose/drafts/{draft_id}
POST /api/compose/drafts/{draft_id}/save

GET  /api/document-profiles
GET  /api/document-profiles/{id}?version=N
POST /api/document-profiles/validate
POST /api/document-profiles/publish
POST /api/document-profiles/reload

GET  /api/jobs
GET  /api/jobs/{id}
GET  /api/events
GET  /api/system/status
GET  /api/system/metrics
GET  /metrics
GET  /health/live
GET  /health/ready
```

### 合稿素材与保存重试

`GET /api/compose/sources` 返回 `version`。替换素材的 `PUT` 和清空素材的 `DELETE` 必须携带该版本，例如 `If-Match: "3"`；缺少版本返回 428，版本过期返回 409，客户端应重新读取素材后再操作。旧版 `basket.yaml` 没有版本字段时从 0 开始，首次更新自动写入版本。生成完成只原子移除本次使用的素材；保存生成文稿为笔记不会再清空整个素材篮。同一标签页的素材刷新和保存共用串行队列，并以本地修改序号阻止在途 GET 覆盖后来新增、移除或清空的素材。

同一草稿的保存和删除按草稿 ID 串行执行。第一次保存会先在草稿 YAML 的 `save_command` 中持久化目标 Thought ID 和保存命令，再创建 Markdown；若笔记已创建但草稿状态写入失败，重启后重试会复用该 ID 和首次保存内容，不重复建笔记，也不覆盖笔记后续的编辑。已标为 `saved` 的草稿继续拒绝重复保存。旧草稿不需要手动迁移。锁在单个服务进程中协调同一工作区的请求；不要让多个 ThoughtFlow 进程同时写同一工作区。

URL 的 refiner 和 expander 共用 Reader 配置。`enabled=false` 时禁止 Reader fallback；开启时使用配置的 `base_url`、`api_key` 与 `timeout`，API key 仅发送给 Reader。Git 自动提交包含本次请求路径的新增、修改和删除，并保留用户预先暂存的无关内容。

## 11. 验证与 CI

本地日常开发验证：

```bash
make check
```

远端提交完整验证（含浏览器 smoke 与 e2e）：

```bash
make ci-check
```

本机 DuckDB tagged 测试如需指定 C++ 链接路径：

```bash
make check CGO_LDFLAGS=-L/tmp
```

GitHub Actions 位于：

```text
.github/workflows/ci.yml
```

CI 执行：

```bash
make ci-check
```

CI 会安装 Go `1.24.x`、Node `22.x` 和 `build-essential`，并检查 Chrome/Chromium 是否可用。日常本地调试不默认执行 browser/e2e，避免不必要的大范围端到端验证。

## 12. 当前验证限制

嵌入式 UI browser smoke 矩阵声明 Chrome、Firefox、Safari 三类目标。当前 Linux 环境实际执行 Chrome desktop/mobile；Firefox 未安装或 Safari/WebKit 不可用时，对应 subtest 会以明确原因 skip。

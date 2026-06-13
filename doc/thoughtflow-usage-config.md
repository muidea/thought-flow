# ThoughtFlow 使用及配置说明

本文说明 ThoughtFlow 的本地启动、工作区、配置优先级、常用 API、验证命令和 CI 行为。

## 1. 环境要求

本地开发和验证建议准备：

1. Go `1.24.x`。
2. Node.js `22.x`，用于嵌入式前端语法检查、组件测试和 API/SSE 端到端测试。
3. Git，用于自动提交 Markdown 变更。
4. C++ 链接环境，用于 `duckdb` build tag 测试。

默认构建使用 fallback search store，可以不依赖 DuckDB CGO 链接环境。需要验证 DuckDB 实现时运行 `make test-duckdb` 或 `make check`。

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

默认工作区是：

```text
./thoughtflow-workspace
```

首次启动会创建：

```text
thoughts/              原子笔记 Markdown
topics/                专题 YAML、专题 Markdown、membership 和审批草稿
attachments/           附件目录
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

[workspace]
content_dir = "./thoughtflow-workspace"
auto_init_git = true

[runtime]
state_dir = "./thoughtflow-runtime"

[capture]
duplicate_policy = "warn"

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
3. `llm.api_key` 为空时，摘要、专题缝合和 Compose 整理使用本地规则 provider。
4. `embedding.api_key` 为空时，服务使用 deterministic local embedding，仍可完成本地采集、搜索和专题匹配。
5. `workspace.auto_init_git` 当前是配置模型字段；实际提交能力由 `git_sync.enabled` 和本机 Git 仓库/身份状态决定。
6. 配置目录和运行状态目录应保持物理分离。配置目录存放 `application.toml`，运行状态目录存放 jobs、logs、DuckDB 等运行态文件。

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

POST /api/capture/sessions
GET  /api/capture/sessions/active
GET  /api/capture/sessions
POST /api/capture/sessions/{id}/messages
POST /api/capture/sessions/{id}/context
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

POST /api/compose/drafts
GET  /api/compose/drafts
GET  /api/compose/drafts/{draft_id}
POST /api/compose/drafts/{draft_id}/save

GET  /api/jobs
GET  /api/jobs/{id}
GET  /api/events
GET  /api/system/status
GET  /api/system/metrics
GET  /metrics
GET  /health/live
GET  /health/ready
```

## 11. 验证与 CI

本地完整验证：

```bash
make check
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
make check
```

CI 会安装 Go `1.24.x`、Node `22.x` 和 `build-essential`，并检查 Chrome/Chromium 是否可用。

## 12. 当前验证限制

Web 端关键路径覆盖由 Node 组件测试（`make node-test`）和 API/SSE 端到端测试（`make e2e-test`）承担；浏览器 smoke 矩阵（`make browser-test`）在 2026-06-13 收口中删除。

# ThoughtFlow 使用及配置说明

本文说明 ThoughtFlow 的本地启动、工作区、配置优先级、常用 API、验证命令和 CI 行为。

## 1. 环境要求

本地开发和验证建议准备：

1. Go `1.24.x`。
2. Node.js `22.x`，用于嵌入式前端语法检查、组件测试和浏览器 smoke 测试。
3. Git，用于自动提交 Markdown 变更。
4. Google Chrome 或 Chromium，用于 `make browser-test`。
5. C++ 链接环境，用于 `duckdb` build tag 测试。

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

指定监听地址和工作区：

```bash
./thoughtflow \
  --host 0.0.0.0 \
  --port 9090 \
  --workspace-root /data/thoughtflow
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
.thoughtflow/jobs/     后台 Job 快照
.thoughtflow/logs/     运行日志目录
.thoughtflow/*.duckdb  搜索索引数据库
```

持久事实源以 `thoughts/` 和 `topics/` 下的 Markdown/YAML 为主；`.thoughtflow/` 是本地运行态目录，不应作为长期事实源提交。

## 4. 配置加载顺序

配置加载顺序为：

1. 内置默认值。
2. 工作区本地配置：`<workspace>/.thoughtflow/config.local.yaml`。
3. 环境变量。
4. CLI 参数。CLI 参数会先映射为对应环境变量，再进入统一配置加载。

用于定位本地配置文件的 workspace root 来自 `THOUGHTFLOW_WORKSPACE_ROOT`；未设置时使用默认 `./thoughtflow-workspace`。

## 5. 本地配置样例

示例路径：

```text
thoughtflow-workspace/.thoughtflow/config.local.yaml
```

示例内容：

```yaml
server:
  host: "127.0.0.1"
  port: "8080"

workspace:
  root: "./thoughtflow-workspace"
  auto_init_git: true

capture:
  duplicate_policy: "warn"

refiner:
  concurrency: 2
  url_fetch_timeout_seconds: 30

git_sync:
  enabled: true
  debounce_seconds: 5

search:
  duckdb_path: ".thoughtflow/thoughtflow.duckdb"
  default_mode: "hybrid"

topic:
  auto_weave: true
  min_semantic_score: 0.78

events:
  sse_heartbeat_seconds: 20

ai:
  base_url: "https://api.openai.com"
  api_key: ""
  chat_model: "gpt-4o-mini"
  embedding_model: "text-embedding-3-small"
  timeout_seconds: 30
```

说明：

1. `search.duckdb_path` 为相对路径时，会解析到 workspace root 下。
2. `ai.api_key` 为空时，服务使用本地规则 provider，仍可完成本地采集、摘要、embedding、搜索、专题匹配和合稿。
3. `workspace.auto_init_git` 当前是配置模型字段；实际提交能力由 `git_sync.enabled` 和本机 Git 仓库/身份状态决定。

## 6. 环境变量

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `THOUGHTFLOW_HOST` | `127.0.0.1` | HTTP 监听 host |
| `THOUGHTFLOW_PORT` | `8080` | HTTP 监听端口 |
| `THOUGHTFLOW_WORKSPACE_ROOT` | `./thoughtflow-workspace` | 工作区根目录 |
| `THOUGHTFLOW_AUTO_INIT_GIT` | `true` | 工作区 Git 初始化策略配置字段 |
| `THOUGHTFLOW_GIT_ENABLED` | `true` | 是否启用 git-sync 运行单元 |
| `THOUGHTFLOW_GIT_DEBOUNCE_SECONDS` | `5` | 自动提交 debounce 秒数 |
| `THOUGHTFLOW_DUCKDB_PATH` | `.thoughtflow/thoughtflow.duckdb` | DuckDB 索引路径，相对路径基于 workspace |
| `THOUGHTFLOW_AI_BASE_URL` | `https://api.openai.com` | OpenAI-compatible API base URL |
| `THOUGHTFLOW_AI_API_KEY` | 空 | AI API Key，空值时使用本地规则 provider |
| `THOUGHTFLOW_AI_CHAT_MODEL` | `gpt-4o-mini` | 摘要、合稿和专题缝合使用的 chat model |
| `THOUGHTFLOW_AI_EMBEDDING_MODEL` | `text-embedding-3-small` | embedding model |
| `THOUGHTFLOW_AI_TIMEOUT_SECONDS` | `30` | AI 请求超时 |

## 7. CLI 参数

CLI 参数与环境变量一一对应：

| 参数 | 对应环境变量 |
| --- | --- |
| `--host` | `THOUGHTFLOW_HOST` |
| `--port` | `THOUGHTFLOW_PORT` |
| `--workspace-root` | `THOUGHTFLOW_WORKSPACE_ROOT` |
| `--auto-init-git` | `THOUGHTFLOW_AUTO_INIT_GIT` |
| `--git-enabled` | `THOUGHTFLOW_GIT_ENABLED` |
| `--git-debounce-seconds` | `THOUGHTFLOW_GIT_DEBOUNCE_SECONDS` |
| `--duckdb-path` | `THOUGHTFLOW_DUCKDB_PATH` |
| `--ai-base-url` | `THOUGHTFLOW_AI_BASE_URL` |
| `--ai-api-key` | `THOUGHTFLOW_AI_API_KEY` |
| `--ai-chat-model` | `THOUGHTFLOW_AI_CHAT_MODEL` |
| `--ai-embedding-model` | `THOUGHTFLOW_AI_EMBEDDING_MODEL` |
| `--ai-timeout-seconds` | `THOUGHTFLOW_AI_TIMEOUT_SECONDS` |

完整示例：

```bash
./thoughtflow \
  --host 0.0.0.0 \
  --port 9090 \
  --workspace-root /data/thoughtflow \
  --git-enabled true \
  --git-debounce-seconds 5 \
  --duckdb-path .thoughtflow/thoughtflow.duckdb \
  --ai-base-url https://api.openai.com \
  --ai-api-key "$OPENAI_API_KEY" \
  --ai-chat-model gpt-4o-mini \
  --ai-embedding-model text-embedding-3-small \
  --ai-timeout-seconds 30
```

## 8. Git Sync 配置

启用 Git 自动提交：

```yaml
git_sync:
  enabled: true
  debounce_seconds: 5
```

提交前需要 workspace 是 Git 仓库，并配置用户身份：

```bash
git -C thoughtflow-workspace init
git -C thoughtflow-workspace config user.name "ThoughtFlow"
git -C thoughtflow-workspace config user.email "thoughtflow@example.local"
```

git-sync 会过滤 `.thoughtflow/` 和 DuckDB 文件，只提交用户可读的工作区内容。若 Git 仓库或用户身份不可用，系统状态、Job 和 `git.commit_failed` 事件会暴露可理解错误。

## 9. AI Provider 配置

不配置 API Key 时：

1. 使用本地规则 provider。
2. 生成 deterministic local embedding。
3. 本地采集、加工、搜索、专题匹配和合稿仍可运行。

配置 OpenAI-compatible provider：

```bash
export THOUGHTFLOW_AI_BASE_URL="https://api.openai.com"
export THOUGHTFLOW_AI_API_KEY="..."
export THOUGHTFLOW_AI_CHAT_MODEL="gpt-4o-mini"
export THOUGHTFLOW_AI_EMBEDDING_MODEL="text-embedding-3-small"
./thoughtflow
```

## 10. 常用 API

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
GET  /api/search
POST /api/system/reindex

GET  /api/topics
POST /api/topics
GET  /api/topics/{id}
PUT  /api/topics/{id}
POST /api/topics/{id}/rebuild
POST /api/topics/{id}/weave-preview
GET  /api/topics/{id}/weave-proposals
GET  /api/topics/{id}/weave-proposals/{proposal_id}
POST /api/topics/{id}/weave-accept

POST /api/synthesis
GET  /api/synthesis
GET  /api/synthesis/{draft_id}
POST /api/synthesis/save

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

嵌入式 UI browser smoke 矩阵声明 Chrome、Firefox、Safari 三类目标。当前 Linux 环境实际执行 Chrome desktop/mobile；Firefox 未安装或 Safari/WebKit 不可用时，对应 subtest 会以明确原因 skip。

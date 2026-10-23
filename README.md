# ThoughtFlow

ThoughtFlow 是一个本地优先的碎片笔记采集、加工、检索、专题缝合与合稿服务。

核心特性：

- 单二进制本地服务，入口为 `cmd/thoughtflow`。
- 原始笔记和专题文档落地为 Markdown。
- 运行态数据默认位于 `./thoughtflow-runtime/`，采集会话私有侧文件仍在内容根的 `.scratchpad/`。
- 支持 REST API、SSE 事件流和嵌入式原生 HTML/CSS/JS UI。
- 默认无 AI Key 也可运行；配置 OpenAI-compatible provider 后启用模型摘要、embedding、合稿和专题缝合。
- 所有构建使用 DuckDB，需启用 CGO 及 C/C++ 链接环境；没有 fallback store 或可选 DuckDB build tag。

## 快速启动

```bash
make build
./thoughtflow
```

访问：

```text
http://127.0.0.1:8080/
```

默认工作区：

```text
./thoughtflow-workspace
```

## 常用命令

```bash
make help          # 查看全部目标
make test          # 默认 Go 测试
make build         # 构建 ./thoughtflow
make node-check    # 前端 JS 语法检查
make node-test     # 前端 Node 组件测试
make check         # 日常开发轻量验证
make ci-check      # 远端提交完整验证，含 browser/e2e
```

本机需安装 C/C++ 工具链。Makefile 会探测 `libstdc++.so` 开发链接；最小 Linux 环境缺少开发链接时，会尝试在 `build/libstdcxx/` 建立指向已安装运行库的链接。

## 文档

- [产品需求](doc/thoughtflow-prd.md)
- [功能设计](doc/thoughtflow-functional-design.md)
- [业务模型定义](doc/thoughtflow-domain-models.md)
- [实现状态](doc/thoughtflow-implementation-status.md)
- [使用及配置说明](doc/thoughtflow-usage-config.md)
- [配置文件模板](doc/application.example.toml)
- [Web 交互与展示风格改造设计](doc/thoughtflow-web-ux-redesign.md)

## CI

GitHub Actions 位于 `.github/workflows/ci.yml`，会执行：

```bash
make ci-check
```

CI 环境使用 Go `1.24.x`、Node `22.x`，并安装 `build-essential` 用于 DuckDB 的 CGO 构建与测试。浏览器 smoke 与 e2e 默认只在远端 CI 完整矩阵中执行。

## 发布版本

`v*` tag 推送后，`.github/workflows/release.yml` 会自动跨平台编译并上传到 GitHub Release：

| 平台 | 产物 |
|---|---|
| linux/amd64 | `thoughtflow-linux-amd64.tar.gz` |
| linux/arm64 | `thoughtflow-linux-arm64.tar.gz` |
| darwin/arm64 | `thoughtflow-darwin-arm64.tar.gz` |

下载安装示例：

```bash
# Linux x86_64
curl -L https://github.com/muidea/thought-flow/releases/download/v0.0.1/thoughtflow-linux-amd64.tar.gz | tar -xz
./thoughtflow -config-dir ./config

# macOS Apple Silicon
curl -L https://github.com/muidea/thought-flow/releases/download/v0.0.1/thoughtflow-darwin-arm64.tar.gz | tar -xz
./thoughtflow -config-dir ./config
```

`-config-dir` 只定位配置文件。默认内容目录 `./thoughtflow-workspace` 和运行目录 `./thoughtflow-runtime` 均相对于启动时的工作目录；不会自动放到配置目录下。可在 `application.toml` 中设置 `workspace.content_dir` 与 `runtime.state_dir` 的绝对路径。


## 当前能力与数据边界

- Web 提供关键词和标签检索；`GET /api/search` 还支持精确专题过滤。语义与混合搜索目前通过内部 Service 使用，主 HTTP 搜索入口未开放模式选择。
- 笔记向量只使用标题、摘要与标签，不发送正文，也不做全文分块。全文仍用于关键词检索与大模型提炼。生成约束、长度上限、缓存与重建规则见 [元数据向量策略](doc/ThoughtFlow-Metadata-Embedding-2026-10-23.md)。
- 专题提案支持 API 预览、读取和接受；Web 尚无完整的提案审核流程。刷新保留已接受正文，来源更新需要重新审阅，不自动重写人工确认内容。
- Git 功能是本地自动提交，支持持久化待提交集合和失败重试；没有 remote push/pull、合并或多设备同步。存在未解决冲突时保留待提交集合，等待用户解决冲突。
- 无 API key 使用本地规则与本地向量。处理 URL 仍会联网请求原站/GitHub；Reader 默认启用，可在没有 key 时向其端点发送完整 URL。设置 `[reader] enabled=false` 只禁用 Reader fallback。设置页提供实际外发配置，敏感 URL 应先移除凭据和查询参数。
- 类型化文档的结构检查与模型语义审查有明确边界；定稿只代表对应冻结版本完成规定检查，不保证事实正确。自定义格式支持 H2–H6。
- 当前按一个本地服务进程管理一个工作区设计；进程内锁与浏览器跨标签提示不构成多进程或多用户协作协议。不要让两个服务实例同时写同一工作区。
- 默认监听 loopback。公开内容开关允许匿名读取启用的固定内容；服务没有通用多用户认证/授权。远程部署需要额外的认证与网络边界，Origin/Host/CSRF 仍需专项验收。
- 备份需同时考虑内容目录和运行目录：Markdown、不可变文档 journal、模板、专题属于内容；草稿、任务、Git 待提交集合与证据等位于运行目录或私有侧文件。可重建搜索索引不等于整个运行目录都可删除。
- 工作区自动维护 `.gitignore`，排除会话、候选和运行目录。忽略规则不会移除已跟踪文件；手工备份、Git 远程发布和公开开关仍需按实际数据范围决定。

本次逐项修复与验证见 [全功能审计修复记录](doc/ThoughtFlow-Full-Function-Audit-Closure-2026-10-22.md)。旧资料迁移 A20 按当前开发阶段不考虑兼容性的要求豁免。

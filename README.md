# ThoughtFlow

ThoughtFlow 是一个本地优先的碎片笔记采集、加工、检索、专题缝合与合稿服务。

核心特性：

- 单二进制本地服务，入口为 `cmd/thoughtflow`。
- 原始笔记和专题文档落地为 Markdown。
- 运行态数据放在工作区 `.thoughtflow/` 下。
- 支持 REST API、SSE 事件流和嵌入式原生 HTML/CSS/JS UI。
- 默认无 AI Key 也可运行；配置 OpenAI-compatible provider 后启用模型摘要、embedding、合稿和专题缝合。
- 默认构建使用 fallback search store；`duckdb` build tag 启用 DuckDB 搜索实现。

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
make test-duckdb   # duckdb build tag 测试
make build         # 构建 ./thoughtflow
make node-check    # 前端 JS 语法检查
make node-test     # 前端 Node 组件测试
make browser-test  # 嵌入式 UI Chrome smoke 测试
make check         # 完整验证矩阵
```

本机运行 DuckDB tagged 测试时，如缺少 `libstdc++.so` 开发链接名，可使用：

```bash
make check CGO_LDFLAGS=-L/tmp
```

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
make check
```

CI 环境使用 Go `1.24.x`、Node `22.x`，并安装 `build-essential` 用于 DuckDB tagged 测试。

# ThoughtFlow (思流) 产品需求文档 (PRD)

## 1. 产品定位
**ThoughtFlow** 是一款基于 AI 的轻量化知识整理应用。它能够捕捉日常零散的想法和参考网址，通过 AI 进行语义分析与分类，自动将其整合进动态增长的专题知识体系中，并支持秒级的即席检索与知识回溯。

*   **核心价值**：从“随意记录”到“体系产出”的自动化流水线。
*   **部署目标**：极致轻量，单文件部署，本地优先。

---

## 2. 核心技术架构 (Architecture)
*   **开发语言**：Golang (高性能并行处理，单二进制文件)。
*   **基础应用框架**：使用 [`magicCommon/framework`](https://github.com/muidea/magicCommon) 作为应用生命周期与运行单元框架。
    *   统一承载模块注册、`Setup` / `Run` / `Teardown` 生命周期、EventHub 事件协同、BackgroundRoutine 后台任务、配置管理与监控能力。
    *   入口层显式选择启用的运行单元，避免业务包通过隐式 import 触发模块注册。
*   **网络服务框架**：使用 [`magicEngine`](https://github.com/muidea/magicEngine) 作为 HTTP 与实时通信框架。
    *   用于实现 REST API、路由注册、中间件、静态资源服务、文件上传能力。
    *   预留 SSE 能力，用于异步加工进度、索引状态、Git 同步状态等实时推送。
*   **持久化层**：
    *   **存储格式**：纯 Markdown 文件 (Markdown-first)，人类可读。
    *   **版本控制**：集成 Git，实现全自动的变更追踪与多端同步。
*   **索引/分析层**：
    *   **数据库**：DuckDB (嵌入式 OLAP 数据库)。
    *   **检索技术**：混合搜索 (关键词 FTS + 向量 Embedding)。
*   **AI 引擎**：接入大模型 API (DeepSeek/OpenAI 兼容格式)。

### 2.1 后端运行单元划分
基于 `magicCommon/framework` 将后端拆分为可独立装配的运行单元，保障轻量部署下的清晰边界：

*   **capture**：负责文本/URL 采集、原始 Markdown 落盘、采集事件发布。
*   **refiner**：负责网页正文清洗、AI 摘要、标签生成、Embedding 生成等后台加工任务。
*   **topic**：负责专题规则、专题命中判断、专题主文档智能缝合与手动重组。
*   **search**：负责 DuckDB 索引、FTS/向量混合检索、检索结果聚合。
*   **git-sync**：负责自动 `git add / commit`、变更记录、后续多端同步扩展。
*   **application**：负责将上述能力暴露为 `magicEngine` HTTP API 和静态 UI 入口，不直接持有其他运行单元的正式状态。

运行单元之间通过 EventHub 或明确的窄接口协作；正式业务状态由所属运行单元维护，跨单元写入和状态变更必须走事件命令或应用服务编排。

### 2.2 API 与服务边界
`magicEngine` 负责对外服务层，`magicCommon/framework` 负责应用内部生命周期和模块协同：

*   **HTTP API**：
    *   `POST /api/thoughts`：提交文本或 URL。
    *   `GET /api/thoughts/{id}`：查看原子笔记与元数据。
    *   `GET /api/search`：执行关键词、语义或混合检索。
    *   `POST /api/topics` / `PUT /api/topics/{id}`：创建与维护专题规则。
    *   `POST /api/topics/{id}/rebuild`：手动触发专题重组。
*   **实时接口**：
    *   `GET /api/events`：基于 SSE 推送采集处理进度、索引状态、专题更新结果与 Git 提交结果。
*   **服务层约束**：
    *   `magicEngine` handler 只处理请求解析、鉴权、响应格式化和错误映射。
    *   业务流程进入对应运行单元的 service/biz 层，不在 handler 中直接操作 Markdown、DuckDB 或 Git。
    *   长耗时任务必须提交给 `magicCommon` BackgroundRoutine，HTTP 请求只返回任务受理状态或当前结果快照。

---

## 3. 功能需求 (Functional Requirements)

### 3.1 极简采集 (The Capture)
*   **文本想法**：支持随手记下的短句、段落。
*   **URL 采集**：
    *   系统自动使用 `Colly/Jina Reader` 爬取正文。
    *   AI 自动提炼网页摘要、核心观点及元数据。
*   **原子化存储**：每条录入生成一个带有 YAML 元数据的独立 Markdown 文件，存入 `thoughts/` 目录。

### 3.2 AI 智能加工 (The Refiner)
*   **自动标签**：AI 识别内容并打上多维度标签。
*   **向量化 (Embedding)**：生成语义向量，存入 DuckDB，为关联分析做准备。
*   **异步处理**：利用 `magicCommon` BackgroundRoutine 执行后台非阻塞加工，确保录入体验秒级响应。
*   **进度事件**：加工状态通过 EventHub 发布，并由 `magicEngine` SSE 接口推送给前端。

### 3.3 多专题管理与自动进化 (Topic System)
*   **专题创建**：用户可定义多个专题（如：AI 研究、烹饪心得、投资笔记）。
*   **匹配规则**：每个专题支持设置关键词触发和语义相似度阈值。
*   **自动路由与更新**：
    *   当新碎片命中专题规则时，AI 自动将其整合进专题主文档 (`topics/{name}/index.md`)。
    *   **智能缝合**：AI 负责寻找合适的章节插入内容，而非简单的末尾追加。
*   **管理界面**：支持专题的创建、规则修改、大纲维护及手动重组触发。

### 3.4 即席检索与展示 (Ad-hoc Retrieval)
*   **混合搜索**：支持 `Ctrl+K` 唤起搜索框。
    *   **精确匹配**：基于 DuckDB 的 FTS 索引进行关键词搜索。
    *   **语义探索**：基于向量相似度查找“意思相近”的想法。
*   **即时合稿 (Synthesis-on-the-fly)**：
    *   支持在检索结果中手动勾选多个碎片。
    *   AI 即时生成针对选中内容的总结报告或逻辑大纲。
*   **溯源展示**：所有搜索结果和专题内容均可反向链接到最原始的原子笔记。

### 3.5 自动化 Git 工作流 (Git Integration)
*   **自动提交**：每次录入、专题更新后，系统自动执行 `git add / commit`。
*   **透明化记录**：Git Commit 记录作为知识进化的“心路历程”。
*   **任务化执行**：Git 操作作为 `git-sync` 运行单元的后台任务执行，失败时通过事件通知 UI，不阻塞采集主流程。

---

## 4. 界面需求 (UI Requirements)

### 4.1 专题管理大盘 (Topic Dashboard)
*   展示所有专题卡片、统计数据（碎片数、字数）及最近活跃度。
*   提供专题创建引导视图。

### 4.2 智能检索中心 (Search Hub)
*   响应式搜索界面，支持实时过滤。
*   搜索结果侧边栏预览功能。

### 4.3 专题详情工作台 (Topic Workspace)
*   **左侧**：专题体系化文档预览（Markdown 渲染）。
*   **右侧**：配置面板（规则设置、大纲编辑）。

---

## 5. 非功能性需求 (Non-Functional Requirements)

*   **轻量化部署**：
    *   无需安装大型数据库中间件。
    *   单二进制执行文件，内存占用在空闲时应低于 50MB。
*   **数据安全与隐私**：
    *   数据存储在本地 Git 仓库，用户拥有绝对所有权。
    *   仅 Embedding 和总结请求发送至云端 API。
*   **扩展性**：
    *   生成的 Markdown 目录结构需完美兼容 Obsidian 和 Logseq。
    *   新增采集源、AI Provider、检索策略和同步后端时，优先以 `magicCommon/framework` 运行单元或 focused package 扩展，避免堆叠到单一服务。
*   **可维护性**：
    *   后端入口保持单二进制启动，框架模块由入口显式装配。
    *   配置按全局配置与模块配置拆分，AI Key、DuckDB 路径、Git 策略、HTTP 端口均通过 `application.toml` 定义。
    *   关键任务需要暴露结构化日志和基础监控指标，便于定位采集失败、AI 调用失败、索引延迟和 Git 提交失败。

---

## 6. 核心业务流程 (Data Flow)

1.  **输入**：用户输入 `URL/文本` -> `magicEngine` HTTP API 接收请求。
2.  **采集**：`application` 调用 `capture` 运行单元 -> 生成原子 Markdown -> 发布采集事件。
3.  **加工**：`refiner` 订阅事件或接收后台任务 -> 爬虫抓取 -> AI 摘要/向量化 -> 更新 `thoughts/` 元数据。
4.  **路由**：`search` 更新 DuckDB 索引 -> `topic` 查询匹配规则 -> 命中的专题触发 LLM 缝合任务 -> 更新 `topics/` 目录。
5.  **持久化**：`git-sync` 接收变更事件 -> 执行 Git 自动提交 -> 发布提交结果事件。
6.  **消费**：用户通过 UI 搜索（即席检索）或查阅专题文档；前端通过 SSE 获取后台任务进度。

---

**PRD 对齐状态确认**：
*   [x] 语言：Golang
*   [x] 框架：magicCommon/framework + magicEngine
*   [x] 存储：DuckDB + 纯文件 + Git
*   [x] 功能：碎片采集、AI 加工、多专题自动更新、即席检索展示
*   [x] 部署：轻量化、避免重型资源占用

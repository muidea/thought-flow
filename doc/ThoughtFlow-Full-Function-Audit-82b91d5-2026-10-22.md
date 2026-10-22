# ThoughtFlow 最新代码全量功能核对报告

- 日期：2026-10-22（UTC+08:00）
- 仓库：<https://github.com/muidea/thought-flow>
- 固定基线：[`82b91d5e3abaee0de70a975d88925b56b7a6f401`](https://github.com/muidea/thought-flow/commit/82b91d5e3abaee0de70a975d88925b56b7a6f401)
- 最新提交主题：`fix(documents): allow system path aliases above storage roots`
- 本轮范围：重新盘点当前全部主要功能域及注册入口，审阅模块装配、业务实现、数据流、前后端连接与测试。不是仅检查最近 diff；也不是宣称穷尽所有输入或完成生产环境认证。
- 操作边界：只读评审；未修改项目源码，未提交、部署，未调用用户的模型账号，未修改用户工作区数据。

## 一、总评

**当前项目已具备较完整的本地知识采集、整理、专题、合稿和类型化文档框架，但尚不能按“全功能已收口”验收。**

主要风险不是缺少入口，而是跨模块实际内容、身份、状态和事件不能始终保持一致：

1. 专题正式正文可能被无关 Compose 事件隐式重建，人工编辑有丢失风险。
2. 专题投递目录路径，与 Git 的只允许具体文件规则冲突，阻断自动版本保存。
3. URL 会话归档字段不完整；网页提炼虽然抓取了正文，却可能把 URL 字符串交给模型。
4. Compose 接受多种来源，但非 Thought 来源没有把真实内容送入生成流程。
5. 前端缺少部分异步请求的会话/版本归属校验，旧响应可覆盖当前新会话状态。
6. 类型化文档主链路已经建立，但外部 Markdown 身份、实际链接节点、候选来源和并发模板发布仍有校验缺口。
7. 索引、专题、后台任务和 SSE 有跨功能一致性、恢复及并发缺口，不能只靠已有组件测试判断闭环。

上一轮“3 项 P2 已关闭”的结论仍然有效；本轮是扩大到全项目后的新发现，不是撤回此前限定范围的修复结论。删除撤销、标题转义、已修正的参考资料识别、括号 URL、首次投影恢复及语义人工确认等修复应保留。

### 优先级含义

- **P1**：正常主流程阻断，或存在数据覆盖、隐私边界破坏等高影响问题，应先修再宣称全量收口。
- **P2**：特定配置、并发、失败恢复或交互情形下的正确性问题，应列入当前功能验收。
- **边界/建议**：明确未实现的能力、需产品决策的范围或尚未动态证实的鲁棒性风险，不冒充已复现漏洞。

所有后端发现都注明为源码/调用链证据或并发推演；没有把未执行的 Go 测试写成成功。前端复现结果另列。

## 二、全功能概览

| 功能域 | 当前实际能力 | 总体判断 |
|---|---|---|
| 启动与配置 | 单二进制、独立配置目录、工作区/运行目录分离、信号关闭 | 已实现；部署文档部分过时 |
| 文本笔记 | 创建、读取、列表、编辑、删除、重复告警 | 主链路已实现；后台并发与外部身份边界待完善 |
| 对话采集 | 会话、上下文、预览、归档、新建/更新/补充、重新整理 | 已实现；URL 归档与前端切换存在阻断 |
| 网页读取 | 目标站点抓取、GitHub README、Reader fallback、开关及凭据隔离 | 已实现；不是浏览器渲染，也不是默认离线 |
| 摘要与扩展 | 本地/远程摘要、标签、要点、关联、计划与网页 followup | 部分闭环；正文选择和阶段失败隔离需修复 |
| 搜索 | 实际使用 DuckDB；关键词与标签检索、索引重建、内部语义能力 | 当前 HTTP 主入口固定关键词模式；恢复、过滤和 embedding 版本不完整 |
| 专题 | 规则、成员、候选、自动缝合、预览/接受提案、刷新 | 功能丰富；正文保护与并发一致性为最高风险 |
| 合稿 | 来源篮、版本冲突、异步草稿、保存、冻结输入与重启处理 | Thought 来源主链路较完整；其他来源内容丢失 |
| 文档格式 | 内置 note/design/research/blog、自定义格式、校验、发布/重载 | 已实现；并发发布和非 H2 修复存在缺口 |
| 类型化交付 | 冻结快照、不可变修订、结构/语义/人工审查、CAS 定稿与固定导出 | 主体完成；仍需补身份、链接和候选隐私边界 |
| Git | 工作区本地自动提交、删除跟踪、无关暂存隔离 | 不是远程多端同步；专题事件及失败重试未闭环 |
| SSE/后台任务 | 事件流、内存回放、任务状态、部分恢复 | 事件白名单、取消竞态、任务互斥/恢复有缺口 |
| Web/i18n | 仪表盘、采集、笔记、搜索、专题、合稿、文档编辑与双语 | 已实现；多个异步/锁场景已复现异常 |
| 公开与隐私 | 显式公开开关、固定定稿导出、来源/报告保护、隐私接口 | 主体存在；隐私声明与实际外发行为不完全一致 |
| 运维与发布 | health、metrics、jobs、CI、三平台 Release 配置 | CI 成功；不能替代业务跨模块回归；无通用多用户/远程同步验收 |

## 三、验证记录与可信度

### 3.1 本次在评审环境实际执行

| 检查 | 结果 | 解释 |
|---|---|---|
| `make node-check` | 通过 | JavaScript 语法检查 |
| `make node-test` | 119 项通过 | 仓库现有 Node 组件测试 |
| `make node-test-i18n` | 6 项通过 | 双语注册、回退及键集合检查 |
| `git diff --check HEAD^ HEAD` | 通过 | 最新提交补丁空白检查，不代表全项目语义正确 |
| 额外 Node VM 复现 | 4 项确认缺陷 | error.code 丢失、旧会话响应覆盖新会话、清空活跃笔记前未释放锁、旧搜索响应覆盖新查询。测试“通过”表示复现断言成立，不是产品行为正确 |
| 双 VM 时间基准复现 | 确认缺陷 | 新旧标签页的 performance.now 基准不同，刚写入的锁可被另一标签误判过期 |
| `make browser-test` | 环境阻断，未完成有效浏览器验收 | 云端 Chromium 启动报 socket Operation not permitted / crashpad；21 个测试记录中 17 失败、2 跳过、2 通过，失败集中于启动阶段，不据此判定产品浏览器功能失败 |
| 本地 Go/build/race/API e2e | 未执行成功 | 当前 `/usr/bin/go` 不是 Go 编译器，返回 `Go: Unknown option: version`；没有可用构建产物供独立 API e2e |

### 3.2 独立查询到的远端 CI

通过 GitHub Actions API 核验，以下运行确实对应本报告完整 SHA，并已成功完成：

- [CI run 39704255455](https://github.com/muidea/thought-flow/actions/runs/39704255455)
- [Check (ubuntu-latest)](https://github.com/muidea/thought-flow/actions/runs/39704255455/job/119580437656)：success。
- [Check (macos-latest)](https://github.com/muidea/thought-flow/actions/runs/39704255455/job/119580437448)：success。

workflow 使用 Go 1.24.x、Node 22.x、Chrome，并执行 make ci-check。这是已核实的远端 CI 状态，不只是作者文档宣称；但未逐项下载解读日志，因此不把可选浏览器引擎或真实 LLM 质量评测视为已运行。CI 绿色与本报告发现可同时成立，因为现有测试缺少部分跨模块与时序用例。

实现文档还记录了 Go 1.24.13 路径别名故障注入及部分 race 通过。这些细分执行结果仍属于提交方记录。本轮没有新增证据证明历史全量 HTTP race 的 DuckDB checkptr 限制已解除。

## 四、阅读与定位方式

以下分域列出功能、问题、触发条件、修复方向和回归要求。代码路径均相对于固定基线的仓库根；同名 API 存在不等于所有参数或异步路径均已闭环。

## 五、采集、加工与合稿

### 功能矩阵

| 功能 | 当前结论 | 实际实现与边界 |
|---|---|---|
| 直接文本/URL capture | 已实现，有 URL 下游缺陷 | 校验类型、标题/标签/专题/来源，Markdown 原子写，发 captured 与 Git 事件；内容 hash 重复只告警而非丢弃。`capture/biz/service.go:86-207,748-775`。|
| 读取、列表、PATCH、删除 | 已实现 | PATCH 拒未知字段、空标题/空正文，支持 body/ai_notes/tags/topic_ids/links/relations/profile；正常路径共享 thoughtlock，typed 文档另有 generation 检查；删除调用 Markdown tombstone 协调层，列表跳过已删除对象。`capture/biz/service.go:209-447,548-611`。同 session 重入不等于同 session 并发串行。|
| Capture 会话 | 已实现 | 消息与结构化上下文、异步 enrich/retry/timeout、预览、new/update/supplement、重开 Thought、重复提交路径均存在。归档前验证预览，新归档成功后标记 committed 并 reset。`capture/biz/scratchpad.go:201-245,476-622,1087-1330,1942-2179`。URL 新归档见 CP-01 / P1。|
| Refine 摘要、要点、标签、标题 | 已实现但 URL 内容选取错误 | captured 触发异步；可手动强制重试；retryable 错误最多 3 次；无 Key 走规则 provider；原文保留。`refiner/biz/service.go:98-130,176-316`。普通编辑不自动重新摘要（只订阅 captured）。|
| Refine 缓存 | 部分实现 | 检查 refined + ContentHash==当前输入 hash；但 ContentHash 同时被 PATCH 更新，不是独立的 last-refined-input hash。编辑后若直接走非强制 RefineNow，会将旧摘要误当缓存；HTTP RetryRefineAsync(force=true)可绕过。`capture/biz/service.go:372-390`、`refiner/biz/service.go:319-337`。|
| Expander | 已实现，失败隔离不完整 | 四路并行：最多 3 个关联 Thought、LLM 扩展计划、最多 3 个近似专题、前 2 个网页 followup；可配置 pipeline timeout。部分结果写回并在 Errors 记失败，但 errgroup 会联动取消其他阶段。`expander/biz/service.go:201-404`。不是联网深度研究，followup 只是抽取链接。|
| 无 Key 模式 | 已实现，但不等于离线 | Refine/Embedding/Weave/Synthesis/Expand/CaptureContext/DocumentGeneration 均有本地规则 fallback（`pkg/ai/refiner.go:190-238,293-482`）；输出是规则摘要/拼接/模板，不是与真实模型同等质量。URL origin fetch 仍联网，Reader 默认 enabled 且无 API Key 可用第三方公共端点（`pkg/appconfig/config.go:284-289`）。|
| Reader 设置 | 已落实 | production 使用 NewFromConfig；disabled 阻断 Reader fallback，base URL/API key/timeout 生效，origin 不带 Reader Authorization；已有 httptest 覆盖。`pkg/webfetch/fetch.go:71-103,207-243`，`config_test.go:15-87`。disabled 不关闭原网址/GitHub 抓取，也不意味着整个 app 离线。|
| URL 抓取 | 部分实现 | GitHub README → HTTP HTML 转文 → Reader fallback；最多读 2 MiB，正则提取网页结构和绝对链接；没有浏览器 JS 执行，不保证登录站点可读。GitHub tree/blob URL 仍尝试仓库 main/master README，不解析所选文件/分支（`fetch.go:148-171`）。|
| Compose 来源篮与异步生成 | 已实现，有非 Thought 数据缺失 | basket 有 version 冲突保护；inflight fingerprint 合并相同请求，生成中 placeholder、jobs、错误状态；成功只移除本次 sources。`compose/biz/service.go:149-280,350-452`，`pkg/composedraft/store.go:211-281`。非 Thought 来源只生成元信息见 CP-03 / P1。|
| Compose Profile/输入冻结 | v2 已实现；不是所有 profile 都有 | async resolve/freeze profile；只有 SchemaVersion==2 才冻结 ThoughtSnapshots 到私有 runtime 并校验 hash（`compose/biz/service.go:168-185,238-246`、`frozen.go:14-72`）。legacy/note v1 路径在 worker 才 hydrate，因此不应泛称所有来源在入队前冻结。|
| Compose 重启/关闭 | 已实现有边界 | completed draft 的 job 补 success；queued 可恢复；running 视为未知收费调用，转 canceled + failed，不自动重放；Close cancel 并等待 worker。`compose/biz/service.go:286-334,1238-1248`。refiner/expander 没有相同恢复机制。|
| Compose 编辑保存 | 已实现 | 保存时可覆盖 content/title/tags，保留 provenance；每 draft 锁、持久化 SaveCommand/预留 ThoughtID，失败后重试不重复创建；已 saved 再保存返回错误而非相同成功响应；编辑后旧生成验证失效。`compose/biz/service.go:738-895`；`capture/biz/service.go:95-111`。|
| Compose 删除/取消 | 删除已实现；运行调用取消仅部分 | draftMu 防生成完成后复活，删除幂等，job 标 canceled。没有单 draft cancel function，当前模型调用仍用全局 workCtx 跑到完成/超时；只在全局 Close 才真正取消 context。`compose/biz/service.go:376-399,665-713,1238-1248`。不要把状态 canceled 描述成已中断模型费用。|
| Refiner/Expander Job 恢复 | 未实现自动完整恢复 | job 文件持久化，但模块 Run 空实现，worker 使用 Background；无对应 restart replay 或 sweep。遇锁直接记 succeeded/skipped，后续没有代码保证自动补做。`refiner/module.go:76-85`、`refiner/biz/service.go:176-185`、`expander/biz/service.go:201-208`。|

### 五项优先缺陷

#### CP-01 / P1：含 URL 的普通 Capture 会话无法新归档

- 触发：会话输入 `https://example.com/article` 或含链接文字，选择普通 note profile，生成有效预览并确认新归档。
- 根因：`internal/modules/capture/biz/scratchpad.go:1860-1864` 将 Type 改为 url；同函数 `1872` 却固定 `URL: ""`；`commitFresh:2001` 送到真实 Capture，`internal/modules/capture/biz/service.go:754-756` 必然报 `url is required`。不是网络失败，也不是 AI Key 问题。
- 影响：常规“粘贴链接→对话整理→归档”主链路被阻断。
- 修复：保留 extractURL 返回值并填入 CaptureCommand.URL；或明确把归档产物建为 text、把 URL 保存为 provenance，但要同步物理类型设计。
- 测试缺口：`scratchpad_test.go:1314` 与 `1998` 仅检查 cmd.Type==url；stubCapture 不做真实校验，未覆盖 BuildCaptureCommand→真实 Capture。

#### CP-02 / P1：网页抓取成功，但摘要模型读到 URL 而不是网页正文

- 触发：直接 capture `{type:"url",url:"https://…"}` 且不附 content；目标页返回明显不同于 URL 的有效正文。
- 根因：`capture/biz/service.go:164-166,768-773` 把 URL 写进 AINotes；`refiner/biz/service.go:288-295` 仅将网页正文存入 ExtractedContent；本地/远程 Refine 分别 `pkg/ai/refiner.go:295,924` 调用 `primaryThoughtText`，该函数 `1798-1804` 永远优先 AINotes。
- 影响：Summary/KeyPoints/AITags/embedding 基于 URL 字符串或用户旁注，而非抓下来的网页；后续搜索关联、Compose 又常优先 Summary，错误延伸。无 Key 与有 Key 均受影响。
- 修复：为 URL 提供明确的 refinement input（网页正文优先并保留用户注释）；不要全局简单调换 AINotes 优先级破坏已编辑笔记；另存 refined input hash。
- 测试缺口：refiner URL 测试 `service_test.go:235,296` 验证失败保留原文/已抓内容，不检查 provider 收到的实际摘要文本。

#### CP-03 / P1：Compose 的 Search/Topic/Capture 来源忽略实际片段内容

- 触发：提交非 Thought ComposeSource，例如 `source_type:"topic_section"`，Snippet 包含唯一标识 `事实：预算为314159`，或来源为 capture_session/search_result。
- 根因：`internal/modules/compose/biz/service.go:924-925,936-960` 不读取这些资源的正文，也不使用 `src.Snippet`/Metadata，只构造 `Compose source type/id/Source link` 三行；此伪正文送进模型。fingerprint 却包含 snippet（`1200` 附近），让 UI 看似来源已参与。
- 影响：合稿可显示成功、保留来源链接，但模型不知道实际来源内容；与功能设计 `doc/thoughtflow-functional-design.md:334-337` 的多来源整理语义不符。
- 修复：来源 resolver 实际 hydrate 对应资源，或至少使用显式提交的 snippet/body 并记录完整性；对 metadata-only 来源清楚报不可用，不能冒充全文。
- 测试缺口：`compose/biz/service_test.go:255-302` 只检查数量、source 类型、link 和 discriminator，不断言真实事实到达模型。

#### CP-04 / P2：Expander 一路出错会取消另外三路，不符合 best-effort 语义

- 触发：搜索立即返回 error，而远程 Expand/provider 或 URL fetch 尚未完成且遵守 ctx。
- 根因：`internal/modules/expander/biz/service.go:267-279` 注释承诺互不阻断，但用 `errgroup.WithContext(ctx)`；各支路 `286-316` 把错误返回 group，首个错误即取消 gctx。
- 影响：本可成功的计划/URL/专题阶段也被 context canceled，且 `228-231` 会把旧关联/旧计划覆盖为空。表现为一个局部故障造成多功能丢失。
- 修复：使用共享 deadline 但不要首错取消 sibling；逐 stage 收集结果/错误，并只覆盖成功阶段的数据。
- 测试缺口：`service_test.go:186-213` 的 partial-failure 测试用忽略 ctx 的同步 stub，所以不能发现真实 HTTP/LLM 的取消行为。加一支等 ctx/定时完成的 provider 测试。

#### CP-05 / P2：同类后台任务不互斥，且编辑锁导致永久 skip

- 触发 A：对同一 Thought 快速两次 retry-refine，两次 provider 都阻塞在响应前。
- 根因 A：每次 `refiner/biz/service.go:110-130` 都新建 job；两个 job 共用 holder="refiner"（`182`），`pkg/thoughtlock/thoughtlock.go:114-120` 允许同 holder 重入。expander 同理。先完成的 job Release 还会把另一个进行中的锁释放。没有 heartbeat，90 秒 TTL 也可在长调用期间过期。
- 影响 A：重复模型收费、最后写入的 metadata 覆盖前一结果；typed 修订 CAS 可挡住部分正文冲突，不能据此声称所有 metadata 更新互斥。普通/legacy Thought 更易丢失更新。
- 触发 B：另一 session 持锁时发 RefineAsync，释放锁后等待。
- 根因 B：`refiner/biz/service.go:178-185`/`expander/biz/service.go:201-208` 直接 succeeded/skipped；注释声称下一 sweep 补做，但模块 Run 空实现且仓库没有这个 sweep。
- 影响 B：用户看到 job 成功，摘要/扩展实际从未做；只有手动重试/另一次事件才可能恢复。
- 修复：per-thought active-job 去重 + 每 job 唯一 lease token/heartbeat；对 busy 保留 queued/retry，并加入明确恢复机制，不伪报 succeeded。
- 测试缺口：现有 locked 测试只验证 skip，没有“释放后最终执行”；未覆盖同 thought 多 job 在途。

### 其他建议记录为边界/后续回归，不混入已动态复现

1. Scratchpad fresh Commit 的 `Get→Capture→MarkCommitted→Reset`（1942-2011）没有整体 session mutex/持久化 reservation；双击并发或 Capture 成功、MarkCommitted 失败后的重试可产生重复 Thought。与 Compose 已实现的 reservation 不同。需要并发 barrier 与失败注入测试后修复。
2. `commitRepeat:2042` 仍调用不取 thoughtlock 的 ApplyDraftInternal（`capture/biz/service.go:321-338`）；其注释沿用“首次提交后 patch”旧理由，但当前 commitFresh 已无此 patch。应复查 repeat 与后台 refine/edit 并发。
3. Jobstore `Save:75-88` 用 os.WriteFile 非 tmp+rename，锁是每 Store 实例，而多个模块各建一个实例；崩溃半写/跨实例读写可使 job JSON 不完整，List 遇一个坏文件整批失败。影响 Compose 重启恢复，需故障测试。
4. Compose delete 只改 job 状态，不取消单任务模型请求；source freeze 只限 v2；这两点应在产品描述与测试中明确，不应归类为已完整支持。
5. Reader 的默认 base 是 `https://r.jina.ai/http://`，fetchReader 直接再拼完整 rawURL（`fetch.go:211`），可能形成双 scheme；现有测试只用自定义 endpoint。需用官方协议/离线 URL 构造测试确认默认值，当前不把外部服务兼容性猜测列为确定缺陷。

### 已有测试值得保留的正面保障

- Capture 原子 Markdown、重复告警、列表删除中断恢复、PATCH 字段/锁/标签/链接：`capture/biz/service_test.go:21,100,125,187,245,345-518`。
- 会话上下文并发用户回合、错误重试、typed 预览时效与精确正文、update/supplement/reopen：`capture/biz/scratchpad_test.go:680,896,1915,2171,2384,2504,2666`。
- Refine 缓存与强制重试、URL 失败保留、retryable provider：`refiner/biz/service_test.go:132,188,235,296,360`。
- Compose 异步/去重/queued恢复/关闭 join/保存 reservation：`compose/biz/service_test.go:715,789,853,976`、`save_retry_test.go:38,79,147`。
- 冻结输入测试 `service_test.go:995` 实际仅调用 saveFrozenInputs/readFrozenInputs 并检查 hash，不是“来源修改后完整异步/重启生成仍用旧输入”的端到端测试，名称比覆盖范围强。
- Reader disabled 零第三方 fallback、凭据只给指定 endpoint：`pkg/webfetch/config_test.go:15,51`。


## 六、检索、专题与 Git

### 功能现状清单

| 功能 | 当前实现 | 判断 |
|---|---|---|
| 关键词搜索、标签过滤、片段预览 | HTTP handleSearch 固定 keyword；store 支持更广参数 | 主体存在；HTTP 忽略 topic_id，底层集合过滤存在精确性问题 |
| HTTP 与内部语义能力边界 | GET /api/search 固定 Mode=keyword，不读取请求 mode；semantic/hybrid 仅内部 Service/Store 能力 | 不能声称当前公开 API 提供语义/混合模式；配置 DefaultMode 也不改变该入口 |
| DuckDB FTS | 延迟加载扩展及建索引；不可用时关键词评分回退 | 有降级，不等于无 DuckDB 数据库运行 |
| 语义/混合搜索 | query embedding + DuckDB vector/HNSW 尝试、array/JSON cosine fallback | 存在，但 embedding 生命周期不完整 |
| 无 Key | 本地规则提炼、本地 64 维 hash embedding、本地规则缝合 | 本地 provider 可不调用模型端点，但 URL/Reader 仍可联网；不是自然语言语义模型，尤其中文整段 token hash 无真实语义能力 |
| 索引重建 | 清表、扫描 Markdown、重新为每篇调用 embedding | 可手动触发；无原子切换、启动自动恢复缺失 |
| Thought 删除索引 | 删除内容、Thought、embedding、向量表；有删除回归测试 | 已实现；专题引用不会同步删除 |
| 专题规则 | keywords Any/All/Exclude、tags Any、manual include/exclude、semantic threshold | 实现存在，语义硬排除也有处理 |
| 自动专题缝合 | 命中且 AutoWeave 时 AddMembership，调用 LLM/本地规则并检查来源链接 | 有实现；并发、已收录内容更新存在缺口 |
| 预览/接受提案 | proposal 持久化、diff/structured patch、base hash stale 检查 | 存在；仅使用原提案 patch 的路径有 stale 防护；用户替换 Document 则整文写入 |
| 专题候选 | 会话匹配 YAML + Compose 动态匹配；已归档会话从候选移除 | 存在；新建/改规则/刷新不扫描已有会话，且同会话重复两种来源 |
| 专题刷新 | 全扫描 Thought、重建 index.md、更新成员/反链、删除过期 membership | 会破坏已编辑正式正文，不能视作安全的候选刷新 |
| Git 本地自动提交 | debounce 合并、后台 Job、事件、工作区独立 git init、历史查询 | 有实现；专题目录事件与文件-only 提交不兼容 |
| Git 删除/无关暂存隔离 | `git add -A` + literal pathspec + `git commit --only`；处理 staged deletion、空 changeset | 有明确新测试覆盖，这是已修复项，不能沿用旧“夹带暂存文件”结论 |
| Git 远程同步/冲突恢复 | 未见 push/pull/fetch、远程配置、冲突恢复流程；错误仅 Job/SSE | 尚无多端同步。PRD 第 34 行已称“后续扩展”，不要误判为现版本必需已有 |
| workspace/runtime | 默认内容 ./thoughtflow-workspace，运行态 ./thoughtflow-runtime；索引相对路径解析到 RuntimePath | 主体分离；.scratchpad 和 candidates.yaml 仍落内容目录 |

### 确认问题与建议

#### STG-01 / P1：专题正常缝合和提案导致 Git 整批提交失败

- 证据：`internal/modules/topic/biz/service.go:1305-1310` 将 `topics/<slug>/memberships`、`approvals` 目录加入 Git paths；`internal/modules/git_sync/biz/service.go:317-318` 明确拒绝目录并立即 return。
- 路径：创建专题 → 收录第一篇 Thought（创建 memberships/*.yaml），或 PreviewWeave（创建 approvals/*.yaml）→ 自动提交包含该目录 → `commit path must be a file`。同 debounce 批次其他 Thought 也无法提交。空专题暂时可成功，掩盖问题。
- 测试缺口：Git tests 验证单文件与删除；topic tests 验证落盘，缺少真实 topic event → Git Commit 链路。
- 修复：Topic 发出新增/修改/删除的具体文件 allowlist，而非放宽成递归提交目录；保留删除的路径，即便文件已经不存在。集成测试同时确认 candidates.yaml 不被提交。

#### STG-02 / P1：创建无关 Compose 草稿会覆盖已确认专题正文

- 证据：`topic/biz/service.go:107-108` 将 compose.draft_created/saved 直接转为 RefreshAllTopics；`:987-1002` 无 AutoWeave 或关联筛选；`topicstore/store.go:700-704` 清空成员并从 initialDocument 开始，`:730` 重织，`:747-748` 覆盖正式 index.md。
- 复现：接受专题提案时增加独有结论/人工段落，或直接编辑正式 Markdown → 创建一个与该专题无关的 Compose 草稿 → 等后台任务 → 独有正文消失。AutoWeave=false 亦不能阻止此路径。人工接受但不符合规则的成员还可能被刷新移除。
- 修复：候选刷新与正式正文重建分开；Compose 变化只更新关联候选投影。正式文档变更以当前文档为基线做提案/CAS，不隐式重置；保留明确接受的成员。

#### STG-03 / P1：专题并行写入缺少资源级串行/CAS，成员和正文可相互覆盖

- 证据：`topicstore/store.go:25-28` 无锁；AddMembership 接收调用方旧 topic，`:479-505` 在旧成员数组上追加并整文件覆盖；`topic/biz/service.go:MatchThought` 每个后台任务先 List 后逐专题 AddMembership。refined 与 index_updated 均订阅并可重复并发触发（`topic/module.go:69-70`）。
- 调用方并发证据：`topic/biz/service.go:544-563` 的 MatchThoughtAsync 仅将 run 传给 AsyncFunction 并立即返回；`vendor/github.com/muidea/magicCommon/task/background.go:94-99,122-126` 将每个 task 交给 Execute.Run；`vendor/github.com/muidea/magicCommon/execute/execute.go:55` 明确启动独立 goroutine。事件 lane 只串行 Notify，不会等待脱离 lane 的后台任务；`eventutil/publish.go:22` 也没有设置 topic ID lane。因此这不是仅依据“未见锁”的猜测。
- 复现路径（并发推演，未执行）：两篇 Thought 同时命中同一空专题，二者都读空 Members；A/B 各写一个成员，最终 topic.yaml 只留最后一个，可能 index.md/反链与 membership 文件集合不一致。已有测试 `TestServiceListCandidatesFusesSourcesAndSortsByScore` 约 1185 行注释也明确说明调用者必须传最新 topic，否则第二次覆盖第一次。
- 修复：专题 ID 锁覆盖读—模型结果提交—写事务，模型期间可用版本号 + CAS 重试；更新 Thought 反链也需读取最新快照并使用已有写协调器，不以旧内容整文覆盖。补 barrier 控制的并发测试。

#### STG-04 / P2：新建/改规则/手动刷新不真正刷新历史会话候选

- 证据：`topic/biz/service.go:115-135` Create/Update 只存储与发事件；对应 handler `application/thoughtflow/service/service.go:1152-1165,1246-1262` 未追加刷新。RefreshWithMatcher 只扫描 thoughts（`topicstore/store.go:708-730`）。ScratchpadProvider.List 虽声明但业务从不调用；候选只在 session 事件到来时匹配单条。
- 复现：先创建包含关键词的未归档会话 → 后创建对应专题 → 候选为空；点刷新仍为空，直到会话再次产生 context_updated。改规则后旧候选也可长期残留。创建专题同样不会自动收录历史 Thought，要额外手动刷新或等其他事件。
- 对照 PRD：`doc/thoughtflow-prd.md:98,107` 明确要求创建/规则更新/刷新基于全量相关信息更新。
- 修复：引入统一 candidate refresh，枚举未归档会话和 Compose，原子替换投影；创建/规则更新触发它。正式内容是否自动收录另尊重 AutoWeave。

#### STG-05 / P2：每个普通采集会话被重复标记成“Thought 重新整理”候选

- 证据：`topic/biz/service.go:335-358` 对每条 session 无条件 append capture_session 与 thought_reopen_session 各一次，两条 session ID 相同，不判断来源 Thought。
- 复现：一个普通新采集会话命中专题，ListCandidates 返回两条同源候选，虚增数量并错误宣称重新整理。
- 测试问题：`TestServiceListCandidatesFusesSourcesAndSortsByScore` 明确断言两条存在，测试固化了错误语义，而非实现了区分。
- 修复：根据真实会话 lineage/original Thought 判断唯一 Source；无 lineage 只能 capture_session。

#### STG-06 / P2：删除 Thought 后专题仍保留失效成员/引用

- 证据：capture DeleteThought 发 thought.deleted（`capture/biz/service.go:244`）；search 订阅并删除索引；topic `module.go:69-74` 无 thought.deleted 订阅，Notify 无删除处理。清理 stale membership 仅在手工 RefreshWithMatcher 中执行（`topicstore/store.go:755`）。
- 复现：把 Thought 加入专题 → 删除 Thought → 搜索可消失，但专题 Members/count/index.md 仍引用已删除来源，打开来源 404；重启也不自动修复。
- 修复：删除事件显式清理成员关系和反链，文档中的已引用内容给出来源已删除状态/需审阅提案，不粗暴重建整文。删除后发布具体 Git paths。

#### STG-07 / P2：索引丢失或隔离恢复后启动仍空索引并报告 ready

- 证据：`searchdb/store.go:46-60` 损坏库 quarantine 后打开全新空库；`search/module.go:73-90` Setup/Run 不 Reindex；搜索 status 仅检查 store/path/vector，并不比较 workspace 内容。
- 复现：已有 Markdown，停止服务并移走可重建索引库（保留正式内容），重启 → Notes 能读取文件，Search 查询旧笔记为空；用户需手动重建。受支持的损坏库恢复路径同样如此。
- 修复：启动检测新建/恢复库、索引版本、workspace fingerprint，安全后台构建并展示 rebuilding 状态；不要把空索引的“可打开”视作内容完整。

#### STG-08 / P2：每次 topic.updated 都全库清空并重算全部 embedding

- 证据：`search/biz/service.go:78-79` 任意专题变更触发 ReindexWorkspace；`searchdb/store.go:435-453` 非事务清空全部表；`search/biz/service.go:335` 再全库 backfill，每篇逐条 Embed，未按 ContentHash 复用。
- 影响：M 个专题变更 × N 篇内容触发 O(MN) provider 调用；只是改专题名称也重新付费 embedding。重建期间搜索可空/不完整；多个专题/Compose refresh 同时触发多个重建，互相清表或插入冲突；扫描遇坏 Markdown 后留下部分索引。
- 复现：N 篇已有索引，更新一个专题，看 embedding 调用增加 N；同时更新两个专题触发两次全库重建。暂停 provider 或放入不可解析 Markdown可观察半成品状态。
- 修复：只更新受影响 Thought 的 topic IDs；按 hash/model/dimension 缓存 embedding；单飞合并重建任务，临时库/事务构建完成再切换，不破坏正在服务的旧索引。

#### STG-09 / P2：内容修改后可继续使用旧 embedding；分开配置 Key 时模型不一致

- 证据：`search/biz/service.go:144-154` 只有事件附带 embedding 才更新，没有则保留旧缓存；`searchdb/store.go:312-360` IndexThought 不失效 embedding；`topic/biz/service.go:1179-1187` cache 只按 ID/model，忽略 ContentHash。Query 使用单独的 NewEmbeddingProvider。
- patched 实际链路：`search/module.go:81` 订阅 thought.patched，`capture/biz/service.go:419-430` 发 map payload；`search/biz/service.go:eventEmbedding` 只接受 ThoughtRefinement / *ThoughtRefinement，必得 nil。refiner 未订阅 thought.patched，因此不能假定后台一定自动补新向量。
- 复现 A：已有 embedding 的 Thought 改标题/正文，patched/expanded 事件无 ThoughtRefinement embedding；关键词索引更新，但语义/专题缓存仍代表旧内容，直到后续重新提炼或全库重建。
- 复现 B：LLM Key 空但 embedding Key 配置有效，NewRefineProvider (`ai/refiner.go:190-195`) 仍选本地 refine 并产生 local-hash embedding，而 query 是远端 model；反之只配 LLM Key、不配 embedding Key，remote refine 的 Embed 错误被吞掉 (`:942-958`)，初始索引无向量，query 却有 local-hash vector。全量 reindex 后才统一到独立 embedder。
- 修复：所有 embedding 通过独立 provider，index 时统一按规范化文本 hash 失效/补全，不依赖 refine provider 顺带返回；缓存使用前验证内容版本；补四种 Key 配置矩阵与 patch/expand 测试。

#### STG-10 / P2：HTTP 忽略专题过滤，底层标签/专题筛选亦不是集合精确成员

- HTTP 直接缺陷：`application/thoughtflow/service/service.go:784-793` 构造 SearchQuery 没有 TopicID，也不读取 query.Get("topic_id")；`GET /api/search?q=...&topic_id=不存在的专题` 与无 topic_id 一样返回结果。应先补入口参数映射与真实 API 断言。
- 底层证据：`searchdb/store.go:847-866` 将 CSV topic_ids/tags 用 `%value%` 查询，且未转义 `%` / `_`。
- 复现：两个专题 ai 和 ai-tools，查询 topic_id=ai 也命中 ai-tools；tags=go 命中 golang；tags=% 可匹配所有非空/空文本。普通参数 SQL 绑定不构成注入，但筛选语义错误。
- 修复：先接通 HTTP TopicID 映射，再规范化关联表或列表类型，按完整元素比较；至少保证分隔符边界并转义 LIKE 通配符。加前缀相同 ID 与含特殊字符标签测试。

#### STG-11 / P2：Git 失败 changeset 被丢弃，没有修复后重试

- 证据：`git_sync/biz/service.go:148` takePending 先清空内存集合；`:159-169` Commit 失败只发事件、返回，未 requeue 或持久化待提交路径。
- 复现：Git user.email 未配置或存在 index.lock → 自动提交失败 → 修好配置/锁 → 不再编辑该文件 → 该次变更不会自动补交；重启也不恢复。后续无关文件事件只提交新文件，不能保证旧文件补上。
- 修复：持久化 changeset，成功才 ack；可重试错误退避，冲突等需人工处理错误保留 pending/blocked，提供明确重试入口。冲突不应靠自动 add/commit 猜测解决。

### 其他明确差异/局限（不与以上缺陷重复计数）

- README 第 12 行称“默认 fallback search store、duckdb build tag”，但 `searchdb/store.go:19` 无条件 import duckdb，目录无 fallback store；Makefile 明写 DuckDB only backing store。README 第 9 行称 runtime 在 .thoughtflow 下，当前默认配置却是 ./thoughtflow-runtime。建议同步文档和发布构建条件。
- .scratchpad 路径仍在内容根（workspace.go:42），session candidates 在 topics/<slug>/candidates.yaml（topicstore.go:1035）。当前自动 Git 文件路径不包含 candidates，因此不能声称已自动泄露；但新建工作区未见生成忽略规则，用户 git add -A 会将私有候选/草稿加入。至少明确备份/同步边界，迁到 RuntimePath 或生成 workspace .gitignore。Git 黑名单只拦 .thoughtflow、RuntimePath、duckdb，不是完整业务文件 allowlist。
- Search IndexThought 在数据库写成功前将 Markdown IndexStatus=Indexed，并更新 Thought.UpdatedAt（search/biz:136-142）；数据库失败可能显示已索引，索引技术操作改变用户内容时间并产生 Git churn。宜将运行状态留 runtime，并先保证数据库成功。
- 语义近命中 NearMissTopics 未排除已经达到阈值/已收录的专题，函数注释声称 below threshold，但实现丢弃 matchTopic 的 bool。需要调用方一并核对是否消重。
- AddMembership 对已存在成员仅更新 membership/反链（topicstore:464-477），不重新缝合其修订内容。已有 Thought 大幅改写后专题正文仍旧，直到破坏式 full refresh；应设计版本化候选更新而非二选一。
- LLM 缝合错误或遗漏 source link 时直接回退 appendThoughtSection（topicstore:1408-1423），让流程可完成，但没有向用户区分“模型合并成功”和“降级追加”。
- Git RuntimeStatus 用 is-inside-work-tree，不像 ensureRepository 验证 top-level；嵌套在别人的 repo 下可先显示 repository ready，第一次 Commit 才初始化自己的 repo。

- Web/API 主搜索入口明确固定 keyword（application/thoughtflow/service/service.go:786），配置默认亦 keyword（appconfig/config.go:262）；search.Service 对直接内部调用的空 mode 则默认 hybrid（search/biz/service.go:176-181）。不要因为底层支持 semantic/hybrid 就宣称 Web 暴露这些模式。

### 已有测试能证明什么，以及缺失的验收

已存在：search Index/Search/preview/filter/ranking、DeleteThought 清向量、reindex 清 stale + backfill；DuckDB FTS/array vector/HNSW（扩展可用时）；topic keyword/semantic/cache、preview/accept/stale patch、会话候选更新/归档移除；Git path filter、嵌套 init、history/status、tracked/staged delete、无关 staged 文件隔离。此次均只阅读未执行。

优先补集成用例：
1. 真实专题收录/提案事件经过 Git debounce 后成功 commit，含文件删除、不夹带 candidates。
2. 人工接受编辑后的专题正文，在无关 Compose created/saved 后字节不变。
3. 同专题两 Thought 并行归档、refine/index 重复事件，不丢成员/正文/反链。
4. 创建/改规则/刷新专题均看到此前已有会话，且每会话仅一候选来源。
5. 删除 Thought 后搜索与专题一致；重启新建索引自动恢复全部当前内容。
6. 四种 Key 配置组合、patch/expand/restart 后语义索引模型和 hash 一致。
7. 双重建同时运行、provider 中断、坏 Markdown 时仍能服务上一版本完整索引。
8. Git 失败修复后无需编辑原文即可重试相同 changeset，且保留用户已有暂存与冲突状态。


## 七、类型化文档交付

### 范围与结论

基线：82b91d5e3abaee0de70a975d88925b56b7a6f401。已读 AGENTS.md；只读代码，无源码修改、切换分支或 checkout。覆盖 documentprofile、documentvalidation、documentstore、documentflow、Markdown 投影及 documents HTTP API；兼看 AI 独立审查和 Capture/Compose 候选接入。

主链路已经具备：冻结模板/正文/契约/来源 → 不可变修订 → 确定性检查 → 独立语义或人工审查 → CAS 定稿 → 固定历史正文导出。不能据此认定全部功能闭环：仍有目标地址检查遗漏、自定义章节修复失败、格式发布竞争，以及外部 Markdown 身份错配引起跨文档写入。

本次结论为代码静态核查。尝试执行 `go version` 和五个目标包 `go test`，均返回 `Go: Unknown option: version/test`；/usr/bin/go 不是可用 Go 工具链，未取得测试通过结果。仓库实现文档记录的既往 CI/race 通过不当作本轮实际执行。

### 当前具体问题

#### D1 [P1] 非参考段落中的图片、自动链接和标题链接绕过安全 URL 检查

- 定位：internal/pkg/documentvalidation/validate.go:112–142。Heading 分支在 138 行直接 WalkSkipChildren；140–142 行只收集 `*ast.Link`，没有 `*ast.Image` / `*ast.AutoLink`。
- 触发：以现有通过结构验证的设计文档 fixture 为底稿，在普通正文区加入 `![diagram](https://example.org/image?token=SECRET)`，或 `### [Appendix](https://example.org/?token=SECRET)`；也可加入 `<https://user:SECRET@example.org>`。不要放在 References 定义中，那里已有独立地址抽取。
- 结果：这些地址不进入后续 validLocator 检查，不会产生 unsafe_link 硬错误。已有的人工审查理由确认后仍可定稿，Export 原样返回含凭据地址的正文；图片还可能在客户端呈现时自动请求目标。
- 与旧修复区别：cc3076e/43395dd 已检查“参考定义”的裸 URL、Link、AutoLink；此问题是正文全局 AST 遍历遗漏，不是重开相同参考定义缺陷。
- 修复：把目的地址收集与章节统计分开，对全树非代码节点统一检查 Link、Image、AutoLink；不能在 Heading 处分支跳过它们。继续保留代码示例不作为实际链接的设计，错误诊断经过 Redact。
- 回归：三类节点分别放普通段落、H1/H3、列表中，凭据/敏感 query 必须硬失败，安全 URL 正常通过；增加 finalization 拒绝与诊断不含 SECRET 的端到端断言。

#### D2 [P1] Markdown front matter 的 id 与文件身份不一致会把 A 的内容发布到 B

- 定位：internal/pkg/markdown/thought.go:121–155，尤其 123 行锁的是入参 thoughtID，153 行却优先采信文件中的 thought.ID；155 行把该值传入 SyncProjectionLocked。该方法在 documentflow/service.go:169 后用 `t.ID` 读取/发布 HEAD，280 行 Publish。
- 触发：已有两个不同正文、同类 Profile 的正常类型化文档 A/B，外部编辑 A.md 的 front matter，仅把 `id` 改成 B 的 ID（或复制 B 的 YAML 头到 A 文件）。调用 ReadThought(root,A)。
- 结果：函数持有 A 的锁却同步 B 的 HEAD。observe=true 时将 A 的正文作为 B 的 external_edit 新修订发布，再把 B 的链接写回 A 的路径。B 的权威正文已被 A 污染，B 的下次读取还会发生反向导入；并发时也绕过 B 的锁。没有要求直接篡改不可变 journal，普通 Markdown 外部编辑即可触发。
- 修复：进入 SyncProjectionLocked 前验证已解析 ID 与请求/路径 ID 完全一致，不一致明确报错；恢复 Projection 也执行同样身份校验。不要把可编辑 front matter 当作锁和 journal 的资源路由。
- 回归：A 文件写入 B 的 ID 后读取 A 应失败且 A/B 两个 HEAD、generation、正文都不变；加并发读写 B 的 race 回归。

#### D3 [P2] 自定义 H3–H6 Profile 可以发布和验证，但局部修复永远按 H2 解析

- 定位：internal/pkg/documentprofile/profile.go:175–176 明确允许 heading_level 2..6；documentflow/service.go:754–775 只传映射后的标题字符串；documentflow/repair.go:28 写死 h.Level==2，44 行替代骨架也写死 `##`。ai/document_review.go 的修复提示同样只说 H2。
- 触发：发布合法 v2 自定义 Profile，heading_level=3，模板/映射都用 H3；选择任一实际存在的 H3 章节和问题调用 Repair。
- 结果：即使模型正确地只改该 H3，scope 检查也没有收集这个 allowed section。改动被判为范围外；即使模型返回完全相同正文，seen 检查仍返回 ErrRules。模型请求已经发生，用户得到 422 且无法通过这个功能修复。
- 修复：把冻结 Profile 的 heading level 和一致的解析配置传入 scope 检查/修复请求，匹配标题提取语义；或者明确只支持 H2 并在 Profile 发布时拒绝其他级别，但不能继续对外宣称 2..6 全支持。
- 回归：H2/H3/H6 成功局部修复、相邻未选章节改动失败、模型零改动行为、HTML-looking 内容与验证器解析一致性。

#### D4 [P2] 同一 Profile 版本的并发发布会覆盖本应不可变的文件

- 定位：internal/pkg/documentprofile/registry.go:61–64 用 Stat 检查不存在，82 行用可覆盖的 os.Rename；整个 Publish 没有发布锁或排他创建，Reload 的锁只在写完后生效。
- 触发：两个 HTTP Publish 请求同时上传相同 profile_id/version、不同正文；均在文件不存在时通过 Stat，然后分别 Rename。
- 结果：两个请求都可返回成功，后 Rename 覆盖前一版本。若第一轮 Reload 已缓存旧 hash，当前进程可能保留旧快照而文件为新快照；重启后只识别新的 hash，先前返回的 ref 无法再 Resolve。破坏“发布版本不可变”的契约。
- 修复：使用 staging+fsync 后排他 link/rename-no-replace 发布，碰撞返回稳定 conflict；或至少对同 registry Publish 串行锁住检查到发布（排他文件原语更可靠且与 store.PutRecord 一致）。
- 回归：并发发布同版本不同内容恰好一成功一冲突，磁盘、返回 ref、Reload 后和重启后 hash 一致。已有 TestRegistryPublishesCustomFormat 只覆盖串行第二次拒绝。

#### D5 [P2] 候选预览绕过来源凭据拒绝边界，先将敏感 Locator 写入内容目录

- 定位：internal/pkg/documentflow/candidate.go:19–26 直接调用 Store.Publish，未调用 safeInputs；对照 service.go:804–813 的 credential-bearing source 拒绝和 Store.Publish:200–201 仅做 UTF-8/体积/数量检查。
- 实际接入：capture/biz/scratchpad.go:1307–1311 直接将 preview.SourceLinks 放进来源清单再 Candidate；compose/biz/service.go:597–607 也直接进入 Candidate。
- 触发：候选来源 Locator 含 `https://example.org/?token=SECRET` 或 URL userinfo。候选正文是否有此引用不影响持久化：Publish 先写 source-manifest.json，39 行随后才验证正文。
- 结果：正式 CreateRevision/SyncProjection 会拒绝的相同敏感来源，在预览路径先落入 workspace/documents/capture-* 或 compose-*/revisions/*/source-manifest.json 并返回于候选 snapshot。即使报告 failed 也不回滚此文件。此处未据此声称它自动进入 Git，候选无 emit，Git 另行审计。
- 修复：在候选任何落盘之前应用统一 safeInputs；尽量把所有发布入口共用的隐私不变量下沉，防止新增入口漏调用。
- 回归：Candidate 对 userinfo/token/CRLF Locator 返回 ErrInvalid 且 HEAD/修订目录均未产生；与普通 CreateRevision 的行为对齐。

### 功能矩阵与现有证据

| 功能 | 当前实现与证据 | 本轮判断/缺口 |
|---|---|---|
| 内置格式和自定义格式 | profile.go Parse 校验 schema、类型强制规则、输入、必需章节映射；registry.go List/Resolve/Publish/Reload；profile_test.go 覆盖发布、自动重载、历史 hash 冲突、软链接 | 基本具备；D3/D4 阻止声称完整闭环 |
| 冻结身份和证据一致性 | body/contract/profile/source manifest 各自 hash；ValidationKey 包含 validator/policy version；Revision 重读校验 hash | 主链路完整；TestIdentityIncludesEveryFrozenInput、TestValidationKeyChangesWithContractAndSources |
| 确定性检查 | AST 标题/层级/章节/占位符/引用/链接/字数，G-D-T、Q-F-C-S、tutorial/case/opinion，约束/术语 | 现有针对性覆盖丰富；D1 有全局链接节点盲区；不把结构通过等同事实准确 |
| 人工审查 | manual_review issue，逐项 issue ID+非空理由，contract 与 runtime 两个 AllowManual 门槛，structure_and_manual 记录 | 已具备；TestReviewFailureManualPathAndHardErrors、TestConfirmableSemanticErrorsDoNotFailStructuralRun |
| 独立语义审查 | 独立 provider 请求、JSON schema/问题字段检查、超时、模型错误显式 error、禁用时不调用 | 已具备；review timeout/cancel/disabled 测试；真实 LLM 事实质量未运行 |
| 局部修复 | 问题/章节选择、模型在锁外、预算、只允许所选 section 外字节不变、新 revision+重新 Validate、释放 slot 防嵌套死锁 | 内置 H2 路径具备；D3 自定义级别不可用；仅已有 H2 scope 和预算测试 |
| 修订与 CAS | Publish 在 generation 匹配时发布；typed API If-Match 428/409；旧报告不适用新修订 | 直接 API 路径已具备；D2 外部文件身份绕过正确资源锁 |
| 定稿 | report revision/key 匹配，重跑硬规则，证据可读，逐项确认，proposal/ready_for_use，blocker 限制；operation hash 幂等 | 已具备；TestConcurrentFinalizeAndEditCAS、PublishedFinals...、FinalizationChecksSourcesOutsideDocumentLock |
| 导出/公开 | draft/final 显式模式、body.md 原样返回；final 查已发布定稿链；公开地址受 PublicThoughtsEnabled；删除 gate | 已具备；不把 finalization 本身当发布；D1 会让未拦截敏感地址进入原样正文 |
| 隐私 | runtime evidence 0600、source Locator 敏感检查、诊断 Redact；ContentPaths 显式 journal allowlist | 主体具备；D1/D5 是不同入口/节点遗漏；未声称整个数据目录无任何敏感正文 |
| 崩溃与恢复 | HEAD 发布点、pending projection、冻结 projection.json 哈希；首次恢复和中断删除测试；未发布 final 不能导出 | 原修复已闭环，未重开；真实断电未做。外部 Markdown 身份仍有 D2 |
| 删除 | 先 tombstone+generation 再删 Markdown；API/export/迟到报告/旧 finalize 重试拒绝，列表跳过已删 | 43395dd/cc3076e 修复有专项回归，未见同一路径回归 |
| 普通笔记 | 非 typed 未创建 journal 时走轻量原逻辑；sections-v2 用明确边界保正文；保留 unknown front matter | 已有 markdown roundtrip；D2 对外部身份输入需加保护 |
| 配置 | Enabled/families/AllowManual/timeout/concurrency/max repair；检查、修复、定稿使用 family gate | 部分边界不一致：SyncProjectionLocked 只检查 Enabled、不检查 families；Candidate 不检查 enabled/families/closed ctx。可形成禁用 family 的 draft journal，是否应允许需明确产品策略 |

### 最新软链接修复 82b91d5 的专门复核

- documentstore.Store.path 已改用 EnsureNoSymlinks(filepath.Dir(s.Root),target)，检查 workspace 根本身及根内组件，不检查根以上系统别名。
- evidencePath 已改用 EnsureNoSymlinks(RuntimePath,target)，独立限制 runtime 根及内部；不是简单取消软链接拒绝。
- TestStoreAllowsSymlinkAboveWorkspace / RejectsSymlinksAtAndInsideWorkspace 和对应 Evidence 两组测试覆盖读写以及 evidence 0600。
- 与 macOS /var → /private/var 的问题方向一致。代码审阅未见本次变更把根内链接放开；本次未实跑 macOS、Go 测试或 TOCTOU 对抗测试。
- 进程锁只支持单写进程是明示产品约束，不作为新 bug；root 的别名规范化、多进程写入不混同于本次系统目录别名兼容修复。

### 建议验收顺序

1. 优先修复 D1/D2（安全检查/跨文档写入），补五类正文链接与身份错配回归。
2. 修复 D4 并发发布原子性、D5 候选敏感来源边界，再闭合 D3 自定义 Profile 修复。
3. 明确 family/Enabled 是否限制候选与普通 typed 草稿创建，统一入口并测试，不仅测试 Validate/Repair。
4. 在真实 Go 1.24+ 工具链执行 document.../markdown 及 HTTP 文档专项单测和 race；再跑 make ci-check。既往完整 HTTP race 的 DuckDB checkptr 限制继续单独记录，不计通过。


## 八、Web、API、事件与隐私

### 一、结论与证据等级

基础 Web/HTTP 功能不是空壳，七个主入口和多数业务 API 已有真实服务调用；但不能把现有测试全绿等同于并发会话、跨标签同步、隐私提示都已完成。

本轮独立 Node 复现 5 类确定问题：Capture 会话错配、搜索乱序覆盖、HTTP 错误码丢失、新建会话锁泄漏、跨标签时钟失配。另静态完整链路证实 SSE 事件漏订阅、隐私报告与 Reader 行为不符、SSE 心跳配置未生效。SSE channel 关闭竞态已考虑 vendored EventHub 的 recover，不能宣称服务必然崩溃。

### 二、完整功能矩阵

| 功能面 | 当前入口 / 后端接线 | 当前结论与边界 |
|---|---|---|
| 主导航 / 路由 | `web/index.html:25-52`，`app.js:124-147`；#/overview、#/capture、#/notes、#/search、#/topics、#/write、#/about | 七主入口；旧 settings/jobs 不再是独立页面，设置为抽屉，任务在 Notes Runtime；旧 topic review 链接映射 detail。查询条件与 tab 通过 hash 恢复。 |
| 总览 | `app.js:1312-1350,1429-1461`；GET system/status、metrics、capture/sessions | 工作区、LLM、Embedding、Git、Search 状态和最近会话；最近活动不是完整 SSE 活动日志。 |
| Capture | `app.js:3114-3209,3441-3755`；sessions 创建/列表/active/get/delete、messages/context/intent/strategy/profile、archive preview/archive | 多轮输入、历史恢复、上下文、归档预览/确认、重新整理、update/supplement、新会话均接线。主显示为气泡；旧 workbench 被 hidden。缺跨请求 session epoch 防护，见 W1。 |
| Notes | `app.js:1462-1748,4208-4363`；GET/POST/PATCH/DELETE thoughts、retry-refine、reopen-session | 列表、详情、状态、Runtime 四 tab；删除确认、重试、重开 Capture、素材入队、分享链接。PATCH 要 X-Session-Id；typed 文档另有 generation 约束。Thought 详情读取无乱序保护。 |
| Search | `app.js:3917-4033`；GET /api/search | 搜索词+标签、预览、打开笔记、选择/素材队列、复制分享链接。UI 固定 page=1/page_size=20，未提供高级模式/日期/分页控件；API 能力不等于 UI 都暴露。旧请求可覆盖新查询，见 W2。 |
| Topics | `app.js:1816-2105`；topics CRUD 中 C/R/U、refresh、candidates | 列表过滤、detail/rules、规则编辑、刷新、成员预览/入素材队列、候选影响展示已接通。无 topic 删除路由。候选卡片只读（1931-1955），无 weave preview/accept 按钮。 |
| Topic weave API | `service.go:1280-1358`；weave-preview、weave-accept、weave-proposals 列表/详情 | 后端真实调用 Topic 服务，但 Web 当前无对应交互入口；不要标记为 Web 端人工审核完整闭环。 |
| Write / Compose | `app.js:4378-4783`；sources GET/PUT/DELETE，drafts create/list/get/delete/save | 后端共享素材队列、If-Match CAS、写入串行化、BroadcastChannel 通知后重新 GET；生成异步 job、1.5 秒兜底轮询、草稿预览/编辑/保存、脏编辑切换确认已实现。缺失 compose SSE 导致其他 tab 的保存/删除同步不可靠；本 tab 有 job/轮询兜底，不能称生成全面失效。 |
| Profile / 文档入口 | `service.go:1054-1141`、`app.js:769-809,2689-2701,5379+` | Profile 列表/详情/validate/publish/reload API、Capture 切换、Compose 动态选择、About 展示和 5 秒刷新；typed 文档编辑/审阅/确认/导出另由专门审阅覆盖。 |
| About | index.html:481+；app.js:786-809 | 功能说明、状态与动态 Profile/registry issues；不是管理配置编辑器。 |
| Settings | index.html:710-789；app.js:1768-1815 | General/Models/Sync/Index/Events；语言、帮助可见性、状态、指标、重建索引、事件过滤；没有 Key 输入或外部请求开关。 |
| Jobs / 诊断 | service.go:1360-1460,1508-1763,1945-1971 | jobs 单项/列表过滤，system status/privacy/metrics，Prometheus /metrics，live/ready，reindex。没有用户取消 job 的 API/UI；canceled 是模型/后台状态，不是已实现的取消按钮。 |
| SSE | module.go:111-138；service.go:1461-1506；eventstream/stream.go | 内存 200 条历史、Last-Event-ID、types 过滤、20 秒 ping、shutdown context。浏览器 EventSource 自带重连；不是持久化事件日志，慢客户端队列满会静默丢事件。漏事件订阅见 W5。 |
| 公共分享 | service.go:479-625；app.js:4036-4069 | 全局开关默认 false；/p/thoughts/{id} 返回 Markdown/HTML/JSON，可协商，HTML 转义，MD/HTML nosniff；启用后全体可识别 thought ID 都能读取，无逐笔记发布/撤销。 |
| 双语 / Markdown / 可访问性 | web/i18n、app.js:908-1264；index.html | 双语字典、Markdown 渲染转义/安全链接、抽屉焦点管理、live region、确认框有现有测试覆盖。浏览器 smoke 本地执行受环境阻断，详见第三节。 |
| 无 Key | appconfig/config.go:272-288；service.go:1693-1763 | LLM/Embedding 未配置会显式 not_configured，核心 ready 判据不强制 Key，因此可 ready=true 同时 status=degraded；本地降级不是启动失败。Reader 默认仍 enabled=true，不能把无 Key 等同离线。 |

路由总表在 `internal/modules/application/thoughtflow/service/service.go:224-294`，不是根据文档猜测。

### 三、优先修复的确定性问题

#### W1 / P1：切换 Capture 会话后旧响应覆盖新会话的内容和预览

- 位置：`web/app.js:2995-3009`（switchCaptureSession）。先写 state.capture.sessionId，GET 返回后无 session ID/请求序号核验，直接替换 activeScratchpad、archivePreview、messages。
- 触发：依次点历史会话 A、B；B 先返回，A 后返回。最终 sessionId 为 B，activeScratchpad.session_id 和消息来自 A。
- 后果：用户看到 A 的内容却向 B 提交消息；归档预览、策略所引用的上下文也可能来自 A。属于数据操作目标/展示内容错配，不只是闪烁。
- 复现：`node --test audit-web-repro.test.js` 的 `AUDIT old Capture response overwrites newly selected session`。断言观测到 B/A/A 三项矛盾状态，成功复现。
- 同类风险：appendSessionMessage（3164-3190）、previewArchive（3644-3684）跨 await 使用当前全局 session，也没有 session epoch。W1 复现仅覆盖历史切换，不把未测试的每条路径都记独立漏洞。
- 建议：所有会话操作捕获 sessionId + epoch；迟到响应不得更新不同会话；跨 await 写操作使用原始 ID。

#### W2 / P2：搜索旧请求晚返回时覆盖新结果，甚至清空搜索后结果复活

- 位置：`web/app.js:3917-3937`。GET 后无序列检查/AbortController，直接写 lastResults/renderResults。
- 触发：提交 old、立即提交 new，new 先完成；old 后完成后 UI 显示 old 结果，输入仍是 new。清空输入的本地 idle 分支也未取消旧 GET。
- 复现：同脚本 `AUDIT search stale response replaces newer query`，最终 thought_id=old。
- 相似未独立复现路径：openTopic（1877-1897）、previewThought（4208-4227）、loadComposeDraft（4686-4721）；applyRoute 的序号检查在调用结果渲染之后，不能为这些 loader 提供完整保护。

#### W3 / P1：隐私报告漏报默认无 Key Reader 外发，且 Web 隐私提示未接线

- 位置：`service.go:1784-1801,1804-1820,1852-1870,1883-1904`。
- 默认 Reader 配置：`appconfig/config.go:284-288` enabled=true、Jina URL、空 API key。
- 真实行为：`webfetch/fetch.go:80-100` 本地正文不足/失败回退 Reader；`208-222` 只看 readerEnabled，key 为空也会向 Reader 发送目标 URL。
- 报告却用 Configured（key 非空）判断 readerSurfaces/commitSurfaces/urlActionHint，所以默认无 key 下 surfaces 不列 Reader，并称 URL 抓取走本地解析；全无 key 时 commit hint 为“不触发外部请求”。真实 URL 归档/加工可发生外发。
- 反向错误：Reader.Enabled=false 但保留 key 时 buildSurface.Enabled 固定 true，报告仍显示 Reader 活跃。
- Web 搜索确认：app.js、index.html 中没有 /api/system/privacy 消费或隐私提示入口。功能设计 `doc/thoughtflow-functional-design.md:676-689` 要求轻量外发提示；该项当前应记未完成而非已有完整隐私 UI。
- 建议：enabled 与 credential configured 分离；基于真实 provider 开关生成 surfaces；URL 原站请求/Reader fallback/LLM/Embedding 分别披露，并接入 UI。无需强制每次确认，但必须准确。

#### W4 / P2：通用 api() 丢弃结构化错误码，Refiner 锁重试永远不触发

- 位置：`app.js:693-697` throw new Error 仅保留 message；`3757-3802` patchActiveThought 检查 error.code。
- 触发：PATCH 返回 409，error.code=thoughtflow.capture.refining 或 locked。客户端 error.code 为 undefined，预设 1.2 秒后一次 retry、refining/locked 专用提示分支都失效。
- 复现：同脚本 `AUDIT api drops machine-readable error code`。
- 后端会返回机器码，见 `service.go:1983-2000`，不是服务端字段缺失。

#### W5 / P2：服务端 SSE 白名单漏 thought.patched 与 compose 生命周期事件

- 位置：`module.go:113-138`；前端期待见 `app.js:4795-4823,4841-4849,4874-4931`。
- 发布端存在：capture/biz/service.go:419 发布 thought.patched；compose/biz/service.go:716 发布 draft_deleted、881 发布 draft_saved，其余创建发布点 257/414/638。
- 实际 stream 未订阅 thought.patched、compose.draft_created/saved/deleted。因此后端产生事件不等于前端能收到；其他标签页面上的编辑/草稿删除不按事件更新。
- 严重性边界：Compose 生成仍有 job.updated 与 polling，可完成，不应误报为完全不能生成；PATCH 当前操作所在 tab 也会主动 refresh。
- 相反方向：服务端订阅了 thought.deleted，但前端 DOMAIN_EVENT_TYPES 未列它，其他标签删除普通 thought 后列表可能继续显示旧条目直到下次刷新。
- 测试缺口：browser fixture 手工发事件能绕过生产 module 白名单；应增加真实 module→eventhub→SSE→UI 契约测试。

### 四、附录：其他已确认缺陷和边界

#### W6 / P2：跨标签锁使用各页面不同基准的 performance.now

`web/session-lock.js:22-24,55-57,84-85` 将别的标签 payload.ts 与本页面 performance.now 相减。新标签打开 1 秒获得锁，老标签已运行 200 秒，则老标签立即判过期并成功 acquire；反向会异常延长 TTL。`node audit-lock-clock.js` 已用双 VM BroadcastChannel 复现，输出 old tab can acquire: true。建议收到 remote heartbeat 时记录本标签接收时间或使用一致时间基准。现有 session-lock.test.js 的 BroadcastChannel=undefined，只测单实例。

#### W7 / P2：/新建之后旧 thought 锁的 heartbeat 未释放

`web/app.js:3732-3753` 先把 activeThoughtId 清空、sessionId 换成新值，再 if(activeThoughtId) release，因此分支永不执行。`audit-web-repro.test.js` 第三个测试确认 releaseCalls=0。旧 owner heartbeat 仍继续，可使另一标签持续看到锁，直至原标签卸载/显式处理。注意 newCaptureSession（3855+）是另一路径，释放顺序正确，不能混为一谈。

#### W8 / P2：SSE 取消订阅与发布并发存在 send-on-closed-channel

`eventstream/stream.go:62-74` 复制 subscribers 后解锁发送；`141-146` 取消 goroutine 在同一 mutex 内 close channel。存在复制→close→send 的合法交错。生产调用由 Notify→Publish；vendored `magicCommon/event/hub.go:230-239` notificationEvent 有 recover，因此已核实影响是该次投递中断、尚未投递的其他订阅者丢事件及错误日志，不声称生产进程必然崩溃。历史记录仍有该事件，但持续在线的漏投 tab 不会自动请求 replay。

另准备过最小压力测试，但未纳入已运行测试或交付复现包。运行未完成：本机 `/usr/bin/go` 并非 Go 编译工具，`go test` 返回 `Go: Unknown option: test`。这项依据是可验证的锁/close/send 交错静态证明，不宣称已实测 panic。修复应统一 channel 生命周期与发送保护；回归运行 race/stress 并发订阅取消。

#### W9 / P3：SSE 心跳配置无效

`service.go:1487` 固定 `time.NewTicker(20 * time.Second)`；`appconfig/config.go` 已加载 events.sse_heartbeat_seconds 并计算 SSEHeartbeat。设置为其他值没有作用，文档仍把它列为配置项。应使用配置并校验正数。未在本环境等待真实 SSE 时间间隔。

#### W10 / P3：README 的 config-dir 下数据路径描述与实现不符

README 最后一段称首次在 `<config-dir>/thoughtflow-workspace` / `<config-dir>/thoughtflow-runtime` 建立数据。实际默认是 CWD 相对路径（appconfig/config.go:228-234，workspace/workspace.go:16，appconfig.RuntimeStateDir）；LoadWithConfigDir 没有把这两个路径 join 到 configDir。用户按 release 示例带 -config-dir 启动时可能在错误位置找数据。另使用文档说 public route “仅启用时注册”，但实现始终注册、handler 动态检查，属于文档口径小偏差。

### 五、安全/部署边界，不伪装为已证实攻击

- 默认绑定 127.0.0.1:8080；无应用账户/鉴权层，X-Session-Id 是写冲突标识不是认证。整个服务通过 0.0.0.0 或反向代理暴露时，/api/thoughts、sessions、jobs、system 等同样可读写。public_thoughts_enabled=false 只关 /p，不保护普通 API；public_base_url 也不会自动配置反向代理隔离。部署文档应明确只代理 /p 或为管理 API 增加外层认证。此为已确认访问模型/部署风险，不把默认 loopback 模式描述成已发生数据泄漏。
- 公共分享粒度是全局所有 thought，不存在逐笔记 token、ACL 或显式 published 列表。Markdown/HTML 公共响应含 raw Markdown/front matter；JSON 返回 snapshot，不能把它称“仅精简正文”。是否需要脱敏取决于产品公开契约。
- HTTP decodeJSONBody 有 1MiB 限制+unknown-field 拒绝，但旧 create thought/topic 等仍直接 Decoder(req.Body)。请求体限额并未统一；未做 DoS/跨站攻击实验，不作已利用结论。
- 未发现前端取消网络请求/后台 job 的实际接口；确认框取消是执行前拒绝，不是已开始操作的取消。
- 新 Key/配置并不执行健康探测：LLM/Embedding 的 status=ready 代表 key 非空，不代表远端调用已成功。

### 六、验证账本与需要补充的测试

已执行（均在临时文件、仓库源代码未改）：

1. `node --test audit-web-repro.test.js`：4 tests / 4 pass。这里 pass 表示“确实观察到缺陷”，不是修复通过。
   - api drops machine-readable error code
   - old Capture response overwrites newly selected session
   - new session fails to release existing live-tab lock
   - search stale response replaces newer query
2. `node audit-lock-clock.js`：跨标签时钟差使新锁立即过期，另一标签 acquire=true。
3. Go overlay 压力测试：因环境缺少真实 Go toolchain 未执行成功，不能填通过。
4. 真正浏览器测试的环境阻断及远端 CI 结果见第三节。

现有覆盖优点：路由/i18n/安全 Markdown、Compose 队列 CAS/迟到 GET 防护、Capture SSE 气泡刷新、public format/config 热更新、profiles、系统状态、双语错误已有测试。显著缺口：多会话乱序、真正双标签 BroadcastChannel 不同 timeOrigin、error.code 到重试的闭环、默认无 key Reader 隐私、生产事件白名单完整性、SSE 发布与取消并发、可配置心跳实际行为。


## 九、建议的收口批次与验收门槛

### 第一批：保护目标身份、内容与隐私

优先处理 W1、D2、STG-02/STG-03：所有读取/异步结果必须绑定资源身份；专题候选更新不得覆盖正式正文；同资源更新必须 CAS 或串行。随后闭合 D1/D5/W3：所有实际链接节点与候选来源入口一致检查，隐私说明匹配真实外发行为。

验收：交错响应不得串会话；A 文件伪造 B ID 不得改变任何 HEAD；人工专题段落在无关事件后字节不变；两并发成员均保留；敏感 URL 在候选发布前或正式定稿前被对应规则拒绝。

### 第二批：打通真实输入到输出

处理 CP-01/02/03、STG-01：真实 URL 对话能归档；模型实际收到抓取正文；不同 Compose 来源实际内容进入生成；真实专题事件能产生 Git commit 且不带私有候选。

验收不能只断言 status、数量或链接存在。测试输入放置唯一事实标识，检查 provider 输入及最终产物对该事实的可追溯性。

### 第三批：并发、事件、索引与恢复

处理 CP-04/05、STG-04..11、W2/W4..8、D4：后台 busy 不伪报成功，事件白名单端到端一致；向量按正文/model/hash 失效；重建单飞且不破坏旧可用索引；Git 失败保留待重试集合；跨标签锁不依赖跨页面 performance.now 相减。

验收加入 barrier、迟到响应、关页/断流、模块重启、provider 错误、半写/文件消失等故障测试。组件和集成测试都要有，不能以同步 stub 代替真实遵守 context 的异步分支。

### 第四批：能力声明、配置和发布文档

明确 semantic/hybrid 目前是内部能力，不把它写成已在 Web 暴露；Topic weave 有 API 不等于有 Web 审核界面；Git sync 当前是本地提交，不是多端推拉；无 Key 不等于离线。修正 DuckDB build tag/fallback、默认目录、SSE heartbeat 等文档/实现差异。

D3 的自定义 H3–H6 修复要么实现，要么收紧可发布 schema。旧格式兼容是否属于当前验收豁免，应由用户明确决定，不以仓库自述代替授权。

### 完成定义

1. 本报告 P1 全部关闭；P2 修复或逐项明确接受范围，不用笼统“已收口”。
2. 每项问题都有能在修复前失败、修复后通过的用例，尤其跨模块事件和实际 provider 输入。
3. 在固定新 SHA 上执行 make ci-check；单独执行相关 race；区分 skipped、环境阻断和真实通过。
4. 关键端到端链路覆盖文本/URL、多来源、无 Key/仅 LLM/仅 embedding/双 Key、普通/typed、成功/失败/重启/并发。
5. 形成最终证据表：功能、入口、输入、持久化产物、事件、测试、限制、对应提交。
6. 保持已修复的删除撤销、Git 暂存隔离、Compose 幂等保存和 frozen document CAS 回归，不为修新问题回退这些保障。

本报告不执行上述改造，也不承诺未经请求的持续监控；下一步可直接以问题编号分批开发和复核。

## 十、部署与运维补充

- 当前是本地优先、单工作区单写进程架构；默认 loopback。X-Session-Id 不是认证凭证，不能将完整管理 API 裸露到公网。
- /p 公开开关不保护 /api；若用反向代理分享，仅代理明确的公开路径，管理面另行鉴权。
- Markdown 是正式内容；runtime 同时包含证据摘录、输入冻结和任务状态，不能把“索引可重建”外推为 runtime 全部可随意删除。备份内容和所需私有证据时应明确权限。
- Release workflow 配置 linux/amd64、linux/arm64、darwin/arm64，使用 CGO 和静态 DuckDB；不是纯 Go 任意平台单命令交叉编译，也未见当前 Windows 发布项。
- scripts/cleanup-archived-thoughts.go 是对固定历史 ID 的一次性清理脚本，默认 dry-run；不是通用迁移或任意工作区维护功能，本轮未运行。

## 十一、源码索引

所有链接固定到本报告 SHA，避免 master 后续变化使定位漂移。

- [internal/modules/capture/biz/service.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/modules/capture/biz/service.go)
- [internal/modules/capture/biz/scratchpad.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/modules/capture/biz/scratchpad.go)
- [internal/modules/refiner/biz/service.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/modules/refiner/biz/service.go)
- [internal/modules/expander/biz/service.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/modules/expander/biz/service.go)
- [internal/modules/compose/biz/service.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/modules/compose/biz/service.go)
- [internal/pkg/ai/refiner.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/pkg/ai/refiner.go)
- [internal/pkg/webfetch/fetch.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/pkg/webfetch/fetch.go)
- [internal/modules/search/biz/service.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/modules/search/biz/service.go)
- [internal/pkg/searchdb/store.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/pkg/searchdb/store.go)
- [internal/modules/topic/biz/service.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/modules/topic/biz/service.go)
- [internal/pkg/topicstore/store.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/pkg/topicstore/store.go)
- [internal/modules/git_sync/biz/service.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/modules/git_sync/biz/service.go)
- [internal/pkg/documentprofile/registry.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/pkg/documentprofile/registry.go)
- [internal/pkg/documentvalidation/validate.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/pkg/documentvalidation/validate.go)
- [internal/pkg/documentflow/service.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/pkg/documentflow/service.go)
- [internal/pkg/documentflow/candidate.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/pkg/documentflow/candidate.go)
- [internal/pkg/documentstore/store.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/pkg/documentstore/store.go)
- [internal/pkg/markdown/thought.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/pkg/markdown/thought.go)
- [internal/pkg/eventstream/stream.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/pkg/eventstream/stream.go)
- [internal/pkg/jobstore/store.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/pkg/jobstore/store.go)
- [internal/modules/application/thoughtflow/module.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/modules/application/thoughtflow/module.go)
- [internal/modules/application/thoughtflow/service/service.go](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/internal/modules/application/thoughtflow/service/service.go)
- [web/app.js](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/web/app.js)
- [web/session-lock.js](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/web/session-lock.js)
- [.github/workflows/ci.yml](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/.github/workflows/ci.yml)
- [.github/workflows/release.yml](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/.github/workflows/release.yml)
- [doc/Typed-Documents-Implementation.md](https://github.com/muidea/thought-flow/blob/82b91d5e3abaee0de70a975d88925b56b7a6f401/doc/Typed-Documents-Implementation.md)

## 附录：当前注册入口清单

此清单证明入口存在，不代表每条入口的所有业务分支已验证。共 74 条方法/路径注册，包含静态资源。

| 方法 | 路径 | 处理器 |
|---|---|---|
| GET | `/api/documents/:id` | `s.handleDocument` |
| POST | `/api/documents/:id/revisions` | `s.handleDocument` |
| POST | `/api/documents/:id/validate` | `s.handleDocument` |
| GET | `/api/documents/:id/validation-reports/:report_id` | `s.handleDocument` |
| POST | `/api/documents/:id/repair` | `s.handleDocument` |
| POST | `/api/documents/:id/finalize` | `s.handleDocument` |
| GET | `/api/documents/:id/revisions/:revision_id/export` | `s.handleDocument` |
| GET | `/p/documents/:id/revisions/:revision_id` | `s.handlePublicDocument` |
| GET | `/` | `s.handleWeb` |
| GET | `/index.html` | `s.handleWeb` |
| GET | `/styles.css` | `s.handleWeb` |
| GET | `/app.js` | `s.handleWeb` |
| GET | `/session-lock.js` | `s.handleWeb` |
| GET | `/i18n/index.js` | `s.handleWeb` |
| GET | `/i18n/zh-CN.js` | `s.handleWeb` |
| GET | `/i18n/en-US.js` | `s.handleWeb` |
| GET | `/vendor/markdown-it.min.js` | `s.handleWeb` |
| GET | `/vendor/markdown-it.LICENSE` | `s.handleWeb` |
| GET | `/assets/thoughtflow.png` | `s.handleWeb` |
| GET | `/api/thoughts` | `s.handleListThoughts` |
| POST | `/api/thoughts` | `s.handleCreateThought` |
| POST | `/api/thoughts/:id/retry-refine` | `s.handleRetryRefine` |
| GET | `/api/thoughts/:id/suggest` | `s.handleThoughtSuggest` |
| GET | `/api/thoughts/:id` | `s.handleGetThought` |
| PATCH | `/api/thoughts/:id` | `s.handlePatchThought` |
| DELETE | `/api/thoughts/:id` | `s.handleDeleteThought` |
| POST | `/api/thoughts/:id/reopen-session` | `s.handleReopenSession` |
| GET | `/p/thoughts/:id` | `s.handlePublicThought` |
| POST | `/api/capture/sessions` | `s.handleCreateSession` |
| GET | `/api/capture/sessions` | `s.handleListSessions` |
| GET | `/api/capture/sessions/active` | `s.handleGetActiveSession` |
| GET | `/api/capture/sessions/:id` | `s.handleGetSession` |
| DELETE | `/api/capture/sessions/:id` | `s.handleDeleteSession` |
| POST | `/api/capture/sessions/:id/messages` | `s.handleSessionMessage` |
| POST | `/api/capture/sessions/:id/context` | `s.handleSessionContext` |
| POST | `/api/capture/sessions/:id/intent` | `s.handleSessionIntent` |
| POST | `/api/capture/sessions/:id/strategy` | `s.handleSessionStrategy` |
| POST | `/api/capture/sessions/:id/profile` | `s.handleSessionProfile` |
| GET | `/api/capture/sessions/:id/archive/preview` | `s.handleArchivePreview` |
| POST | `/api/capture/sessions/:id/archive` | `s.handleSessionArchive` |
| GET | `/api/search` | `s.handleSearch` |
| GET | `/api/compose/sources` | `s.handleGetComposeBasket` |
| PUT | `/api/compose/sources` | `s.handleSaveComposeBasket` |
| DELETE | `/api/compose/sources` | `s.handleClearComposeBasket` |
| GET | `/api/compose/drafts` | `s.handleListComposeDrafts` |
| POST | `/api/compose/drafts` | `s.handleCreateComposeDraft` |
| GET | `/api/compose/drafts/:id` | `s.handleGetComposeDraft` |
| DELETE | `/api/compose/drafts/:id` | `s.handleDeleteComposeDraft` |
| POST | `/api/compose/drafts/:id/save` | `s.handleSaveComposeDraft` |
| GET | `/api/document-profiles` | `s.handleListDocumentProfiles` |
| POST | `/api/document-profiles/validate` | `s.handleValidateDocumentProfile` |
| POST | `/api/document-profiles/reload` | `s.handleReloadDocumentProfiles` |
| POST | `/api/document-profiles/publish` | `s.handlePublishDocumentProfile` |
| GET | `/api/document-profiles/:id` | `s.handleGetDocumentProfile` |
| GET | `/api/topics` | `s.handleListTopics` |
| POST | `/api/topics` | `s.handleCreateTopic` |
| POST | `/api/topics/:id/refresh` | `s.handleRefreshTopic` |
| POST | `/api/topics/:id/weave-preview` | `s.handlePreviewWeave` |
| POST | `/api/topics/:id/weave-accept` | `s.handleAcceptWeave` |
| GET | `/api/topics/:id/weave-proposals/:proposal_id` | `s.handleGetWeaveProposal` |
| GET | `/api/topics/:id/weave-proposals` | `s.handleListWeaveProposals` |
| GET | `/api/topics/:id/candidates` | `s.handleListSessionCandidates` |
| GET | `/api/topics/:id` | `s.handleGetTopic` |
| PUT | `/api/topics/:id` | `s.handleUpdateTopic` |
| GET | `/api/jobs` | `s.handleListJobs` |
| GET | `/api/jobs/:id` | `s.handleGetJob` |
| GET | `/api/events` | `s.handleEvents` |
| GET | `/api/system/status` | `s.handleSystemStatus` |
| GET | `/api/system/privacy` | `s.handlePrivacyReport` |
| GET | `/api/system/metrics` | `s.handleSystemMetrics` |
| POST | `/api/system/reindex` | `s.handleReindex` |
| GET | `/metrics` | `s.handlePrometheusMetrics` |
| GET | `/health/live` | `s.handleLive` |
| GET | `/health/ready` | `s.handleReady` |

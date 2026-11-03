# ThoughtFlow 工程设计生成与交付实施记录

- 日期：2026-11-03。
- 对照：`ThoughtFlow_设计生成与交付代码收口方案_v1.0.md`（正文版本 v1.1）。
- 开发基线：`a26c815947d0f38007eb4c816cf0569425f525f9`；本记录描述其后的当前工作树，尚不绑定最终提交 SHA。
- 当前范围：ThoughtFlow 本地生产端实现及确定性回归。没有修改 Workorch、调用真实外部模型、操作真实用户工作区、自动迁移旧数据或创建消费者批准/Run。

## 1 固定实施决策

| 决策 | 实施选择 |
| --- | --- |
| D-01/02/09 | 沿用内部 schema 1 / `document-set-v2`；新增工程用途明确固定为 `engineering-design-v1`，生成策略 `engineering-prompts-v1`。不支持的 Profile/公开格式被拒绝，不改写旧字节，不实施历史兼容迁移。 |
| D-03/04 | 扩展原 Fact：verification.acceptance、constraint.quality/state_rule、artifact_spec.artifact；`FactMeaning` 统一排除 hash/私有引文 envelope，保留全部规范字段。 |
| D-05 | Capture 对话归档自动匹配统一 Profile 目录；软件功能设计与交付自动进入 `builtin.engineering-design-set` 规划与生成，无需再次点击生成。明确单篇、笔记、调研和原 feature/research Profile 保持用途隔离。 |
| D-06/12 | 公开 DTO 采用允许列表，固定 `engineering-public-v1`、`engineering-markdown-html-v1`、`public-json-jcs-v1`；审查前形成公开描述符和派生文件摘要。 |
| D-07/13 | 适配基线固定为 Workorch `05a823064e1b99aa60e8ba38c1e9d090cf2c5fe0`；只生成本地材料和预算预览，不代表该消费者已接收或完成联调。 |
| D-08 | 真实模型名单、调用次数、费用和门槛未另行冻结；本轮不调用真实外部模型，不声明真实质量通过。 |
| D-10 | 强制 schema、来源、hash、范围及公开完整性没有人工豁免旁路。非阻断问题仍须在套级风险台账处置。 |
| D-11 | 历史引用使用 set/revision/fact/hash 精确端点；从经校验的保留祖先解析，输入提供的历史副本不被直接信任。支持 split/merge/replace/retire/restore，保留原修订。 |

普通 Profile 的历史协议没有因为新增工程能力而被统一升级；工程 Profile/提示/公开转换的后续不兼容修改必须显式更换对应版本并重新审查，不能给原 PASS 临时补 hash。

## 2 TF-01～TF-12 对应实现

| 任务 | 本地实现 | 主要回归/证据入口 |
| --- | --- | --- |
| TF-01 必需内容 | 六类工程角色；架构、业务行为、接口、数据、恢复、质量、验收与 artifact 内容义务；DEFINED/NOT_APPLICABLE/UNRESOLVED；每个模块实例都需落点。内置恢复/质量义务分别要求 state_rule/quality，不能互相充数。 | `documentset/engineering_test.go`：工程缺口、未知适用性、第二模块不能遗漏、错误 payload 阻断。 |
| TF-02 结构化合同 | 模态、规范/可选、来源类别、范围、验收/状态/质量/artifact payload；统一语义身份、正文合同块及类型校验；权威定义唯一。 | 规范字段变异、非法类型/关系、公开语义与权威正文匹配。 |
| TF-03 确认与问题 | confirmed 必须有用户精确引文；模型/实现推导不能冒充已确认；问题可表达关联事实/范围/义务/验收/资源、责任角色、选项、依据和解决条件；关闭/降级需要新用户依据，未解决的有类型问题不能被省略。 | `TestEngineeringNewPayloadsNeedNewApproval`、`TestEngineeringOpenIssuesCannotBeDeletedOrClosedWithOldQuote`。 |
| TF-04 关系与覆盖 | 注册关系及方向；只对前置依赖图检查环；范围保留全体要求及排除，禁止依赖被排除项；需求必须有设计责任与可判定验收，可选/未确认事实不能承担必需义务或实现/验收覆盖；历史替代不计为当前验收。 | 类型关系、覆盖、范围依赖、可选事实绕过阻断与多修订生命周期测试。 |
| TF-05 artifact | 交付形态、文件、入口、构建、运行、环境、配置、数据、使用、安全、许可及验证要求进入单一权威 Fact。 | 缺 artifact、缺必需验收字段的阻断反例。这里只验证设计合同，未执行业务 artifact 构建。 |
| TF-06 工程生成 | Purpose/生成策略贯穿调用；传入角色、范围、义务、适用事实/关系、问题、固定上游正文及 hash；工程空章节报缺口，不用本地通用补全。重试延用原 ModelHash/预算/任务机制。 | `TestEngineeringGenerationPublicationHandoffAndRestore`；AI 工程空章节及本地补全拒绝；原 Capture/研究回归。 |
| TF-07 质量门禁 | 规则检查与独立联合审查；公开描述符作为审查项的冻结输入；Report 同时绑定 CandidateHash/publicPayloadHash/投影版本；Ready/Finalize/Delivery 重验；非阻断风险保留并要求处置。 | 单篇/套级既有门禁回归；公开身份变异、跨包审查、实际资源篡改拒绝。 |
| TF-08 修订影响 | 新规范字段进入确认保护；范围/义务/关系变更要求新依据；事实/关系/范围/问题/条件/输入变化进入影响闭包；显示需重生成和可复用候选；最终复用仍校验磁盘字节、Profile、来源与依赖；权威/范围编辑走提案。 | 新字段授权、受管说明文字与合同编辑、多修订不可变性、既有 CAS/重试/恢复回归。 |
| TF-09 公开包 | 允许列表模型、完整范围/关系/义务/必要历史描述、规范正文、MD/HTML/资源映射与摘要、公开审查、公开 schema 说明；独立检查器；公开分发需要明确授权。 | `TestEngineeringPublicPackageAndMixedReview`、真实临时目录的附件及备份往返、JCS Go/JavaScript 向量。 |
| TF-10 有界交接 | Goal/直接验收/设计资源分通道；完整事实与范围按不可分割记录装入有序 UTF-8 资源；精确字节/条数/模板下界检查；输入确认绑定内容、顺序、修订、资源与模板；不造消费者身份、映射或批准。 | `TestHandoffSourceMappingAndLayeredBudgets`、UTF-8/4096/32768 边界、128037/132096-byte 模板反例。真实消费者部分 NOT_RUN。 |
| TF-11 API/Web | 既有 Profile/规划/任务/审查/阅读链路扩展；缺口保留事实 ID 和正文定位，显示影响摘要；新增双语交接对话框，展示通道/预算/待验证层，编辑使精确确认失效；固定版本和异步作用域保护沿用。 | Node 缺口与 JCS 测试；实际 Chromium 交接交互；原固定版本、历史导航、SSE/API 回归。 |
| TF-12 回归/评估 | 确定性 fixture 区分生产代码、模型测试替身和实际外部模型；本地机制门禁与真实模型/消费者证据分开记录。 | 本文第 5 节；第 6 节为未执行项目，不统计为通过。 |

实现文件集中在 `internal/pkg/documentset/{engineering*,public*}.go`、`documentsetflow/engineering.go`、`documentsetstore/{public,handoff}.go`；同时接入原 profile、生成/审查、存储、受管编辑、HTTP 与 Web 路径。

## 3 公开身份与离线校验

依赖顺序：冻结 Plan/Members → 内部 CandidateHash；公开合同/规范正文/派生文件与资源清单 → publicPayloadHash；两者与投影版本 → 最终 Report → 内部 RevisionHash；公开审查 → publicReviewHash；最后形成分发文件清单。公开描述符不包含自身摘要、审查摘要或内部修订 hash，不存在自引用。

`design.json` 是版本化公开描述符；`review.json` 绑定公开摘要并保存必要的审查/风险处置；`public-schema.md` 随载荷冻结。内部候选/审查 hash 仅作来源引用。Go 与 JavaScript 使用同一组规范化向量，包括 UTF-16 排序、Unicode 分隔符、字面转义、空值及安全整数。

离线运行：

```sh
go run ./scripts/designcheck -expected-payload 'sha256:<可信渠道固定的公开摘要>' delivery.zip
```

省略 `-expected-payload` 仍检查包内一致性，但输出 `publisher_authentication=NOT_AUTHENTICATED`。没有外部可信锚点时，不声称已认证发布者。检查器拒绝缺/多文件、不安全路径、重复路径、错误 hash、跨版本转换、规范正文不符、合同/范围/历史引用不闭合及审查错配；即使重算分发清单也不能掩盖这些混包。

公开文本/附件检查会拒绝已识别的私有路径、敏感链接和凭据形态。它不能自动证明任意自然语言或二进制内容均可对外公开，因此工程 delivery 与 handoff 导出另要求明确的 `public_authorized`；不会悄悄删去敏感的必需合同以获得 complete。

资源在公开包中使用稳定的 ASCII 路径 `assets/<resourceID>/asset.<ext>`，原始 Unicode 文件名保留在描述符 `resources[].name`，不将上传文件名当作物理包路径。中文文件名的正常交付及附件字节篡改均有回归。

## 4 API 与交接状态

- `POST /api/document-sets/:id/handoff-previews`：`expected_generation`、固定 `set_revision_id`，可选 Goal、直接验收、真实消费者身份和模板快照；只接受当前已就绪定稿。
- `POST /api/document-sets/:id/handoff-exports`：同一输入，额外要求 `public_authorized=true`。没有精确输入确认时输出待确认材料；不会生成正式消费者请求/批准/Run。
- 原 `/exports` 在工程 `kind=delivery` 时同样要求 `public_authorized=true`，并走完整公开校验，不借一般导出路径放宽门禁。

所有设计事实默认进入带来源类别、知识状态、模态和固定事实引用的文本资源；自动抽取不会进入 `AcceptanceRequirements`。只有本次显式提供/采用的直接验收进入候选直接通道。预览给出 `input_confirmation_hash`；确认该值只记为 `CONFIRMED_PRODUCER_INPUT`，证明本地精确材料采用。映射的 `producer_input_adoption` 与 `consumer_confirmation` 分开，后者仍是 `PENDING_CONSUMER_CONFIRMATION`；生产端不代写消费者正式输入确认、规格批准、接收或执行授权。A→B→A 的正式输入代次由真实消费者管理，本地 hash 不能替代该状态机。

预算分别计算：输入 131072 bytes、Goal 32768 bytes、直接验收最多 128 条且每条 4096 bytes、有序资源最多 8 份且每份 32768 bytes/合计 131072 bytes、资源引用 1024 bytes、TemplateID/WorkspaceID 各 256 bytes。模板已知下界另按直接验收加真实模板条目检查；模板未知、派生 AdditionalItems、256 KiB 实际请求组装、File owner 落地及消费者创作审查保持 PENDING/NOT_RUN。超限阻塞，不截断事实、不删验收、不改写模板、不伪造消费者 ID。

## 5 本地执行记录

以下只记录实际执行范围，不代表远端精确 SHA CI 或真实模型质量。早期并行检查出现过测试超时及既有短时限断言失败，早期 race 构建也未完成；这些尝试不计为 PASS。最终检查使用 `GOMAXPROCS=2`，完整矩阵另设 `GOFLAGS=-p=2`，工程三包 race 设 `-p 2 -timeout=20m`，Capture 自动归档增量的定向 race 设 `-p 2 -timeout=10m`，以限制检查的并发负载。

| 检查 | 状态 | 证据 |
| --- | --- | --- |
| 针对性工程合同/公开包/交接测试 | PASS | 本地 `go test` 的 TestEngineering/TestPublic/TestHandoff/TestCanonical；真实临时文件、发布/定稿、附件篡改、备份恢复。 |
| HTTP 规划→发布→定稿→公开授权→离线验证→交接 | PASS | `TestEngineeringHTTPPlanPublishPublicAuthorizationAndHandoff`；真实 HTTP handler、临时工作区和持久任务，拒绝缺公开授权及过期 generation。模型/审查仍为确定性替身。 |
| 拆分→合并→退役→恢复旧要求 | PASS | `TestEngineeringSplitMergeRetireRestoreAndBackupClosure`；核对当前范围分母、原修订身份、祖先引用、备份恢复后离线交付。 |
| Chromium 交接交互 | PASS | `engineering handoff previews preserve source channels and invalidate stale confirmations`；实际点击、输入、确认失效及刷新预览。 |
| 完整 `make ci-check` | PASS | 自动归档链路版本（按钮移除前）的工作树 `GOMAXPROCS=2 GOFLAGS=-p=2 make ci-check`：fmt、全量 Go、构建、JS 语法、Node、i18n、完整 Chromium 浏览器与 API/SSE e2e 均通过。 |
| 定向 Go race | PASS | 工程合同修复阶段 documentset/documentsetflow/documentsetstore 三包通过；Capture 自动归档增量完成后，对 application/thoughtflow/service 与 documentsetflow 的新增归档、竞争、澄清与绑定回归定向重跑通过。具体命令与结果见文末。 |
| 远端 Ubuntu/macOS 最终 SHA CI | NOT_RUN | 尚无本轮最终提交 SHA，不沿用基线 CI。 |

### 5.1 验收项证据映射

下表是自动化覆盖及待验收入口的映射，不将一个机制测试提升为自然语言质量或真实消费者已通过。既有任务、存储、Capture、Topic 与读取回归由完整矩阵保留。

| 验收 | 本地支撑与限制 |
| --- | --- |
| AC-01/02 | `TestEngineeringContractsAndPurposeIsolation`、`TestEngineeringEveryModuleNeedsObligationAndFreshImpact`、工程空章节测试：缺必需内容、未知适用性、多模块遗漏均不能成为 ready。 |
| AC-03/04 | 类型/方向/引用及完整权威合同块规则；公开检查器重算语义与正文映射。任意解释段落的语义矛盾仍需独立语义审查，确定性 reviewer 不证明真实识别率。 |
| AC-05/06 | `TestPublicIdentityAndSemanticMutation`、`TestEngineeringNewPayloadsNeedNewApproval`，统一语义 hash 与新增用户证据检查。 |
| AC-07 | `TestEngineeringOpenIssuesCannotBeDeletedOrClosedWithOldQuote`，未决阻塞问题不能消失、用旧引文关闭或无依据降级。 |
| AC-08/09a | 工程类型关系、需求实现/验收闭包及被排除前置依赖反例。 |
| AC-09b | handoff 全范围、排除、关系和历史载荷保留；这里只验证生产端材料，实际消费者理解未运行。 |
| AC-10 | artifact/acceptance 缺字段、质量阈值非数字等反例；未构建/运行实际业务产物。 |
| AC-11/12 | `TestEngineeringDraftCompletionNeverSuppliesFiller`、空章节阻断及原普通用途回归。 |
| AC-13 | 沿用 run 固定 ModelHash、重试模型身份拒绝与调用台账；未进行真实跨模型质量比较。 |
| AC-14 | 可确定的缺合同/验收/关系/失败约束规则和审查清单已接入；模型对任意自然语言冲突与遗漏的检出质量未独立评估。 |
| AC-15/16 | 原套级 Ready/Finalize/Export 状态与候选绑定回归；新增公开载荷/转换身份变更、混入审查、附件篡改均拒绝。 |
| AC-17/18 | 影响闭包与新规范字段失效；`TestEngineeringManagedNarrativeAndContractEditsStaySeparate` 验证人工合同修改走提案。 |
| AC-19 | 多修订生成、恢复、备份导入与不支持公开版本的拒绝；不执行旧用户数据升级。 |
| AC-20/21/22 | `TestEngineeringPublicPackageAndMixedReview`、中文附件交付与 tamper、离线检查器、私有 canary 和允许列表。隐私扫描能力有第 3 节限制。 |
| AC-23/25 | handoff UTF-8 单条/输入/资源/总量/条数边界，超限不裁剪、身份无效不显示 PASS；只导出材料，不创作批准/DoD/Run。 |
| AC-24a | 受控相对资源路径、固定映射和有序材料，路径逃逸与冻结资源变化拒绝。 |
| AC-24b | NOT_RUN：需真实 File owner 落地/归属/读取期变化验证。 |
| AC-26/27 | 现有问题/规划/重审路径保留，缺口与影响摘要 Node 回归；新增交接 Chromium 真实交互和双语/API；未进行独立完整产品易用性验收。 |
| AC-28 | 确定性正常/反例及多修订 fixture 已落地，明确区分生产代码与模型替身。 |
| AC-29/30 | NOT_RUN：真实模型预冻结评估/真实 Workorch 联调分别执行，见第 6 节。 |
| AC-31 | 原重复请求/CAS/取消迟到/投影中断保护回归继续运行，工程修订依赖同一任务状态机。 |
| AC-32 | Go/JavaScript 规范化共享向量；公开合同/正文/审查混包、清单重算、转换版本变动及可信摘要锚点检查。 |
| AC-33 | 生产端直接输入/设计资源分通道、精确输入确认绑定及变更失效；实际 USER_EXPLICIT/DERIVED 消费分类 NOT_RUN。 |
| AC-34 | `TestEngineeringSplitMergeRetireRestoreAndBackupClosure` 及错误 hash/非祖先反例；恢复后旧版本与公开历史描述仍闭合。 |
| AC-35 | 生产端模板下界 128037/132096-byte 反例、模板未知和变更失效；实际派生/组装预算 NOT_RUN。 |

## 6 不能替代的独立验收

- AC-29 真实跨模型重复评估：NOT_RUN。未获独立模型/费用/数据范围，不调用真实外部服务；确定性 reviewer 不作为真实内容质量证明。
- AC-24b、AC-30、AC-33 消费者层、AC-35 消费者层及 HC-01～HC-08 联合验证：NOT_RUN。生产端仅对固定消费者合同生成材料和本地检查；真实 ID、来源分类、派生覆盖、资源安全物化和组装预算需由真实 Workorch 验证。
- 实际业务 artifact 构建、运行和独立验收：NOT_RUN，属于 Workorch/实施环境；设计合同不冒充产物证据。
- 远端 CI 必须绑定最终提交；本地 `make ci-check` 不替代这一证据。

因此，本记录可以声明已实现并验证的生产端机制，不据此声明设计方案第 24 节的全部独立验收已经无保留通过。


## 2026-11-03 Capture 自动归档修正

此前首次归档仅在会话已有成果集计划时才进入成果集链路，且自动匹配目录只有单篇 Profile。现将已启用成果集的轻量描述加入同一匹配目录，输出种类由服务端从真实目录确定，并冻结 Profile ID／版本／hash。对话归档与提前到达的菜单归档均先分类，再直接执行成果集规划与生成。

任务通过 StartWithBinding 在启动前持久关联会话；生成使用原始冻结对话，保留既有范围、证据、预算、联合审查及发布门禁。阻断问题通过对话反馈，补充后延续已接受归档；等待补充状态明确传入模型，用户延期、取消或转入无关讨论时不强制归档。追加消息时同步归一化 new/update/supplement 策略，不依赖异步上下文才能得到默认值。重复同一操作复用既有任务；过期规划不能覆盖新用户输入。前端事件与会话轮询均可发现自动任务，刷新后恢复固定版本阅读入口。Capture 右上角不再保留“文档集工具”；按钮、绑定与手动生成向导均移除。Markdown 文件继续从 Thought 列表导入，成果集原件预览／映射 API 保留。

新增受控 HTTP 模型、真实临时工作区生成、澄清续跑、菜单提前归档、新输入竞争、Profile 用途隔离、契约变化、启动前绑定、Node 轮询及桌面／窄屏浏览器回归。验证结果以本次最终运行记录为准；未调用真实模型或外部交付消费者。


自动归档链路验证（移除手动入口前的工作树，无提交 SHA）：

| 检查 | 结果 | 记录 |
| --- | --- | --- |
| 完整矩阵 | PASS | `GOMAXPROCS=2 GOFLAGS=-p=2 make ci-check`，全量 Go、构建、Node、i18n、Chromium 和 API/SSE 均通过；日志 `/tmp/thoughtflow-capture-auto-ci-complete.log`。 |
| 自动归档增量 race | PASS | 使用下方定向命令；日志 `/tmp/thoughtflow-capture-auto-race-complete.log`。 |
| 归档浏览器交互 | PASS | 桌面和窄屏均验证：发送归档消息、自动显示成果集、无 Profile 对话框／生成按钮操作、刷新恢复、进入固定版本阅读。已纳入完整矩阵。 |
| 临时工作区实际生成 | PASS | 受控 HTTP Capture 模型 → 自动规划 → 6 个成员 Thought 落盘；澄清续跑、延期不续跑、重复操作复用和新输入竞争均有反例回归。 |

检查中发现的提示 JSON 区块边界和同步策略默认值问题均已修复，随后完整矩阵重跑通过。上述模型请求均指向测试 HTTP 服务；真实外部模型质量、消费者联合验证及远端精确 SHA CI 的边界保持第 6 节的 NOT_RUN。


```sh
GOMAXPROCS=2 go test -p 2 -race -timeout=10m -run 'TestCapture(ChatArchive|ArchiveClarification|PendingArchive)|TestAutomaticSetPlanning|TestMenuArchiveAutomatically|TestStartBinds' -count=1 ./internal/modules/application/thoughtflow/service ./internal/pkg/documentsetflow
```


### Capture 手动入口移除补充

按界面复核结果，移除 Capture 页的“文档集工具”按钮、事件绑定及手选 Profile 的生成向导，清除不再使用的按钮文案。自动归档的任务展示、审查、重试、恢复和阅读功能继续沿用原链路；Markdown 文件在 Thought 列表导入。桌面／窄屏自动归档浏览器回归增加首次加载和刷新后均无该按钮的断言，并重跑现有 Markdown 导入／完整阅读回归。


该界面增量验证：`make node-check node-test i18n-check`、`node --test --test-name-pattern='capture archive in conversation automatically|Markdown files import intact' web/app.browser.test.js`、`GOMAXPROCS=2 GOFLAGS=-p=2 make build`、`git diff --check` 均通过。浏览器覆盖桌面和窄屏，实际检查首次加载及刷新后无手动按钮、对话归档结果仍可打开，以及既有 Markdown 导入和完整阅读功能。日志分别位于 `/tmp/thoughtflow-capture-remove-tools-node.log`、`/tmp/thoughtflow-capture-remove-tools-browser.log` 和 `/tmp/thoughtflow-capture-remove-tools-build.log`。本次未重跑完整后端矩阵，前文完整矩阵记录对应按钮移除前的链路版本。

### 2026-11-03 规划响应 JSON 包裹兼容

只读核对失败会话及私有 planning 记录，确认本次 `invalid character '<' looking for beginning of value` 发生在成果集规划阶段，尚未绑定生成任务。现有记录没有保存原始模型响应，不能据此断言其一定是思考标签或 HTML 错误页。源码确认规划和套级审查直接使用严格 JSON 解码，未兼容完整的输出包裹。

规划及联合审查请求现在明确要求只返回一个 JSON 对象；统一 JSON 响应入口仅在去除一个完整的前置 `<think>…</think>` 或 `<analysis>…</analysis>` 和可选 Markdown JSON／无语言代码围栏后，剩余内容确为单一完整 JSON 对象时，才去除这些包裹。JSON 对象内的正文、契约和 Unicode 字节保持原样。严格字段、来源、范围、内容及发布校验继续执行；不会从 HTML、思考内容或嵌套示例中抽取规划，不修复截断或多份候选输出，不增加模型重试。Provider／模型的 response_format fallback 记忆继续生效。规划及联合审查错误保留具体失败阶段和底层 ProviderError。

新增 `TestJSONResponseWrappers`、`TestDocumentSetJSONWrappersPreserveStrictValidationAndFallbackCache`，覆盖格式支持／fallback 两条路径、重复调用、字段和必填内容校验、HTML、截断、多对象及思考中伪答案。`TestCaptureArchiveWithWrappedPlannerJSONFallback` 使用受控 HTTP 模型和真实临时工作区，验证完整包裹响应经自动归档生成 6 篇可读 Thought，HTML 响应拒绝且输入保留，两种场景均只发起一次格式探测和一次兼容规划。单篇审查也覆盖围栏和思考标签，不把缺失审查结果当成通过。未重放真实用户归档、未调用真实外部模型，也未修改运行中工作区的状态。

首次完整矩阵暴露通用提示前缀影响测试网关按任务识别请求，已将新增提示限定在规划和联合审查原提示之后。归档预览 API 测试的等待预算同时改用单调时钟，保留原有超时长度和冲突断言。最终 `GOMAXPROCS=2 GOFLAGS=-p=2 make ci-check` 全部通过，包含全量 Go、构建、Node、i18n、完整浏览器和 API/SSE，日志 `/tmp/thoughtflow-json-wrapper-final-ci.log`。真实模型返回和最终提交的远端 CI 不在本地验证结论内。

最终定向 race 复验通过：`GOMAXPROCS=2 CGO_LDFLAGS='-L/usr/lib/gcc/x86_64-linux-gnu/14 -L/usr/lib/gcc/x86_64-linux-gnu/13' go test -p 2 -race -count=1 -run 'Test(JSONResponseWrappers|DocumentSetJSONWrappers|DocumentReviewerUsesSeparateReadOnlyCallAndStrictIssues|CaptureArchiveWithWrappedPlannerJSONFallback)' ./internal/pkg/ai ./internal/modules/application/thoughtflow/service`；日志 `/tmp/thoughtflow-json-wrapper-final-race.log`。

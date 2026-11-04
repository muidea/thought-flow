# ThoughtFlow 工程设计生成与交付实施记录

- 日期：2026-11-03。
- 对照：`ThoughtFlow_设计生成与交付代码收口方案_v1.0.md`（正文版本 v1.1）。
- 开发基线：`a26c815947d0f38007eb4c816cf0569425f525f9`；本记录描述其后的当前工作树，尚不绑定最终提交 SHA。
- 当前范围：ThoughtFlow 本地生产端实现及确定性回归。没有修改 Workorch、调用真实外部模型、操作真实用户工作区、自动迁移旧数据或创建消费者批准/Run。
- 当前 LLM 协议与格式策略（2026-11-04）：文本生成统一使用 `/v1/responses`；归档规划、成员生成、审查及修复强制 `text.format={"type":"json_object"}`，不再文本降级；普通对话保持既有兼容策略。此用户决策取代下文历史记录中的 Chat Completions 调用及归档默认 fallback 描述，详见文末。

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

### 真实失败继续复核：补齐本地响应诊断

运行进程的构建信息已确认包含 `43e4650`，重启后的新归档仍在规划阶段收到相同解析错误。原始响应在客户端和网关均无完整记录，无法追溯其实际格式。因此，上节只证明已知包裹的兼容机制和受控回归，不能作为真实失败已经修复的证据；本节也不继续猜测标签或放宽解析、发布门禁。

规划和联合审查现在为每次调用建立独立诊断上下文，关联实际完成请求（包括 fallback 后的请求）的请求 ID、HTTP 状态、响应形态、UTF-8 字节长度、SHA-256 和解析位置。普通错误及日志仅携带这些元数据。响应形态采用固定标签，例如 `html_document`、`unclosed_reasoning_wrapper`、`reasoning_only`、`unrecognized_markup`、`markdown_fence`、`invalid_json_object`，不复制任意标签名或正文片段。

仅对已返回但未通过 JSON／响应契约校验的规划或联合审查，保存一份原始 completion 内容到 `.content-registry/private/document-sets/<set>/diagnostics/<planning|review>/<operation>-<digest>.json`，文件权限 `0600`，沿用现有私有目录保护与 HTTP 响应大小上限。不保存提示词、HTTP 凭据或成功响应。规划失败 marker 通过 `diagnostic_path` 关联文件；每份诊断包含操作身份、阶段及原始响应摘要。原始内容不进入错误序列化、普通日志、成果正文、Git、交付包、恢复备份或通用资源接口。写入失败会保留并返回原 ProviderError 和诊断写入错误，不伪报已保存；诊断既不触发模型重试，也不自动接纳失败输出。

新增／扩展受控 HTTP 与临时工作区回归：fallback 最终请求关联、原始内容和摘要核验、跨并发请求隔离、错误／日志不包含 canary、私有文件权限、资源 API 拒绝、Git 白名单拒绝、交付与备份包排除、成功响应不落盘，以及诊断目录被符号链接替换时拒绝越界写入。真实问题仍需下一次用户明确重试产生的诊断来定位；本轮未调用真实外部模型、未修改真实失败会话或重启运行进程。

诊断增量验证：`GOMAXPROCS=2 GOFLAGS=-p=2 make ci-check` 全部通过（全量 Go、构建、Node、i18n、浏览器及 API/SSE），日志 `/tmp/thoughtflow-json-diagnostics-ci.log`。AI、成果集流程、Capture HTTP 服务及 Git 白名单的相关定向 race 均通过，日志 `/tmp/thoughtflow-json-diagnostics-race.log`。本地验证证明诊断机制与隔离边界，不代表这次真实归档故障已关闭；远端最终 SHA CI 尚未核实。

### 2026-11-04 依据真实诊断收紧规划输出契约

只读核对用户本次请求对应的私有诊断，原始 completion 为 34,217 个 UTF-8 字节，摘要与报告一致。其开头为字面量 `<escape>`，移除该标记后仍不是完整 JSON：末尾停在新字段名的开启双引号处。根字段还出现输入侧 profile 元数据和 `questions`，与真正的 `SharedDesign` 契约不符。因此不能将本次失败归因为 HTML，也不能通过移除标签、补齐括号或忽略未知字段把它转换为成功规划。诊断不能证明内容在哪一层截断。

规划请求增加从实际 Go DTO 生成的完整 JSON Schema，包含精确根字段、嵌套事实、工程 typed payload、问题、来源、条件及范围变化。schema 使用共享定义，不重复展开完整类型。规划器和持久预算复用同一输入构造函数，新增 schema 字节计入原有规划调用预算。模型需使用 `open_questions`，不得在输出根复制 profile／schema／set 元数据。要求紧凑 JSON、省略不适用的可选字段，不提前生成整篇文档；冻结对话、完整 Profile、上轮事实仍全部作为输入，不以删减需求、证据、未决范围或伪造排除来缩小结果。此 schema 是规划输入，不是新 Provider 能力参数；原有严格解码、业务门禁、默认 fallback 及能力缓存继续执行。

统一响应入口仅对一个确切的 `<escape>` 前缀兼容，去除后必须是单一完整 JSON 对象；不接受任意 HTML、重复标记、多对象或缺尾部的候选。诊断检查可识别已知包裹后的未完成 JSON，返回 `reason=incomplete_json`、`response_shape=incomplete_json_object`，字节位置指向原始响应 EOF；不再只报告开头 `<`。对于返回 `stop` 但实际未完成的响应同样拒绝。未知字段归类为 `unknown_field`，普通错误不回显模型控制的任意字段名，原始字节仅在私有证据保留。私有诊断补充 completion 的 finish_reason 和输入／输出 token 统计，以便后续关联网关；不改变重试策略，不自动重放规划或补全失败结果。

新增脱敏反例覆盖完整 `<escape>`、未闭合字段、根 schema 漂移、已打包的伪答案、HTTP 200／stop 却不完整、原始字节与摘要保留、无自动重试及请求实际包含完整 schema 和所有冻结输入。真实临时工作区的 Capture 自动归档回归扩展为完整包裹与 `<escape>` 成功生成六篇、HTML 与未完成 JSON 拒绝且输入／私有证据保留。未把真实用户 completion 加入测试或普通日志，未修改真实失败会话、未重启现有服务、未调用真实模型。本地测试不证明真实模型下一次一定返回完整结果；本次已截断结果不能恢复为可信规划。

最终工作树验证：`GOMAXPROCS=2 GOFLAGS=-p=2 make ci-check` 全部通过，包含全量 Go、构建、Node、i18n、完整浏览器和 API/SSE；日志 `/tmp/thoughtflow-planner-contract-final-ci.log`。AI、成果集流程、Capture HTTP 服务及 Git 私有文件白名单的定向 race 均通过，日志 `/tmp/thoughtflow-planner-contract-race-final.log`。预算回归直接对照实际 HTTP user payload，不使用已脱敏发布计划重建原始输入。程序已重新构建，运行中进程未重启；真实模型归档恢复和最终提交的远端 CI 尚未验证。

### 2026-11-04 成对 escape 包装补充

用户新请求的私有诊断为 22,503 个 UTF-8 字节，摘要与报错一致，记录的 finish_reason 为 stop。本次不同于前一份尾部不完整的响应：它是完整的 `<escape>…</escape>` 包装，内部 JSON 完整，根字段符合 SharedDesign。上一版明确只兼容前缀、拒绝成对包装，导致此有效候选在移除前缀后仍因闭合标签不能通过 json.Valid，随后回到原始文本并报告开头 `<`。这是客户端包装识别遗漏，不能将它再次归因为模型截断。

现允许一个精确的 escape 前缀及可选的最末闭合标签，仍需内部为单一完整 JSON 对象，之后严格解码及业务门禁继续执行。任意 HTML、重复包装、多个对象、尾随解释、单独闭合标签、未知字段和缺尾部候选继续拒绝，不补全或丢弃字段。脱敏回归增加成对包装、组合围栏、闭合标签后的额外文本和成对包装内的截断；Capture 临时工作区回归增加成对包装生成六篇可读 Thought，以及成对包装不完整时拒绝并保留输入和私有证据。

使用一次性本地校验代码只读加载该真实诊断，经修复后的归一化与 `StrictJSON` 成功解码出 12 条事实、5 个问题。未保存正文到测试 fixture 或普通日志，临时校验文件已移除；此结果证明这份真实响应的解析问题得到修复，不代替其后续范围、来源、工程契约及发布校验，也未重新调用真实模型或修改失败会话。

本增量 `GOMAXPROCS=2 GOFLAGS=-p=2 make check` 通过，覆盖全量 Go、构建、Node 语法／组件和 i18n；日志 `/tmp/thoughtflow-escape-pair-check.log`。AI 及 Capture HTTP 的相关定向 race 通过，日志 `/tmp/thoughtflow-escape-pair-race.log`，包含成对包装的正常生成与不完整候选保护。程序已重新构建，当前进程未重启。本增量没有再次运行独立浏览器与 API/SSE 矩阵；上节完整 CI 记录对应成对包装补充前的工作树，不能替代真实会话后续业务验收。

### 2026-11-04 Capture 参数类型契约与旧任务提示隔离

用户提供的终端日志确认，新失败请求发生在 Capture 对话整理阶段：`document_parameters` 的值应为字符串，却返回对象，错误字段为 `document_parameters`。该阶段此前没有保存原始 completion，现有证据不能确定对象内部键值，不能宣称已离线验证这份失败输出的转换。只读会话核对显示当前输入为第 14 代，页面附带的 escape 规划错误仍属于第 12 代旧任务，且没有新的规划诊断；不能把旧报错解释为本轮已再次进入规划。

Capture 请求现在同样提供从实际 Go DTO 生成的完整 JSON Schema。默认或自定义业务提示均附加不可覆盖的 wire 类型合同，明确 `document_parameters` 为参数名到字符串的扁平映射，确认／来源元数据由后端管理。整型、布尔、字符串数组和结构化 issues 的类型一并明确。模型结果仍须通过原有解析、模板和业务校验；类型错误不会自动重试，也不会把对象强制转成字符串、丢弃元数据后更新上下文。已有用户确认的参数继续优先于模型建议。

Capture 失败现在使用独立响应 trace，保留最终 fallback 请求的 ID、状态、原始摘要及字段错误；提示明确指出参数字段并附带安全的请求 ID。仅失败响应保存到 `.content-registry/private/capture/<session-hash>/diagnostics/<generation>-<response-key>.json`，记录阶段为 `capture_context`、原始 session ID 和输入代次，权限 `0600`。目录组件使用摘要，沿用安全写入及私有资源保护。成功不留原文，原文不进入会话正文或普通日志；保存失败保留原 ProviderError 并记录写入错误，符号链接不得使响应写出工作区。该目录纳入 Git 私有文件拒绝回归。

前端仅在输入代次匹配时把失败／阻断／冲突／中断操作显示为当前归档结果，保留存储中的旧操作记录及已完成归档入口。新测试覆盖真实 HTTP fallback、对象参数拒绝、字符串参数成功、已确认参数保留、无自动重试、私有响应权限与日志隔离、符号链接拒绝及历史操作保留。桌面／窄屏真实浏览器验证当前错误可见、旧规划错误隐藏，以及刷新后状态一致。

本轮最终 `GOMAXPROCS=2 GOFLAGS=-p=2 make ci-check` 全部通过，覆盖全量 Go、构建、Node、i18n、完整浏览器和 API/SSE，日志 `/tmp/thoughtflow-capture-parameter-contract-ci.log`。AI、Capture 及 Git 白名单相关定向 race 通过，日志 `/tmp/thoughtflow-capture-parameters-race.log`；新增桌面／窄屏浏览器定向用例通过，日志 `/tmp/thoughtflow-capture-parameters-browser.log`。程序已重新构建，运行中服务未重启，未重放真实会话归档或调用真实模型；尚不能把本地受控回归视为真实模型归档已恢复，最终提交的远端 CI 也未核实。

### 2026-11-04 规划交付合同空占位兼容

用户新请求在规划阶段报告 `reason=unknown_field`。只读核对对应私有响应，23,881 个 UTF-8 字节及 SHA-256 均与用户日志一致。响应是完整的 escape 包装 JSON，唯一超出原 DTO 的字段是 `facts[].artifact.not_applicable`，值为精确的空对象 `{}`。本次不是截断、HTML 或包装解析再次失败，也不同于上一节 Capture 参数值类型错误。

交付合同解码现在只允许该已确认形态的空占位：未携带任何内容，不进入规范合同序列化，不改变既有事实语义 hash，不能豁免任何必需字段。非空声明、null、数组或标量继续拒绝；每次出现该键均独立验证，不能利用后到空值覆盖前面的非空声明。其他未知字段仍严格拒绝，失败原文继续走已有私有诊断，不静默丢弃不适用理由。规划 response_schema 明确该兼容字段只能是无属性对象；提示明确 acceptance 的不适用案例与 artifact 合同不同，交付适用性须保留在对应合同字符串或未决问题，不得虚构适用条件。

受控 HTTP 回归覆盖默认 fallback、Provider／模型能力缓存延续、完整合同往返、非空声明及其他未知空字段拒绝、失败原文保留和无自动模型重试。领域回归验证空占位不改变实际合同，缺少构建内容仍产生 `design_artifact`；Capture 临时工作区回归增加成对 escape 包装及空 artifact 占位自动生成六篇 Thought。真实私有响应通过修复后的严格解码，得到 13 条事实、6 个问题；一次性只读校验代码已删除，原文未复制进测试或普通日志。该结果仅证明这份响应的解析障碍排除，不代表其后续来源、范围、工程内容或发布门禁通过；未修改真实会话或重放真实归档。

本增量 `GOMAXPROCS=2 GOFLAGS=-p=2 make ci-check` 全部通过，覆盖全量 Go、构建、Node、i18n、完整浏览器与 API/SSE，日志 `/tmp/thoughtflow-artifact-placeholder-ci.log`。交付合同、规划 schema／fallback 及新增 Capture 自动生成场景的定向 race 均通过，日志 `/tmp/thoughtflow-artifact-placeholder-race.log`。程序已重新构建，运行中服务未重启，未调用真实模型；真实会话后续业务结果与最终提交远端 CI 尚未核实。

### 2026-11-04 规划正文语法错误与未完成尾部

新失败请求的私有响应为 34,178 个 UTF-8 字节，摘要与用户日志完全一致。去除已支持的精确 escape 前缀后，正文中 `recovery_cases` 数组缺少闭合 `]`，首个语法错误位于原文第 17,611 个 UTF-8 字节；响应结尾还停在未完成的字符串中。该内容自身不构成合法完整 JSON，不能沿用上一轮“完整 JSON 加空占位”的处理，也不能补括号、截取前几条事实或忽略剩余正文后交付。未确认在哪一层产生尾部缺失，不能仅凭字节长度断言模型或网络截断。

原归一化入口会将无效的包裹响应原样交给严格解析，因此旧诊断仅定位到开头 `<`。新增诊断专用检查，对已识别包装中的对象定位真实语法错误，换算为原始 completion 的 UTF-8 字节位置，返回 `response_shape=invalid_json_object`、`reason=invalid_json`。只改变失败归属与位置，不改变响应接纳、业务门禁或重试策略。未知 HTML、多对象、未闭合标签继续拒绝；诊断保留原始完整内容和摘要，不向普通错误输出正文。只读核验该真实响应已确认新位置为 17,611，一次性校验代码已删除。

新增脱敏回归覆盖“中间缺少数组终止符且末尾字符串未完成”、相同错误出现在被舍弃的思考示例与真正答案中、中文前缀的字节位置，以及受控 Capture HTTP 归档失败后的输入与私有证据保留、不启动成员生成、无自动重试。全量检查暴露既有参数类型测试夹具额外包含一个 `}`，已修正为合法 JSON 的对象型参数，使该测试只验证预期的字段类型错误，而不混入独立的语法错误；修正后连续 30 次通过。

另对本机 AetherRelay `37ecbf6` 做只读源码核对：`internal/modules/application/proxyapi/service/proxy/chatgpt_web.go` 的非流式适配器固定返回 `finish_reason=stop`；`internal/modules/blocks/chatgptwebupstream/internal/client/text.go` 使用 16 MiB `io.LimitReader`，而 `finishTextResult` 未以 `Done` 为成功前置条件。通过临时 Go overlay 调用实际 `ParseTextSSE`，在合成输入中分别复现普通 EOF 和触达读取上限后的 EOF 均返回 `error=nil, Done=false` 和部分文本；日志 `/tmp/thoughtflow-relay-sse-eof-diagnostic.log`。没有修改网关仓库、调用真实上游或确认远端部署版本，故不能将这个已复现的本机缺陷直接认定为本次线上响应缺尾的根因，也不能用它解释或豁免正文中间独立的语法错误。

该上游边界仍待网关收口：明确区分收到终止标记、正常 EOF、读取预算耗尽与传输中断；未确认完成时返回明确错误并标注生成已发出，不把部分正文与合成 `stop` 当作完整成功，也不据此自动重试。预算触达必须可观察，完整多快照输出须在批准的预算内通过，否则明确拒绝。需要进一步提高大规划成功率时，应另行设计保留完整冻结输入、跨片范围核对及统一调用预算的分阶段规划，不能以删除需求或接纳无效 JSON 替代。当前客户端诊断修复不等于真实归档已恢复。

最终 `GOMAXPROCS=2 GOFLAGS=-p=2 make check` 通过，覆盖全量 Go、构建、Node 语法／组件和 i18n，日志 `/tmp/thoughtflow-invalid-planner-syntax-final-check.log`。AI、Capture HTTP 及参数类型保护的相关定向 race 通过，日志 `/tmp/thoughtflow-invalid-planner-syntax-final-race.log`。本增量未重复浏览器与 API/SSE 全矩阵，上节完整 CI 对应本增量之前的工作树。程序已重新构建，现有服务未重启，未重放真实归档或提交修改。

## 2026-11-04 归档强制 JSON object

根据用户最新决策，实际归档链路强制发送 `response_format: {"type":"json_object"}`，无配置开关。此规则覆盖 Capture 单篇归档、多文件成果集规划、成员生成、联合审查及有界内容修复，也覆盖有类型文档的 Compose 生成、单篇审查和修复。纯本地渲染无需模型请求；普通 Capture 对话整理和其他既有文本调用保留原策略。

强制策略通过操作 context 传递，后台任务每次 execute 都重新设定，包含显式重试任务；不依赖发起 HTTP 请求的 context 存活。强制调用直接请求原生 JSON object，绕过普通对话中已记忆的 response_format 不支持状态，不等待或改写该能力缓存。同一个 Provider 实例中的普通对话与归档并发时，各自保留自己的策略。

Provider 不支持 JSON object 时，保留 `conversion_unsupported`、feature 和请求 ID，单次失败，不移除参数、不自动重新生成、不转本地生成、不自动切换 Provider/模型。普通单篇语义审查失败仍按既有规则保存 error 报告，不能把能力错误算作通过。恢复已有落盘事务无需新增模型请求；明确的新尝试仍需支持该能力的 Provider。

JSON object 是输出格式约束，不是内容质量、字段合同或来源有效性的保证。现有严格解码、字段类型、来源冻结、范围、审查、发布和交付门禁继续执行；无效输出仍拒绝，不补写括号、不删字段来凑出可接受结果。格式请求成功后的校验错误使用 `invalid_json_response`，不会误标为文本 fallback 错误。

新增回归使用本地受控 HTTP 和临时工作区：同 Provider 的普通对话已 fallback 后，归档规划、六成员生成和联合审查仍全部携带 JSON object 并提交；不支持、非法 JSON、非法字段类型均无文本重放；Capture 输入与私有诊断保留；单篇生成、审查、修复的失败不发布内容；并发策略隔离及取消仍生效。主要入口为各包 `required_json_test.go` 与 `TestCaptureArchiveRequiresJSONAndValidatesPlannerOutput`。没有调用真实模型、重放用户归档、修改真实配置或重启服务。使用时需加载新二进制，并选择明确支持 JSON object 的模型与 Provider；仅更新网关并不自动证明对应后端已具备该能力。

本增量最终 `GOMAXPROCS=2 GOFLAGS=-p=2 make ci-check` 全部通过，含全量 Go、构建、Node、i18n、真实浏览器及临时服务 API/SSE；日志 `/tmp/thoughtflow-required-json-final-ci.log`。AI、成果集流程、单篇文档流程、Capture 业务和 Capture HTTP 新增回归的定向 race 全部通过，日志 `/tmp/thoughtflow-required-json-race.log`。全量首轮发现旧自动归档夹具仍要求文本降级，已改为原生 JSON 请求上首次返回 502、显式重试成功的场景，继续验证未知生成不得自动重放及只写入一篇 Thought；定向及 race 通过，日志 `/tmp/thoughtflow-required-json-explicit-retry.log`、`/tmp/thoughtflow-required-json-explicit-retry-race.log`。最终源码空白检查通过。以上是当前工作树的本地证据，不代表真实模型质量、线上归档或最终提交远端 CI 已验证；本轮尚未提交修改。

### 归档能力拒绝的提示与日志区分

用户随后提供的 `14:39:37` 会话请求日志包含普通上下文整理的自动 fallback；`14:40:20` 规划请求则以 `conversion_unsupported/response_format` 单次失败，两者不是同一请求。只读核对 AetherRelay `ValidateConversionRequest` 与 `conversionAPIError` 确认，所示 `conversion does not support ... native endpoint contract` 文案来自转换能力校验，在上游请求前拒绝；目前证据不足以确定实际 Provider。原生 JSON 能力需由网关路由及上游实际支持，不能只按模型名判定，也不能以网关的基础文本转换补足。

归档状态气泡对 response_format 能力拒绝改为提示更换支持 JSON object 的模型与 Provider 后重新归档，保留错误详情及请求 ID，不再套用“再次发送归档重试”的通用提示。其他失败类型保留原有提示。LLM 请求日志新增 `json_required`，普通 fallback 警告补上 scope_id 和 `json_required=false`；强制归档拒绝记录 `json_required=true/json_format=true`，不改变调用或降级策略。

该反馈增量 AI 包全量测试及相关定向 race 通过，Node 154/154、i18n 6/6、JavaScript 语法检查通过，新增桌面／窄屏真实浏览器测试验证错误提示及刷新后保留输入和请求 ID。日志 `/tmp/thoughtflow-json-capability-message-ai-full.log`、`/tmp/thoughtflow-json-capability-message-go.log`、`/tmp/thoughtflow-json-capability-message-node.log`、`/tmp/thoughtflow-json-capability-message-browser.log`。定向浏览器命令只运行新增场景，其余 26 个顶层测试被筛选跳过；上一节完整 CI 对应本反馈增量之前的工作树，本次未重复全矩阵。未修改网关、真实配置或真实会话，也没有重放真实归档。

## 2026-11-04 LLM 统一迁移至 Responses

根据用户最新统一要求，所有远端 LLM 文本生成改为 `POST /v1/responses`，覆盖 Capture 分类、对话上下文、Refine、Weave、Synthesize、Expand、单篇归档与 Compose、成果集规划与成员生成、单篇及联合审查和修复。生产代码只有统一的 `client.Responses.New` 生成入口，不再请求 Chat Completions，也不因 400/404/协议失败自动改回旧端点。embedding 继续使用独立的 `/v1/embeddings`，本地无模型实现不受影响。沿用 `llm.base_url`、`api_key`、`chat_model`、超时字段；base_url 仍填写服务根或 `/v1` 基地址，不填写完整 `/v1/responses`。没有新增端点／降级配置开关，没有自动更换真实模型及 Provider。

按照 [OpenAI Docs：迁移至 Responses](https://developers.openai.com/api/docs/guides/migrate-to-responses/)，请求将原 system/user 文本及角色完整放入 `input`，JSON object 通过 `text.format={"type":"json_object"}` 声明，分类输出上限映射为 `max_output_tokens`，不发送旧的 `messages/response_format/max_tokens`。使用 `store=false`，不创建远端 Response 存储／续接依赖；每次仍提交应用管理的完整上下文。这个字段必须由实际 Provider 路径支持，不能为通过文本转换而静默删除。

归档的强制 JSON 策略继续生效；普通对话的有界 fallback 只在明确不支持 JSON 格式时移除 `text.format`，后续调用延续同端点、凭据及模型下的能力记忆，仍请求 Responses。支持 `text.format` 及其子能力的准确诊断，同时保留既有网关 `response_format` 错误别名；混合缺失能力、`text` 整体不支持、store 或其他参数失败均不触发格式降级。能力缓存身份显式包含 Responses 路径，未复用旧协议的判定。归档提示也识别 `text.format` 能力拒绝。

响应严格要求原生 `object=response`、`status=completed`；按顺序拼接所有已完成 assistant message 的 output_text，不把 reasoning 摘要当正文。`incomplete`、max_output_tokens 截断、failed、queued/in_progress/cancelled、缺失状态、错误对象、拒答、非预期 tool call／内容、未完成 message、空正文、旧 Chat envelope 均拒绝，不接纳部分正文、不轮询补齐、不自动重新生成。调用仍沿用同一截止时间与取消、SDK 零重试和请求 ID。私有 JSON 校验诊断记录实际 response_status、input_tokens/output_tokens 映射后的用量，不合成旧 finish_reason=stop。

已将受控 Go 与 API/SSE 测试网关改为真实 Responses 请求／响应合同，而非在生产代码中兼容两种 envelope。新增测试核对 URL、input 角色及中文原文、JSON 格式嵌套、store=false、token cap、reasoning 与分块正文、用量、所有终态拒绝，以及格式／协议失败不重放、不切端点。现有来源、范围、不可变版本和发布校验保持执行。当前网关若只能提供基础文本转换或不支持 JSON object，仍会明确拒绝归档；本轮未调用真实上游，不能把协议迁移等同于已验证线上归档成功。

本次 Responses 迁移最终 `GOMAXPROCS=2 GOFLAGS=-p=2 make ci-check` 全部通过，覆盖全量 Go、构建、Node/i18n、完整真实浏览器、API/SSE；日志 `/tmp/thoughtflow-responses-final-ci.log`。AI 全包 race 通过，日志 `/tmp/thoughtflow-responses-ai-race.log`；Capture、成果集、单篇文档与应用 HTTP 的归档／强制 JSON／显式重试定向 race 通过，日志 `/tmp/thoughtflow-responses-flow-race.log`。独立相关包检查日志 `/tmp/thoughtflow-responses-packages.log`；Node/i18n 日志 `/tmp/thoughtflow-responses-node.log`。新二进制已构建，真实运行服务未重启，真实模型和网关配置未改动，未重放真实会话归档，尚未提交修改；远端 CI 未核实。

### 2026-11-04 Responses 返回格式与 Content-Type 收口

用户提供的 `evt-20261104-152917-23aeca2e` 日志表明，HTTP 200 响应声明 `text/event-stream`，此前 SDK 按 JSON 结构解码时拒绝了该 Content-Type。这条日志本身不能证明正文是 SSE。只读检查 AetherRelay 的 `writeCodexOAuthCompleteSuccess` 与 `copyCodexHeaders`，确认非流式成功分支会写入汇总后的 JSON 字节，同时保留上游的 Content-Type，只有原头为空才设为 application/json，因此存在“JSON 正文被标为 SSE”的路径。本轮没有该线上响应的原始正文，不能将此源码路径直接认定为该请求的唯一根因；网关应使成功响应头与实际返回格式一致。

ThoughtFlow 仍只请求 `/v1/responses`，统一显式声明 `stream=false`、`Accept: application/json`，保留 `store=false`、JSON object 策略和 SDK 零重试。通过 SDK 的 raw response 接口读取同一次 HTTP 响应，按实际字节识别原生 JSON envelope 或 Responses SSE，不因 Content-Type 错误重新发送模型请求。日志增加实际解码格式 response_wire 及规范化 response_content_type，保留请求 ID、HTTP 状态和用量；不输出正文。

SSE 必须含有完整的 response.completed 快照并正常结束；核对响应身份以及已观测 output_text delta/done 与最终快照的文本和成员位置。只有通过原有 completed assistant text 检查的结果才交给严格 JSON／业务校验。缺少完成快照、记录截断、失败／错误事件、重复完成、完成后继续输出、身份或文本矛盾、拒答及非法字段均拒绝；[DONE] 不能替代完成快照。解析失败不自动重放、不自动切换端点，不把流中的部分文本用于更新或发布。既有 4 MiB 响应预算继续执行，底层限流读取不再把超限伪装成正常 EOF；读取结束或失败均关闭响应体，沿用原请求截止时间与取消。

新增受控测试覆盖误标 SSE 的 JSON、真实 SSE（完整快照、分片、CRLF、BOM）、元数据、协议失败和无重放、超限边界、取消／超时，以及完成后仍有非法业务 JSON。Capture 使用真实 provider 与临时磁盘会话，验证有效回答持久化，以及后续 SSE 失败保留原需求、新输入和按既有策略保留的文档参数；新增输入时，旧的派生候选正文仍按原规则失效，不把它当作新输入已完成整合的结果。六成员成果集规划、生成、审查和提交增加三种返回格式的集成回归，保留归档强制 JSON、不继承普通对话 fallback 的门禁。仅使用本地受控模型服务；未改 AetherRelay、真实配置、真实会话，也未重启线上服务或重放归档。

本增量验证：`GOMAXPROCS=2 GOFLAGS=-p=2 make ci-check` 中全量 Go、构建、Node、i18n 和完整真实浏览器均通过；API/SSE 阶段的“专题刷新后精确搜索成员”首次等待失败（无模型配置场景），其余端到端场景通过，包括六成员归档与 SSE。未修改代码或放宽断言，独立重跑 `make e2e-test` 为 41/41 通过，该专题场景约 3 秒完成。首次失败原因未确定，不将其直接归因于 LLM 协议修改，也不将这一轮完整矩阵记为一次性全绿；日志 `/tmp/thoughtflow-responses-wire-ci.log`、`/tmp/thoughtflow-responses-wire-e2e-recheck.log`。AI 全包 race 通过，日志 `/tmp/thoughtflow-responses-wire-ai-race.log`；三种返回格式的六成员流程与 Capture 持久化定向 race 通过，日志 `/tmp/thoughtflow-responses-wire-flow-race.log`。新二进制已构建；真实服务未重启，真实模型归档未验证，尚未提交，远端 CI 未核实。

### 2026-11-04 成果集规划的用户证据下标收口

用户 `evt-20261104-160921-b70d5b05` 日志已确认 native JSON 请求成功，声明 text/event-stream 的 JSON 正文被正确解码；随后 `REQ-001 lacks user evidence` 是业务来源门禁拒绝，不是 JSON 格式能力失败。当前日志未包含该次规划的原始 source.message/quote，不能断定其具体原因。源码确认一个可确定的合同歧义：规划提示原写 zero-based user message index，但 FreezeShared/UserEvidence 实际按完整 capture.messages 下标取证，数组包含 AI 回答。用户第 2 轮常位于完整数组下标 2，模型若填写用户轮次 1，会引用 AI 回答而被拒绝。

规划请求现明确 source.message 为完整冻结数组的零基绝对下标，新增 source_contract 列出 user_message_indices，禁止按用户轮次／一基计数；所有原始对话、profile、历史事实和 DTO schema 保持完整，新增索引指导也计入原有输入预算，不复制整段用户原文。所有引文必须从对应原始用户消息逐字连续摘取；摘要、AI 回答和改写内容不能替代证据。

只在模型适配器解析完成后，对“下标无效，但引文逐字存在于唯一一条用户消息”做确定性下标校正。保持引文、事实内容、确认状态和历史快照不变，不作模糊匹配，不补写缺失引文，不自动授予确认，不重复请求模型。原下标本来有效则保留；无效下标且引文在多条用户消息中出现则不猜测。涵盖当前事实、条件、范围、排除、适用性、范围变更及问题解决来源；历史快照来源不按当前对话重写。显式 API 输入和 FreezeShared 本身仍严格按原下标核验，不自动校正；已确认事实变更仍由现有新增用户证据门禁检查。

来源失败细分 quote_empty/message_out_of_range/not_user_message/quote_not_found，保留事实 ID、原始下标、request_id、HTTP 状态及不可自动重试属性。该错误仍包装 ErrInvalid，HTTP 校验状态保持 422，不伪装成 JSON 语法错误。模型适配器提前检查 confirmed 事实来源，原始输出通过既有私有诊断机制保留；日志和公开错误仅含稳定字段及原因，不泄露引文和完整输出，不发布失败计划或成员。该次旧错误没有被这项新诊断追溯补造。

回归覆盖交错用户／AI 消息、错误用户序号、一基／越界／错误用户下标的精确校正、有效重复引文原下标保留、歧义／仅 AI／改写／空引文拒绝、当前嵌套来源与历史隔离、显式 API 来源不校正、业务错误及私有诊断链。三种 JSON/SSE 返回格式的六成员规划、生成、审查和提交均使用用户轮次与绝对下标不同的对话构造，检查私有持久计划的来源，并保持原生 JSON 及无文本重放门禁；公开计划仍按原规则去除私有引文。

本增量 `GOMAXPROCS=2 GOFLAGS=-p=2 make check` 全部通过（全量 Go、构建、Node/i18n），日志 `/tmp/thoughtflow-planner-evidence-check.log`；新二进制上的 API/SSE 端到端 41/41 首轮通过，含六成员生成、交付、备份、重启与专题刷新搜索，日志 `/tmp/thoughtflow-planner-evidence-e2e.log`。documentset、AI 及成果集流程的来源／私有诊断／六成员规划定向 race 通过，日志 `/tmp/thoughtflow-planner-evidence-race.log`。单元与定向流程日志 `/tmp/thoughtflow-planner-evidence-unit.log`、`/tmp/thoughtflow-planner-evidence-flow.log`。本增量没有前端修改，未重复上一返回格式增量已通过的完整浏览器套件，不将旧结果写成此次重新执行；当前工作树通过空白检查。没有调用真实模型、重放真实归档、修改真实会话／配置或重启真实服务；尚未提交，远端 CI 未核实。新二进制需重启现有服务后生效；实际失败引文若确为空、被改写或只来自 AI，仍会拒绝，并由新的私有诊断与原因码提供核对依据。

### 2026-11-04 工程规划关系合同收口

用户随后报告 `invalid document set contract: invalid typed relation REL-003`。这是工程 profile 的关系类型／方向／端点类型门禁，不能仅凭关系 ID 判断实际错误；本轮没有该次模型输出的 kind/from/to，不将某一关系方向错误当作已取得的线上证据。

源码确认此前 Relation DTO schema 仅描述字符串，实际类型及方向由提示词和 Go switch 分别维护，EngineeringSchema 也没有列出机器可读关系注册表。现将原有允许规则提取为单一 EngineeringRelationRules：realizes（module/decision → requirement）、constrains（constraint → interface/module/artifact_spec）、uses_contract（module/decision → interface/entity）、verifies（verification → requirement/constraint/interface/artifact_spec/module）、depends_on（既有事实 → 前置事实，禁止自身及前置环）、supersedes（既有事实 → 固定历史引用）。没有放宽端点集合或循环策略，没有新增别名；普通研究／非工程 profile 不启用这些规则。

DocumentSetPlanningInput 将同一规则表放入 relation_contract，并据此为工程 response_schema 的 Relation.kind 生成枚举；请求预算仍按完整实际 payload 计数。提示词说明 from/to 必须为事实 ID、方向有意义；验证事实指向被验证事实，设计职责指向被实现需求；接口字段到实体字段的映射保留在 fields.entity_id/entity_field，不能错误表示为接口 uses_contract 实体。不能为了过门禁将不支持的关系随意改成 depends_on、反转端点或删除覆盖；无法表达的设计关系保留事实，并提出阻断的类型化问题。

工程规划在模型返回后立即调用与 BuildPlan 相同的确定性校验。失败细分 unknown_kind、endpoint_kind_mismatch、missing_from/missing_to、self_dependency、invalid_historical_target、duplicate_relation_id、invalid_relation_id、cyclic_prerequisite，错误描述关系 ID、kind、from/to 与其事实类型，保留 request_id、HTTP 状态和 ErrInvalid，业务 HTTP 状态仍为 422。不可作为机器标识的任意文本不进入公开错误，原始完整 JSON 通过现有私有诊断保存；不发布失败计划、不生成成员、不自动重放模型请求。历史引用的完整固定身份和 hash 仍由原历史解析链核验，前置类型检查不代替它。

新增规则正反向、端点缺失、未知类型、依赖自环／环、重复 ID、历史替代及规则表可变性隔离回归；模型请求实际核对 registry 和 enum，原生 JSON/SSE 的错误关系保留完整私有结果但公开错误不泄露正文。工程六成员集成回归验证合法规划生成、联合审查并提交；非法 REL-003 无新计划、无生成调用、无发布指针变化且保存私有诊断。没有擅自改写、删除或补造关系来通过校验。

本增量最终 `GOMAXPROCS=2 GOFLAGS=-p=2 make check` 全部通过，覆盖全量 Go、构建、Node 154/154、i18n 6/6，日志 `/tmp/thoughtflow-planner-relations-check.log`。新二进制的 API/SSE 端到端 41/41 通过，含六成员生成、交付、备份恢复、重启、专题刷新及真实事件流，日志 `/tmp/thoughtflow-planner-relations-e2e.log`。documentset、AI、成果集流程的 Relation/Evidence/私有诊断定向 race 通过，日志 `/tmp/thoughtflow-planner-relations-race.log`；相关包全量初步检查日志 `/tmp/thoughtflow-planner-relations-focused.log`。重命名统一入口时编译检查发现并修正历史计划验证和公开读取的两处旧调用，最终全量检查及上述 race、端到端均在修正后通过。没有前端变更，未重复早前已执行的完整浏览器套件，不将旧结果记为此次重跑；源码空白检查通过。

以上为当前工作树的本地受控证据，不是原始 REL-003 的现场重放或真实模型质量证明。本次未调用真实模型、未改网关／配置／真实会话、未重启运行中的服务，未擅自改写关系，尚未提交或核实远端 CI。已构建的新二进制需重启现有服务后生效；若模型仍输出不合法关系，新错误会直接给出关系类型、端点身份／事实类型、原因码及 request_id，并保存私有原始结果以便准确核对。

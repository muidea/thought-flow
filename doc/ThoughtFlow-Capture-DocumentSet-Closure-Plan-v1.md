# ThoughtFlow Capture 多文件成果集生成收口方案

版本：1.0\
日期：2026-10-27\
源码基线：`123bc1ed7762eb1e5b4d379d56dbcb8f5ddcefe9`\
文档性质：待实施设计与验收规范，不是已实现或已验收声明。

## 1 目标与结论

本次收口的交付单位是**一个设计任务的一套关联文档**，不是若干独立 Thought 加入同一个目录。

用户在 Capture 中讨论功能目标、整体架构、模块、接口、数据存储等内容后，系统应根据实际 profile 和确认的任务范围，自动规划、生成、检查并保存多个文件。各文件从同一组设计事实展开，职责互补、引用可达、契约一致，整体覆盖用户需求。用户无需逐篇建计划项、复制正文、加入专题或迁移文件。

软件设计是首个参考场景，成果集模型不应写死为软件工程专用平台。某个 profile 可以生成一篇，另一个生成多篇；是否拆分模块、是否需要存储或部署文档，由 profile 的声明和已确认范围决定。

**首期必须完成跨文件完整性与一致性校验、统一版本选择和失败恢复。**旧专题工作区方案将这些能力放在第二阶段的范围划分，不再适用于本次目标。旧方案中的存储、检索、独立版本保护继续作为底座，不推翻其已完成的专项修复。

“确保一致性”在工程上指明确的检查规则、覆盖证据、版本约束与阻断机制，不承诺模型能够证明任意自然语言设计绝对正确。不能把无异常、空问题列表或 ZIP 导出成功当成整套设计通过。

## 2 当前实现与新增范围

| 范围 | 基线事实 | 本次处理 |
| --- | --- | --- |
| Capture | 一个 profile → 一个 DocumentDraft → 一份 preview.Body → 一个 Thought | 增加成果集分支及持久化生成任务，保留单篇路径 |
| Profile | 一组 inputs、sections、单个 template | 新增文档集 profile，组合多个单文档角色契约 |
| 专题工作区 | collection、分组、主归属、独立修订、定稿选择、快照、检索、导出 | 复用物理布局；增加 set-aware 读取、固定成员与版本解析 |
| 校验 | 单文档结构、来源、部分需求／约束／术语检查 | 显式填充契约，新增整套覆盖与一致性检查 |
| 来源追踪 | 冻结来源、hash、changed/unavailable | 复用，并增加语义依赖与受影响文档待复核 |
| 编辑与链接 | 普通导入文档误入 typed 编辑；thought/topic 链接被过滤 | 本期修复，接入成果集解析与转换入口 |
| 自动归档 | 单 Thought 的预留、CAS、恢复、事件 | 不得简单循环调用视为整套原子；增加套级提交协议 |

本方案新增的数据类型、目录、接口和状态均为**建议实施契约**。不能把本文 API 示例当作当前已有接口。

本期不增加多租户权限、分布式多写者、任意执行插件、自动远端 Git 推送或软件实现功能。文档 ready 不等于软件已开发、测试或上线。

## 3 用户可见的完成流程

1. Capture 讨论并提取目标、约束、关键事实及未决问题。
2. 选择或匹配一个文档集 profile；存在决定性歧义时只询问缺失信息。
3. 自动形成文档计划与共享设计，用户可以展开预览；不存在歧义时不强迫逐文件确认。
4. 用户一次点击“生成整套”，或由已明确启用的自动归档策略触发。系统完成所有成员生成、单篇检查和联合检查。
5. 自动保存草稿成果集及专题目录，显示完成度、阻断问题、审查证据与版本。失败成员可以重试，但半套不能冒充完成成果。
6. 满足发布门禁后，在原授权范围内确认整套定稿；生成草稿的授权不等同自动公开分享或外发。
7. 后续继续对话修改设计，系统评估影响并产生新候选套，旧完整版本保持可读。

Capture 中“草稿已保存”“生成已结束”“整套检查通过”“整套已定稿”必须分别显示，不能用一个 success 混合表达。

## 4 核心对象与唯一权威

| 对象 | 职责与关键字段 |
| --- | --- |
| DocumentSetProfile | profile_id、version、content_hash、inputs、roles、output_rules、set_checks、limits |
| CaptureSnapshot | conversation generation、确认事实、未决问题、来源快照、scope_hash、context_hash |
| SharedDesignRevision | 需求、术语、模块、接口、数据实体、决策、约束、关系、来源；稳定 ID 与不可变 hash |
| DocumentSetPlan | plan_id/hash、profile 快照、共享设计版本、成员稳定 key、覆盖责任、生成 DAG、文件名建议 |
| GenerationAttempt | operation_id、request_hash、base token、任务状态、逐成员结果、调用账本、检查结果 |
| MemberArtifact | member_key、document_id、revision_id、body_hash、role/profile hash、依赖版本、人工修改基线 |
| SetValidationReport | 冻结输入清单 hash、规则版本、结构结果、语义覆盖与证据、风险确认、状态 |
| SetRevision | set_revision_id、父版本、共享设计、计划、固定成员清单、报告与资源可达闭包 |
| SetFinalization | 精确 set_revision/hash、报告、交付级别、批准事实、时间、操作 ID |
| SetHead | 单一原子指针记录：generation、当前完整草稿套、当前定稿套、operation_id |

共享设计是**确认事实与结构化契约**的权威；成员正文是对应版本的可读解释。目录是成员清单的投影，不得反向静默改变权威事实。每个成员仍有稳定文档 ID，可复用现有 revision 存储，但成果集阅读不得再按每篇 latest HEAD 临时拼接。

一个专题可保留普通参考条目；首期一个受管成果集对应一个 collection，明确区分受管成员与外部参考。不能把未纳入计划的普通条目自动计入成果集完成度。

## 5 文档集 Profile 契约

建议增加独立 `DocumentSetProfile` 类型，复用现有注册、版本及内容 hash 机制，不原地改变已有单篇 profile 的解释。单篇 profile 可以通过适配器形成单成员成果集，但历史记录不自动转换。

示意配置如下。角色 profile 名称为本方案拟新增内置项，不表示基线已存在。

```yaml
schema_version: 1
kind: document_set
profile_id: builtin.feature-design-set
version: 1
inputs:
  - {key: goal, required: true}
  - {key: scope, required: true}
  - {key: constraints, required: true}
roles:
  - key: architecture
    cardinality: one
    required: true
    document_profile: builtin.architecture-design-v1
  - key: module
    cardinality: per_confirmed_module
    required: true
    document_profile: builtin.module-design-v1
  - key: interfaces
    cardinality: one
    required: true
    when: has_interfaces
    document_profile: builtin.interface-contract-v1
  - key: storage
    cardinality: one
    required: true
    when: has_persistence
    document_profile: builtin.storage-design-v1
  - key: verification
    cardinality: one
    required: true
    document_profile: builtin.design-verification-v1
set_checks:
  - required_scope_coverage
  - unique_contract_owner
  - interface_entity_compatibility
  - resolved_member_links
  - cross_document_semantic_review
limits:
  max_members: 20
  max_repair_rounds: 2
  max_parallel_generations: 3
```

配置中限值仅为首版建议默认值，部署可调整且须受工作区总预算约束。`when` 只能引用服务端白名单中的已确认布尔事实；不能执行表达式、模板代码、shell 或模型返回的任意逻辑。每个角色必须显式声明 required，不采用隐式默认值；when=true 时执行其 required 约束，unknown 时进入澄清，只有 false 时才可按该条件省略。缺少事实时进入澄清，不把 unknown 当 false 来省略文档。

示例中的 `document_profile` 字符串为角色契约简称；注册发布时必须解析并保存完整的 profile ID、version 和 content_hash，未解析或有歧义时拒绝发布。实际保存的 profile 引用必须固定精确版本和内容 hash。角色名、文件名和展示标题不同：成员身份使用稳定 key，例如 `module:MOD-ORDER`；模块重命名不创建新身份。数量膨胀超过限额时显示拆分建议，不静默截断。

## 6 共享设计事实与事实归属

共享设计包含至少以下结构：

- `requirements`：稳定需求 ID、范围、优先级、验收意图、来源与确认状态。
- `terms`：术语 ID、名称、含义、别名；避免同词异义。
- `modules`：模块 ID、职责、边界、拥有的数据与提供／消费的接口。
- `interfaces`：接口 ID、操作、参数、类型、可空、错误、权限、幂等、兼容语义及 owner。
- `entities`：实体与字段 ID、类型、键、约束、生命周期、事务和迁移规则。
- `decisions`：决策 ID、选项、理由、约束及影响对象。
- `relations`：需求实现／验证、接口消费／提供、存储映射、依赖关系。
- `open_questions`：问题、阻断程度、受影响成员、提出依据。

每个事实记录来源位置、状态（用户确认／模型建议／未决）、内容 hash 和修改原因。模型建议不能自动冒充用户确认；用户确认的事实不能被生成或修复阶段静默推翻。

对接口和字段建立唯一权威 owner。允许在多个文件出现，但重复出现的结构化内容由同一契约确定性渲染；其他正文只能解释，不另立一套字段定义。未持久化字段、计算字段、外部数据需要显式分类，不能机械要求每个 API 字段都必须存在数据库列。

## 7 规划与完整性覆盖

规划输出须列出每个成员的职责、包含与不包含内容、所覆盖需求／模块／契约、依赖、role profile、文件建议和必要性。建立“范围要求 → 权威设计事实 → 成员章节 → 验证项”的追踪关系。

角色是否必需由 profile 和确认范围计算；用户排除某项须记录理由和范围修订，不能通过删除计划项把完成率做成 100%。每个必需事实至少有一个明确的交付位置，每个契约恰有一个定义 owner；参考出现次数不算重复定义。

软件模块依赖图允许现实中的环。**生成调度图必须无环**，不能为了调度方便禁止所有循环系统。共同接口先在共享设计阶段确定；必要时把强连通模块作为联合规划组，再分别展开文档。无法稳定解开的设计冲突回到澄清，不能随意改变系统架构。

计划 hash 包含输出集合、角色规则、共享设计版本及精确 profile 快照。生成开始后新增模块或改需求形成新计划，不向正在执行的任务追加无版本约束的成员。

## 8 生成与确定性渲染

生成采用“规划共享设计 → 生成成员解释 → 确定性装配 → 单篇检查 → 联合检查”的流程，可多次模型调用，也可在限额内并行；不是要求一次调用输出整个目录树。

每个调用只获得完成该成员所需的冻结上下文，但必须包含适用的共同约束、术语、契约以及上游固定摘要／版本。来源正文作为不可信资料，不具有更改系统规则、写入路径或授权外发的权限。

模型返回结构化章节、事实引用、覆盖声明、假设和冲突建议，不返回任意服务器路径。服务端验证 schema、成员身份、大小和引用，再生成正文。接口表、字段表、稳定链接及追踪表优先由结构化事实确定性渲染，减少自然语言重抄引入的分歧。

不能只检查模型附带的 sidecar：最终落盘正文必须重新解析并核对关键字段、标题锚点、引用、契约表及覆盖证据。对于无法确定性提取的叙述，纳入明确的联合语义审查，不把 sidecar 声称一致当作正文一致的证据。

## 9 单篇与整套校验门禁

校验分三层，顺序与责任固定：

1. **格式与单篇门禁**：成员 profile 结构、必填输入、修订 hash、资源、引用、必需章节；显式把 SharedDesign 中适用的 Terms／Constraints／Requirements 填入 DocumentContract，不能假设当前 Capture Candidate 已填。
2. **整套确定性门禁**：计划成员齐全、稳定身份唯一、覆盖关系完整、owner 唯一、接口与实体兼容、无非法引用、依赖版本精确、最终正文与权威契约一致、无未处理人工编辑冲突。
3. **整套语义审阅**：跨文档职责是否重叠／遗漏，流程是否前后矛盾，失败处理、权限、事务、时序与约束是否连贯；审阅有明确范围、章节位置和证据。

联合审阅应按关系和需求分片，但必须建立覆盖台账，证明必审对象和关系全部被检查。上下文太长时分批检查，不能截断后仍标 passed。无输出、解析失败、超时、模型不可用、缺少覆盖分别为 error/not_run/incomplete，不能当通过。

硬错误不可人工豁免，包括成员缺失、契约类型冲突、覆盖缺失、身份或版本错误、越权资源、损坏内容、CAS 冲突。语义疑点允许按策略逐项确认，须记录证据、理由、适用版本和批准者；不可照搬“所有模型 issue 都可 HumanConfirmation”的规则。改变事实解决问题必须重新生成受影响内容和报告。

最终 ready 门禁要求：必需成员完成、所有硬规则通过、所有规定语义覆盖完成、阻断疑点关闭、非阻断风险有记录。若外部语义模型不可用，首期可支持**完整人工审阅替代**，但必须逐项覆盖同一检查表，结果显示 `manual_reviewed`，不得伪装成模型通过或绕过硬规则。

## 10 报告与版本绑定

为避免报告 hash 与包含报告的 manifest 自循环，分开两个摘要：

- `candidate_manifest_hash`：计划、共享事实、全部成员 revision/body/profile/资源/来源 hash、依赖和验证策略版本；不含报告自身。
- `set_revision_hash`：candidate manifest 加校验报告 hash、风险处置与其他发布元数据。

SetValidationReport 必须绑定 candidate manifest hash 和检查器版本。任何成员、共享事实、来源契约、profile 或策略改变，旧报告对新候选套失效。即使复用未修改的旧 revision，也必须作为新候选套的一部分重新参与联合检查。

单文档定稿不能替代整套检查；整套通过也不能掩盖其中一个成员单篇硬规则未通过。套级定稿记录固定 set revision 和报告，旧定稿不因新草稿或新模型版本自动改写。

## 11 状态与运行控制

GenerationAttempt 主状态：`queued → planning → generating → checking → awaiting_review → ready → committing → committed`。按情况进入 `needs_clarification`、`failed_retryable`、`failed_terminal`、`stale`、`cancelled`。这些是建议新增值，不是当前已有枚举。

生成任务的 committed 只表示完整候选草稿套已经提交，不表示正式定稿：只 CAS 更新 SetHead.working_set_revision，保留 finalized_set_revision。正式定稿是独立操作，通过目标交付级别门禁并核对前置版本后，才更新 finalized_set_revision。不完整或检查阻断的 attempt 只保存于私有预览，不推进这两个指针。

每个成员另记 queued/running/generated/checked/failed/stale 与 attempt ID；任务进度不是成果完成度。通过检查但尚未发布的候选套可在私有预览中查看，不能冒充当前定稿。

同一次 attempt 冻结 Capture generation、上下文与范围、profile/plan/shared-design hash、目标 set/catalog generation、成员编辑基线和模型配置。外部调用不得持有 namespace 或文档写锁。

设置调用次数、输入输出 token、并行数、墙钟时限和总预算；默认修复上限示例为两轮，达到上限后明确问题，不无限自修复。重试使用指数退避和有限次数，取消停止新调用；迟到响应只允许保留为旧 attempt 的诊断，不覆盖新结果。

服务端不能保证第三方模型调用 exactly-once。网络结果不明时记录 uncertain；恢复已落盘结果而非盲重发。必要重试应计入预算并暴露重复计费风险，不以相同 operation ID 宣称供应商必不重复收费。

## 12 存储布局与逻辑原子发布

建议在既有专题根下增加不可变成果集元数据；隐私与运行态仍在既有私有目录体系内，不擅自固定全工作区绝对路径。

```text
topics/<topic-id>/
  document-set/head.json
  document-set/revisions/<set-revision-id>/manifest.json
  document-set/revisions/<set-revision-id>/shared-design.json
  document-set/revisions/<set-revision-id>/plan.json
  document-set/revisions/<set-revision-id>/validation.json
  thoughts/<document-id>.md
  documents/<document-id>/... existing immutable revisions ...
  attachments/...
```

attempt 草稿、调用记录、来源证据和 staging 使用私有受控目录，不进入默认 Git、公开分享或交付包。具体路径通过现有位置登记与安全路径接口获得。

不能用“N 次更新单文档 HEAD，最后再改目录”实现整套原子。采用如下协议：

1. 在 private staging 准备候选文件和报告，不激活正常列表／搜索／公开入口。
2. 校验全部内容与资源，写入不可变成员 revisions 和 set revision。此时存在的字节不等于对用户可见。
3. 获取统一短提交锁，重新校验 attempt 冻结的所有 generations/hash 和用户编辑基线；不匹配返回 stale/conflict，不覆盖修改。
4. 校验 manifest 可达闭包完整，记录可恢复的 durable intent（包含发布意图及待派发事件清单）；通过原子替换一个 SetHead（含草稿／定稿指针）完成逻辑提交，使用文件及父目录持久化协议。
5. 提交后推进目录、Thought 投影、搜索和 outbox；失败可重放。Thought 投影重放必须携带预期工作版本 CAS，若提交后已有人工修改，则跳过旧投影或按当前工作态重建，绝不能用发布套中的旧正文覆盖新编辑。outbox 可由提交前 durable intent 重建，不依赖提交后首次创建。它们是派生状态，不另有独立发布权威。

必须新增 set-aware resolver 和访问 gate。现有生命周期校验不一定识别 `set_pending`，现有 finalization 遍历个人 HEAD 历史；不得只加一个字段就假设所有入口已隔离。成员通过 set manifest 固定版本读取；个人 HEAD 保持工作编辑职责，不能决定成果集所选版本。

公开／普通直接访问、下载、资源接口及搜索都要检查对象是否已激活、请求版本是否可达。不能因猜到 pending ID 就读到半套内容。首期仍只支持单工作区服务写者，不以此协议宣称分布式事务。

## 13 目录与检索避免混合版本

每次套级读取先取得一个 SetHead 快照，后续目录、正文、链接和状态均固定该 generation。跨多个 HTTP 请求使用 `set_revision_id`，服务端不在请求中途解析 latest。

专题全文检索新增 set-revision 范围，过滤后排名分页，并返回命中的成员 revision；结果点击继续打开同一版本。已定稿成果搜索不能偷偷用最新工作正文。

异步索引落后时，允许显式返回“该版本索引处理中”或受控回退查询完整的上一代索引，须标出版本；不允许将新目录与旧片段混合显示。完成一个索引 generation 的校验后才切换可服务指针。

现有全局工作笔记检索保留，但标识 working 与所属 set 状态；未激活的 staging 对象始终排除。全局参与策略继续由 owner 默认和单篇覆盖决定，不被文档集生成悄悄放开。

## 14 恢复与幂等

所有变更请求带 `operation_id` 和预期 generation；服务端保存规范化请求 hash。同 ID 同请求返回既有任务／结果，同 ID 不同请求报冲突。

恢复先检查 SetHead 的 operation ID 和 manifest hash，并查询保留的 set revision 父链／持久提交账本。某操作已提交后又有更新提交时，当前 head 已不再指向该操作，不能据此判为未提交。账本与版本链须保留足以覆盖未完成收尾操作的证据：

- 未到提交点：从已验证的 staging 继续，或安全标失败；旧发布套保持不变。
- 已到提交点：只补幂等事件与收尾；目录、索引按当前 head 的事实重建，Thought 投影另受工作版本 CAS 保护。不重新生成、不将 head 回退到旧套，也不覆盖后来编辑。
- 文件存在但 hash 不一致／记录歧义：隔离并报告，不能猜测采用较新的文件。
- 一个失败任务不能永久阻断其他无关专题恢复；故障记录保留明确原因与恢复入口。

staging 清理仅处理没有活跃任务、没有发布可达引用、满足保留策略的对象。不能按文件年龄直接删除历史定稿引用的 revision。恢复、备份和垃圾回收使用同一套可达闭包定义。

## 15 增量修订与人工编辑保护

Capture 新增要求先形成共享设计候选变更和影响清单，计算语义依赖闭包。接口变化可影响调用模块、存储映射、流程和验证方案；不只重生成用户点到的一篇。

不受影响成员可以复用旧 revision，但在新 manifest 中声明复用依据，并参与新的整套校验。受影响成员即使文字未改，也须有重新验证事实；不能沿用旧报告假装最新设计已验证。

人工编辑采用 base/generated/manual 三方比较。没有人工变化的机器内容可自动更新；有冲突则保留双方并显示差异，不覆盖。契约自动区的人工改动视为共享契约变更提案，而非允许本篇自行偏离权威定义。用户确认语义变更后更新共享设计，再传播。

新增、删除、拆分或重命名成员形成新计划；已发布版本保持原成员关系。移除文件不能自动缩小必需范围，必须先修订范围或提供等效覆盖。

## 16 跨文件链接与已有资料接入

新增统一逻辑引用格式，例如 `docset://<set-id>/<member-key>#<anchor-id>`，作为建议协议，不依赖物理文件名。链接解析绑定当前 set revision；跨成果集引用须固定外部 set revision 或复制显式快照，禁止隐式漂移。

渲染器、校验器、资源读取和导出同时支持新协议，并明确适配已有 thought/topic 链接。不能为了兼容放开任意 scheme。锚点使用稳定章节 ID，重命名标题不破坏历史链接。

批量 Markdown 接入提供导入预览：原文件相对路径 → 稳定成员 key → 新文档 ID 的映射；检查缺失文件、重复路径、循环引用、大小和资源边界。原文件字节与原件 hash 保留；可编辑候选正文采用派生版本完成链接归一化，不修改历史原件。

普通 Thought 必须先走普通编辑或显式类型化转换，不能再显示一个必然 404 的 typed 编辑入口。转换固定角色 profile、保留 Original 和来源、不自动宣布内容达标。

已有相对路径扫描会把部分兄弟 .md 链接视为非法附件；接入路径应先识别文档引用与附件引用，建立映射，再执行位置迁移。不能建议用户仅修改工作正文就能消除仍在 Original 或历史 revision 中的旧链接。

## 17 Capture 与专题界面

Capture 展示匹配的成果集 profile、范围摘要、预期文件数量、待澄清问题和预算提示，主操作为一次“生成整套”。展开计划是可选检查入口；只有实质歧义、范围变更或权限／预算边界需要确认。

生成过程中展示规划、生成、检查、待复核等阶段和逐文件进度；支持取消、重试失败项、回到对话补充信息。自动保存候选不等于自动定稿；失败草稿可找回，不进入完成计数。

专题页提供：

- 当前整套版本、完成级别、来源 Capture 与共享设计版本。
- 目录、正文、共同契约和跨文件问题面板。
- 问题两侧的文档与章节定位、修复前后差异。
- 草稿套与定稿套切换，明确“新候选尚未完成”。
- 完整版本固定链接、前后篇、搜索、最近位置和窄屏目录。
- 按角色动态选择 profile 与参数，不继续硬编码三种模板。

编辑器关闭／Escape／切换成员时保护未保存输入；保存冲突保留本地内容。固定版本和快照使用可读版本选择器，不要求用户手输 revision ID。

## 18 拟新增接口

以下路由为建议，实施时按现有服务路由惯例统一命名。所有变更遵循工作区现有身份／会话约束，CAS 与 operation ID 不替代鉴权。

| 接口 | 目的 |
| --- | --- |
| GET /api/document-set-profiles | 查询启用的版本化成果集 profile 与角色描述 |
| POST /api/capture/sessions/:id/document-set-plans | 生成／更新计划与共享设计候选，必要时返回澄清问题 |
| POST /api/capture/sessions/:id/document-set-runs | 按冻结计划启动整套任务，202 返回 run_id |
| GET /api/document-set-runs/:id | 查询持久化状态、成员进度、预算与错误 |
| POST /api/document-set-runs/:id/cancel | 幂等取消；已提交结果不能伪装取消 |
| POST /api/document-set-runs/:id/retry | 重试允许的阶段，陈旧输入先拒绝或创建新 attempt |
| GET /api/document-sets/:id/revisions/:revision | 读取固定清单、报告与共享设计 |
| GET /api/document-sets/:id/members/:key/content | 必须给定 set_revision；解析精确成员版本 |
| POST /api/document-sets/:id/reviews | 提交逐项风险处置或人工覆盖审阅，不豁免硬错误 |
| POST /api/document-sets/:id/finalizations | 固定候选套并执行全量门禁和 CAS |
| POST /api/document-sets/:id/exports | 按固定 set revision 导出交付或备份 |

请求至少携带 `operation_id`、`expected_generation`、`capture_generation`、`profile_ref` 或 `plan_hash` 等该操作需要的精确前置条件。缺前置条件 428；冲突／陈旧 409；非法契约 422；预算不足使用可识别业务错误；异步任务失败由 GET 返回结构化状态，不能长期悬挂。

SSE 只用于通知，包含 set_id、run_id、generation、member_key 和事件 ID，不携带不必要私有正文。重复、乱序、断线靠 GET 当前事实恢复，事件不是权威状态。

## 19 定稿 导出 备份与隐私

定稿按完整 set revision 完成，校验 profile 所要求的全部成员及交付级别。达到 proposal 与 ready_for_use 分开；完整交付按钮必须执行同一门禁，不能只看每篇有定稿就放行。允许诊断性导出草稿包，但名称与 manifest 显式标 incomplete，不得混作正式交付。

交付包包含目录、精确成员版本、共享契约的可分享投影、必要附件、版本清单和审阅摘要。原始对话、私有来源摘录、调用日志默认不外发；敏感内容不得通过摘要或共享事实投影旁路泄露。

导出清单记录每个成员的原 revision/body hash、派生 Markdown/HTML hash、资源 hash、链接映射和 set revision hash。只改写派生导出文件中的引用，保留不可变原正文。包内全部成员引用解析为相应文件与稳定锚点；外部依赖未收录时明确 partial，而非静默断链。

备份必须覆盖 SetHead 与所有保留 set revisions 的可达闭包，包括被旧套引用、却不再由当前个人 HEAD 指向的成员 revision、profile、计划、报告和资源。扩展现有 backup/import/Git 白名单，不只遍历单文档当前链。私有证据需显式选择；公开交付包不是完整恢复包。

导入默认拒绝身份冲突，使用 staging 与同样的激活／恢复协议；发布前验证版本、路径、哈希和引用完整性。不开启任意路径读取、可执行模板或自动远端同步。

## 20 代码落点与兼容边界

建议新增 `documentsetprofile`、`documentsetstore`、`documentsetflow` 等职责模块，命名可按仓库惯例调整，但避免将整套事务塞入已有单篇 GenerateDocument 函数。

- models：新增共享设计、计划、成员清单、run、报告、set revision/head/finalization。
- profile registry：识别 single/document_set，冻结子 profile 快照，发布前验证角色引用和限制。
- capture：基于 profile kind 分流，复用已确认事实、来源冻结与输入 generation；保留既有单篇归档恢复。
- ai：新增规划／成员生成／联合审阅结构化协议，沿用超时、取消和严格解析，不信任模型文件路径。
- documentflow/validation：接收明确的共享契约投影，保留单篇门禁，新增最终正文与联合校验。
- topic/objectlocation：受管 set 成员标识、staging 生命周期、可达性和 set-aware resolver；物理布局与唯一原件不变。
- search/events/git/packages：generation 隔离、提交后 outbox、索引切换与备份可达闭包。
- web：Capture 整套生成、进度、问题定位、共同契约、版本选择、编辑安全和动态 profile。

新 schema 必须声明最低 writer 能力；无法识别成果集元数据的旧二进制不得写入新布局。保留既有单篇及普通 collection 的回归，不默认迁移所有历史数据。旧专题接入通过显式预览并创建新 set revision，不修改旧定稿。功能开关关闭后仍须允许读取和导出已存在的成果集；不能因禁用生成而使数据失联。

## 21 实施顺序与每阶段退出条件

| 阶段 | 范围 | 退出条件 |
| --- | --- | --- |
| P0 契约与夹具 | 新模型、profile、共享事实、至少五篇设计夹具 | schema／安全限制／覆盖计算单测通过，基线兼容明确 |
| P1 最小整套纵向链 | Capture 分流、规划、生成、单篇与联合硬检查、私有预览 | 从一次对话自动产出五篇，无手工建稿／绑定步骤 |
| P2 联合质量与修订 | 语义覆盖、澄清、有限修复、增量影响、人工编辑保护 | 各篇单独通过但跨篇冲突的反例被拦截 |
| P3 一致发布与恢复 | SetHead CAS、全入口可见性、索引／事件、崩溃恢复 | 任一故障点均不暴露混代或半套定稿 |
| P4 用户闭环与交付 | UI、链接、导入转换、正式导出、备份恢复 | 浏览器＋真实 API 从 Capture 到导出恢复全链通过 |
| P5 收口验证 | 全量回归、并发／race、安全及精确提交 CI | 以下必测项全部有证据，未运行不计通过 |

阶段是实施次序，不是拆掉核心承诺的分期。P0–P5 完成前不能宣布“多文件 Capture 自动生成已收口”。不以目录 UI、文件数量、模型成功率或旧提交 CI 替代验收。

## 22 验收矩阵

每项需登记测试名称、固定夹具、执行环境、实际结果与日志。`自动化` 是目标执行方式，不表示本文已执行。至少建立一套含架构、两个模块、接口、存储、验证共六篇的参考设计，以及至少一个非软件的多文件 profile。

| ID | 场景 | 必须观察到的结果 |
| --- | --- | --- |
| A01 | 单篇 profile 原流程 | 既有生成、保存、编辑、定稿行为不退化 |
| A02 | 同一 Capture 选择成果集 profile | 自动生成完整计划，不要求逐篇建条目 |
| A03 | 范围无持久化 | 存储角色按确认规则省略，覆盖理由可追踪 |
| A04 | 是否持久化未知 | 请求澄清，不把未知当 false |
| A05 | 多模块输出 | 每个确认模块有稳定成员 key，重命名不换 ID |
| A06 | 超过成员／预算限额 | 明确阻断或缩减提案，不截断后报完成 |
| A07 | 自定义 role profile | 界面、服务、生成与校验使用同一精确版本 |
| A08 | profile 含任意表达式或路径 | 注册／生成前拒绝，不执行代码 |
| A09 | 生成中 profile 更新 | 在途任务使用冻结版本；不能混用新模板 |
| A10 | 用户事实与来源文本冲突 | 进入澄清，来源提示不能改变权限或确认事实 |
| A11 | 软件模块循环依赖 | 不被错误地当调度环直接拒绝；共同契约可先规划 |
| A12 | 真正生成 DAG 环 | 注册／规划时报清晰错误，不死循环 |
| A13 | 必需需求没有文档位置 | 联合硬门禁失败，完成度不通过 |
| A14 | 两篇声明同一接口 owner | 拦截重复权威定义 |
| A15 | 每篇通过但 API 与存储类型矛盾 | 整套失败，定位双方字段及版本 |
| A16 | 可空／错误码／权限语义不一致 | 对适用规则给出硬冲突或明确语义阻断，不能忽略 |
| A17 | sidecar 正确而正文写错字段 | 最终正文核对／语义审阅发现，不能仅凭 sidecar 通过 |
| A18 | API 计算字段无数据库列 | 有明确分类和映射时通过，不产生机械误报 |
| A19 | 缺模块职责或验证覆盖 | 识别遗漏，不因文件存在而计完成 |
| A20 | 联合审阅无输出或非法 JSON | error/not_run，不是 passed |
| A21 | 长文超出上下文 | 分片覆盖台账完整，未覆盖对象阻止通过 |
| A22 | 语义风险人工确认 | 保存精确位置、理由、候选 hash，状态标人工审阅 |
| A23 | 人工确认确定性契约冲突 | 服务端拒绝绕过 |
| A24 | 单成员内容改变 | 原套报告对新候选失效 |
| A25 | 未变成员复用旧 revision | 仍进入新套联合校验；manifest 指向精确旧版 |
| A26 | 架构接口更新 | 受影响模块／存储／验证进入待复核闭包 |
| A27 | 用户同时手工编辑 | CAS／三方比较保护输入，不被生成结果覆盖 |
| A28 | 用户修改自动契约表 | 形成共享契约变更提案，不允许单篇悄悄分叉 |
| A29 | 取消后模型迟到 | 不推进当前任务或覆盖新 attempt |
| A30 | 同 operation 重试 | 同请求返回同任务；不同请求明确冲突 |
| A31 | 供应商响应不明 | 记录 uncertain 与预算，不承诺外部 exactly-once |
| A32 | staging 写入后崩溃 | 旧套保持可见，新半套不进入列表／搜索／分享 |
| A33 | 不可变 revision 写入后崩溃 | 未提交版本可恢复或回收，不被 latest 泄露 |
| A34 | A 提交后崩溃，B 随后提交并有人编辑 | 从账本认定 A 已提交；不重跑 A、不回退 B、不覆盖人工编辑，只补幂等事件和当前投影 |
| A35 | 提交前 catalog 或文档 generation 变化 | 返回冲突，旧／人工修改内容均保留 |
| A36 | 草稿生成提交及并发套阅读 | 自动生成只变 working 指针、定稿不变；同一请求和固定版本导航不混代 |
| A37 | 旧索引尚未重建 | 显示版本／处理中，不返回混合新旧结果 |
| A38 | 猜测 pending 文档或资源 ID | 普通读取／下载／公开入口均拒绝 |
| A39 | SSE 重复乱序断线 | GET 可恢复真实状态，完成不依赖事件到达 |
| A40 | 一个恢复任务损坏 | 隔离该任务，不无限阻塞其他专题 |
| A41 | 文档间逻辑引用 | 页面、固定链接及离线包均能定位正确版本锚点 |
| A42 | 标题／文件名变更 | 稳定成员链接保持有效 |
| A43 | 批量关联 Markdown 导入 | 文件映射明确；缺失依赖提示，Original 字节不变 |
| A44 | 普通 Thought 加入后编辑 | 正确分流／转换，不再 typed API 404 |
| A45 | 编辑器关闭／切换 | 未保存输入得到保护，冲突不清空内容 |
| A46 | 正式导出含未达门槛必需项 | 正式交付拒绝；诊断草稿包明确 incomplete |
| A47 | 导出链接改写 | 原正文 hash 不变，原始与派生 hash 可核对 |
| A48 | 私有证据与对话 canary | 默认交付与 Git 不包含；显式备份按权限处理 |
| A49 | 旧套引用不在个人当前 HEAD 的 revision | 备份／恢复后旧套仍完整可读 |
| A50 | 导入目标 ID 冲突／崩溃重启 | 拒绝覆盖、可恢复，无部分正常激活 |
| A51 | 删除旧 staging／垃圾回收 | 不删除任何保留套可达的 revision 或资源 |
| A52 | 六篇真实进程全链 | Capture → 自动生成 → 联合检查 → 保存／定稿 → 导出 → 空工作区恢复，无手工文件补齐 |
| A53 | 六篇各自合格但整体矛盾夹具 | 确定性 stub 产生的冲突稳定被拦截，可在 CI 重现 |
| A54 | 非软件成果集 profile | 使用相同机制完成，不依赖硬编码软件文件名 |
| A55 | 桌面／窄屏与前进后退 | 套版本、阅读位置、错误与编辑状态正确恢复 |
| A56 | 旧程序／功能开关 | 不认识新 schema 的 writer 拒绝写；禁用生成仍可读取历史套 |

## 23 测试策略与发布门槛

分开验证机制正确性与模型质量。CI 使用可控的结构化生成器／reviewer stub，覆盖正确、矛盾、遗漏、超时、非法输出、取消和重试。不能只使用模拟 HTTP 页面证明后端真实完成；A52 要真实服务进程、临时工作区和最终磁盘文件，并核对重启后的状态。

真实模型评估另在明确预算和允许外发的数据范围内进行，固定案例、模型配置和评分规则，记录完整输出与人工判定。它衡量生成质量，不替代确定性恢复测试，也不以单次成功承诺所有设计一致。本文不授权执行真实付费调用。

必须运行相关 Go 单测／race、Node／i18n、构建、API/SSE e2e、浏览器桌面与窄屏、导出／导入和故障注入，再检查最终确切提交的 CI。未安装工具、环境权限限制、跳过平台、无法读取 CI 均记 NOT_RUN／UNVERIFIED，不能计通过。

本次基线核对已运行 Node 138 项、i18n 6 项和 JS 语法检查；链接渲染和编辑入口另有 VM 证据。Go 编译器不可用，真实 API 未运行，浏览器受环境限制，最新 CI 未核实。这些仅说明现状核对边界，**不构成本方案新增能力的验收证据**。

最终收口需提交：实现 commit、需求到测试映射、全部必测结果、恢复故障证据、至少一套六篇成果及离线交付／恢复样例、明确未支持边界。任何核心项缺失不得仅凭旧 R1–R16 关闭宣布本次完成。

## 24 源码依据与最终交付定义

以下链接固定到核对基线；现状章节以源码为依据，其余新增机制是本方案设计决策。

- [现有单篇 profile 结构](https://github.com/muidea/thought-flow/blob/123bc1ed7762eb1e5b4d379d56dbcb8f5ddcefe9/internal/pkg/documentprofile/profile.go#L69-L82)
- [Capture 单篇生成和渲染](https://github.com/muidea/thought-flow/blob/123bc1ed7762eb1e5b4d379d56dbcb8f5ddcefe9/internal/modules/capture/biz/scratchpad.go#L1300-L1450)
- [Capture 单 Thought 归档](https://github.com/muidea/thought-flow/blob/123bc1ed7762eb1e5b4d379d56dbcb8f5ddcefe9/internal/modules/capture/biz/archive_pipeline.go#L210-L310)
- [现有专题目录与定稿选择](https://github.com/muidea/thought-flow/blob/123bc1ed7762eb1e5b4d379d56dbcb8f5ddcefe9/internal/pkg/topicstore/catalog.go)
- [现有单文档校验](https://github.com/muidea/thought-flow/blob/123bc1ed7762eb1e5b4d379d56dbcb8f5ddcefe9/internal/pkg/documentvalidation/validate.go)
- [现有文档版本与定稿](https://github.com/muidea/thought-flow/blob/123bc1ed7762eb1e5b4d379d56dbcb8f5ddcefe9/internal/pkg/documentflow/service.go)
- [现有导出与备份](https://github.com/muidea/thought-flow/blob/123bc1ed7762eb1e5b4d379d56dbcb8f5ddcefe9/internal/pkg/topicstore/packages.go)
- [现有相对资源扫描](https://github.com/muidea/thought-flow/blob/123bc1ed7762eb1e5b4d379d56dbcb8f5ddcefe9/internal/pkg/objectlocation/relative.go)
- [原专题工作区设计及旧范围](https://github.com/muidea/thought-flow/blob/123bc1ed7762eb1e5b4d379d56dbcb8f5ddcefe9/doc/ThoughtFlow-Topic-Workspace-Closure-Design-v1.md)

**本次交付定义：一次 Capture 设计讨论，经必要澄清后，由版本化 profile 自动规划并生成多份关联文件，依据共同设计事实完成单篇与整套校验，统一保存可追溯的版本组合，并在修改、失败和恢复时继续保持完整性与一致性。**

仅能创建专题、生成多个文件、逐篇校验或下载 ZIP，均不满足这一收口定义。

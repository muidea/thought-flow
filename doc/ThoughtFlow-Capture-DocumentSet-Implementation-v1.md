# Capture 文档成果集实现与验收记录

日期：2026-10-27。设计依据：[收口方案](ThoughtFlow-Capture-DocumentSet-Closure-Plan-v1.md)。基线：`123bc1ed7762eb1e5b4d379d56dbcb8f5ddcefe9`。本记录对应包含本文的实现提交；本地验证已完成，远端同一提交的 CI 尚未验证。

## 实现范围

- P0：独立的版本化 document-set profile、冻结角色 profile、共享事实、覆盖责任、生成 DAG、预算、候选摘要、套修订与审批记录。内置软件设计六篇夹具和研究成果三篇夹具，模块身份与标题分离。未知条件和缺少确认的事实进入澄清。
- P1：Capture 规划/生成接口、持久化任务、单篇契约、确定性装配及联合硬检查。来源和 profile 固定；用户一次生成整套，不需要逐篇创建、绑定或迁移。原单篇归档路径保留。
- P2：联合审查台账、章节原文证据、完整人工审阅、风险逐项处置、两轮以内局部修复、语义影响闭包、无影响成员复用及三方合并。硬冲突不能人工豁免，自动契约修改保存为私有变更提案。
- P3：成员 revision 准备不发布个人 HEAD，套级 SetHead 单次 CAS 发布完整 working 套；finalized 独立更新。预写 durable intent，派生目录/投影/事件/Git 可重放，不回退新 HEAD，不覆盖后来人工修改。普通读取在整套投影完成前拒绝，固定套入口仍读取全部不可变成员。
- P4：Capture 状态气泡、可选计划和 Markdown 映射、取消/有限重试、已提交投影恢复（不调用模型）、人工审查表、套阅读器、共同契约和问题位置、固定版本链接、全文词法搜索、目录/上下篇/最近位置、窄屏界面、动态角色模板、普通编辑和显式类型化转换。固定套导出、备份与空工作区导入恢复。
- P5：新增确定性、故障注入、浏览器和真实进程 API 回归。实际执行结果在下表登记；远端精确提交的 CI 验证仍待完成。

## 存储与发布约束

`documentset` 负责数据契约、规划规则、渲染和检查；`documentsetstore` 负责不可变版本、可达闭包、发布和包；`documentsetflow` 负责任务、模型调用、人工审阅与恢复。单篇 `documentstore.Prepare` 只准备字节，不改变个人 HEAD。

套级元数据在 `topics/<set-id>/document-set/`。计划预览、调用账本、失败尝试、三方比较及 outbox 在受控私有目录。公开 manifest 去掉原始对话和用户证据原句，保留来源位置与摘要。普通数据保护、受控资源根和单工作区 writer 租约继续生效。

成员读取、资源和导出固定 `set_revision`；读取历史资源使用不可变绑定，不混入后来上传的附件。定稿指针必须与不可变审批历史完全一致，交付再核对精确修订和报告 hash。完整备份追踪当前 working、全部保留审批及父版本，包括不再由个人 HEAD 选择的 revision、原件和资源。导入先验证闭包和完整门禁，再通过单一套 HEAD 激活，目标冲突不能覆盖文件。

不可变 manifest、单篇 profile/source/contract/body、派生的共享设计/计划/报告均重新核验。`ready` 字符串不是授权；提交、定稿、交付和导入都重新检查精确候选 hash、检查器版本、结构、清单及证据覆盖。

## 操作方式

1. Capture 中选择“生成整套”，选择启用的成果集 profile；可选择关联 Markdown 并显式映射成员。
2. 系统规划共享事实与清单。实质缺失显示问题，回对话补充后重新规划；没有缺失时直接生成。
3. 任务进度与成果状态分开：候选待审阅、完整草稿已提交、联合检查通过、整套定稿各自显示。
4. 专题“文档”页阅读固定套，查看报告或定位问题；定稿是独立操作，交付必须达到 ready_for_use。
5. 已提交但投影/通知失败时，Capture 提供不调用模型的恢复按钮；只重放派生状态，不重跑生成。套备份导入走 staging；有冲突保留任务诊断。禁用生成后已有套仍可读和导出。

普通 Thought 先打开普通编辑器，可显式选择精确单篇 profile 转换；转换保留 Original 和来源，不自动宣布合格或定稿。编辑保存使用内容 hash / generation CAS；冲突保留输入，关闭、Escape、切换保护未保存内容。

## 执行证据

本地：Linux x86_64，Go 1.24.13，Node 20，Chrome；临时工作区及本地可控模型网关。没有调用真实外部模型，没有修改真实用户工作区或重启用户服务。

| 检查 | 状态与日志 |
| --- | --- |
| 第一轮成果集单测 | PASS；`/tmp/thoughtflow-docset-complete-focused.log`，后续加固需最终检查覆盖 |
| Node 组件 / i18n / JS 语法 | PASS：最终 142 / 142、6 / 6；`/tmp/thoughtflow-docset-ci-final.log` 的 node-test / i18n-check / node-check |
| Chrome 完整浏览器、固定套桌面/窄屏导航 | PASS；最终 `/tmp/thoughtflow-docset-ci-final.log` 的 `ci: ok browser-test`，包含本文新增阅读/编辑及历史导航回归 |
| Firefox / Safari 本地自动化 | NOT_RUN；Firefox 缺少 Playwright npm 包，Safari/WebKit 在本 Linux 主机不可用；browser-test 的两项可选 smoke 被跳过，不计通过 |
| 编辑器关闭/Escape/冲突保留 | PASS；`/tmp/thoughtflow-docset-editor-browser-fixed.log` |
| 刷新与会话选择 | PASS；`/tmp/thoughtflow-docset-browser-reload-fixed.log`：等待新 document load；桌面/手机归档状态恢复；`/tmp/thoughtflow-docset-history-ui.log`：相同列表不替换按钮 |
| 最终 Go 与新增故障回归 | PASS；最终 `/tmp/thoughtflow-docset-ci-final.log` 的 `ci: ok test`，包含全部 Go 包和新增故障回归 |
| 完整 make ci-check | PASS：fmt-check、test、build、node-check、node-test、i18n-check、browser-test、e2e-test 八个阶段全部通过；`/tmp/thoughtflow-docset-ci-final.log` |
| 定向 race | PASS；`/tmp/thoughtflow-docset-race-final.log`：documentset 3.005s、documentsetstore 1.350s、documentsetflow 314.825s；不宣称全仓 HTTP race |
| 六篇真实进程及 Markdown 原件往返 | PASS；`/tmp/thoughtflow-docset-acceptance.log`：真实二进制、6 篇、两份互链 MD、定稿/交付/备份/重启/空工作区恢复、Original 字节一致 |
| 六篇交付与恢复样例 | PASS；`/tmp/thoughtflow-docset-acceptance/` 的 `delivery.zip`（15 文件）、`backup.zip`（60 文件）、`set.json`、`verification.json` 与已解包 `delivery/index.md`；全部交付文件 hash/大小及 6 篇原正文 hash 已独立核对 |
| Ubuntu / macOS 精确提交 CI | UNVERIFIED；须针对包含本文的实现提交核对，不能引用旧 SHA 的 CI |
| 真实模型质量评估 | NOT_RUN；方案未授权真实付费调用 |

临时样例与日志不进入 Git。构建当前二进制后，可复现六篇样例（仅使用测试内本地网关）：

```sh
THOUGHTFLOW_DOCSET_ARTIFACT_DIR=/tmp/thoughtflow-docset-acceptance \
  node --test --test-name-pattern='Capture document set:' web/api.e2e.test.js
```

## A01–A56 需求与回归映射

此表记录具体检查入口，不把源码实现当成已执行用例。Go 测试分别位于 `documentset/profile_test.go`、`documentsetflow/{service,edges,closure}_test.go`，下文简称 profile / flow。浏览器在 `web/app.browser.test.js`；真实进程在 `web/api.e2e.test.js`。最终执行状态以最终日志为准。

| ID | 实现与针对性检查 |
| --- | --- |
| A01 | 原单篇分支保留；完整 Go、Node、typed document browser、原 API/SSE 回归 |
| A02 | `TestSixMemberSetAndPinnedRevisions`；真实进程 Capture document set 六篇链路 |
| A03 | `TestStatelessScopeAndModuleRenameHaveStableMembership`：无持久化省略 storage，保留 omission 理由 |
| A04 | `TestUnknownConditionsAndFalseOmissions`：unknown 不按 false 处理 |
| A05 | `TestStatelessScopeAndModuleRenameHaveStableMembership`：模块标题改名仍保持 key/ID |
| A06 | `TestConservativeBudgetsAndContextCoverage`、profile 安全测试的 member-budget；不截断输出 |
| A07 | `TestPublishedSetProfileResolvesExactRoleSnapshot`；界面从 profile 获取角色/参数；自定义 role 全浏览器生成组合尚未单独穷举 |
| A08 | `TestProfileSafetyAndGenerationDAG`：表达式、未知规则、未固定角色均拒绝；文件建议由服务端产生 |
| A09 | `TestCommitCASAndProfileSnapshotIsolation`：执行使用冻结 role/profile 快照 |
| A10 | `TestPlannerCannotRewriteConfirmedFactsUsingOldEvidence`，拒绝伪造用户证据；新事实未确认进入澄清；任意自然语言注入不能被本用例穷尽证明 |
| A11 | `TestSharedFactImpactAllowsModuleCycles`：软件图可有环 |
| A12 | profile cycle / `Acyclic`：真正生成图环拒绝 |
| A13 | `TestFinalBodyAndSemanticEvidenceCannotBeFaked`：缺必需成员/覆盖拦截；计划成员按 profile 重算 |
| A14 | 同上 owner duplicate：其他成员不能重定义契约 owner |
| A15 | `TestJointConflictAndMissingReviewCannotPublish` 的 type-conflict，定位接口/实体 |
| A16 | final-body nullable mismatch；权限/错误语义在共同契约和逐关系语义审查中检查，未声明自然语言自动证明 |
| A17 | final-body sidecar true body false：校验落盘正文的确定性契约块 |
| A18 | 计算/外部字段不强制持久化映射；stateless 场景使用 computed 字段 |
| A19 | final-body no verification coverage；成员存在不能替代职责/验证关系覆盖 |
| A20 | missing-review / malformed review 返回 error/incomplete，不视作通过 |
| A21 | conservative budget/context：缩小审查批次，单项仍超限则阻断并提供完整人工审查，不截断后 passed |
| A22 | `TestManualReviewRequiresEntireChecklistAndHardErrorsNotWaivable`；逐条完整证据、理由、位置和候选 hash |
| A23 | 同上 hard errors：不能人工豁免确定性错误 |
| A24 | final-body changed revision / candidate hash：旧报告不能授权新内容 |
| A25 | old intent / revision reuse：未变成员精确复用，新的候选重新全套检查 |
| A26 | shared fact impact：接口/实体/关系变化传播影响，有限修复重新检查完整套 |
| A27 | `TestThreeWayMergeRetainsIndependentChanges`、manual recovery / commit CAS：重叠保留三方，写入中变化拒绝 |
| A28 | `TestManagedContractEditCreatesProposal`：契约修改保留私有提案并阻断独立分叉 |
| A29 | `TestCancellationDiscardsLateResultAndIdempotentStart`：迟到不发布 |
| A30 | 同上 idempotent start；请求 hash 不同返回冲突 |
| A31 | `TestBoundedCallsRetryOnlyProvenUnexecutedFailures`：未知执行记 uncertain；重试须明确承认重复计费风险 |
| A32 | `TestPreHeadFaultsCannotExposeMembersAndOtherRecoveryContinues`：revision/intent 阶段中断不公开 |
| A33 | 同上，不可变成员存在不代表可读；不提供按文件年龄删除的 GC |
| A34 | `TestOldIntentNeverRollsBackNewHeadAndManualContent`：旧 intent 只补派生状态，不回退新套/覆盖人工修改 |
| A35 | commit CAS / profile isolation；catalog、位置/绑定与文档原始字节基线再次核验 |
| A36 | six-member working 不改 finalized；`TestProjectionFailureGatesEntireGenericSetAndRecovers`；套内读取始终固定修订 |
| A37 | 套搜索直接查询固定不可变正文，不依赖异步全局索引；浏览器 old/new 搜索迟到隔离 |
| A38 | `TestOriginalPendingRawAndResourceGate`、pre-head faults、projection gate：pending 原始读取/下载不可达 |
| A39 | Node 的 set event A→B→A；状态从 GET/持久化会话恢复，SSE 不承担提交权威 |
| A40 | `TestCorruptRecoveryDoesNotBlockUnrelatedSet`：损坏任务诊断保留，不阻断无关套 |
| A41 | 固定套浏览器目录/逻辑链接和历史导航；导出保留各成员文件与锚点；Unicode offline anchor 回归 |
| A42 | stable member key/ID 与标题解耦；固定套和逻辑链接使用 key，不用物理标题 |
| A43 | `TestMarkdownPreviewRetainsOriginalAndDetectsBoundaries`；真实进程两份互链 MD、六篇成果、恢复后 Original 字节核对 |
| A44 | `TestPlainThoughtConversionPreservesOriginalAndRequiresCAS`；普通编辑/显式转换分流，避免 typed API 404 |
| A45 | typed document browser 实际关闭/Escape/冲突保留；编辑请求 epoch、路由/成员切换 dirty guard；普通编辑内容 CAS |
| A46 | 真实 API 在定稿前交付 422；正式交付复核完整门禁，诊断包标 incomplete |
| A47 | 交付记录原 body hash、派生 MD/HTML hash 与文件哈希；原 revision 不被改写；真实进程导出/恢复比较套 hash |
| A48 | `TestDefaultPublicationClosureExcludesPrivateConversationAndEvidence`、Git allowlist 新回归；默认不包含原始对话/私有证据 |
| A49 | `TestLegacyTopicBackupContainsRetainedSetClosure`、old-intent / backup round-trip：包括旧套选择的非个人当前 revision |
| A50 | backup/import conflict recovery，目标占有预检、激活 gate、重启恢复；不能覆盖不同字节 |
| A51 | publication closure test 在现有临时投影清理后重读保留套；首期无自动 revision/staging GC，全部保留引用闭包 |
| A52 | `Capture document set: six files, joint gate, fixed reading, delivery, backup and restart`：真实二进制/临时磁盘/本地网关，非 mock 页面代替后端 |
| A53 | `TestJointConflictAndMissingReviewCannotPublish` 及 final-body 矛盾夹具，CI 无外部调用 |
| A54 | `TestNonSoftwareSetUsesSamePublicationAndGate`：研究三篇使用同一事务和门禁 |
| A55 | document set reader 浏览器 desktop/mobile：实际目录点击、逻辑链接、版本选择、后退、刷新和迟到搜索 |
| A56 | writer version 2、原有 layout guard；`TestFrozenSetResourcesAndDisabledGenerationRemainReadable`：关闭生成仍读/定稿/导出已有内容 |

## 明确边界

- 预算是调用账本、输入输出字节、UTF-8 字节作为保守 token 上界、上下文和墙钟限额；不是供应商计费结算。供应商内部的兼容协商与重试仍遵循既有 AI 层；不能据此承诺上游 exactly-once 或精确金额。成员目前串行执行，始终低于并发上限。
- 长文按关系批次缩小上下文；单个检查对象过大时阻断并要求覆盖同一清单的完整人工审查，不声称模型已覆盖。语义错误必须修正文；仅非阻断风险允许逐项记录处置。
- 三方合并保守处理单个连续改动范围；相邻/重叠修改保留三方并拒绝自动覆盖。契约提案通过继续对话修订共享设计/计划处理，不在单篇编辑器中直接批准共享事实。
- 关联 Markdown 先支持 .md 文件，完整保留 Original；附件不隐式读取或下载，明确提示缺失，本地依赖未解决不得作为完整交付。远程图片未收录时不能宣称离线完整。
- 套搜索为固定版本的词法全文查询，不提供套级专用向量索引。普通全局搜索继续遵循原有 owner/单篇策略。
- 没有自动清除保留套的 revision/资源；没有多租户/分布式写入、远程 Git 推送、真实模型质量承诺。历史数据兼容按用户已有豁免处理，不自动改写旧定稿或批量转换既有专题。
- 远端 Ubuntu/macOS 同 SHA 的 CI 以及人工真实模型质量评估尚未验证，不能记为通过。正式验收应取得对应证据后更新此记录，保留每项的实际验证层级。

## ce437dc 复核后 DS01–DS12 增量修复

日期：2026-10-27。对照评审基线 `ce437dcbb3fb6d807a2d06a0aab7d937407961b4`；本节对应包含本节的 DS01–DS12 修复提交，不借用基线 CI 证明修复。保留上面的 A01–A56 映射，以下反例补充原有成功夹具。

| 项目 | 修复和回归入口 |
| --- | --- |
| DS01 / A27、A34 | 个人 HEAD 保存投影前原始字节 hash 和预期输出 hash。pending 恢复只能接受这两种字节，其他输入保留并报冲突。`TestPendingProjectionChecksBothCrashWindows` 覆盖 HEAD 已写/正文未写、正文已写/pending 未清，以及两处外部编辑和无编辑恢复。 |
| DS02 / A04、A10、A13 | 条件改为 value/status/source/reason；无用户原句证据或未确认时进入澄清，不允许作为已确认省略。条件变更不能复用旧证据。私有目录保存冻结 Capture，联合审查新增 `capture-scope`，独立对照原始消息和确认需求，不只对照 planner 事实。`TestConditionalOmissionRequiresConfirmedUserEvidence`、`TestConditionChangesCannotReuseOldScopeEvidence`、`TestIndependentScopeReviewBlocksPlannerOmittingUserRequirement`。自然语言含义仍由独立语义/完整人工审查判断，不宣称字符串匹配能够证明语义。 |
| DS03 / 来源冻结门禁 | 会话缺失与关联来源缺失使用不同错误；严格会话读取不合成空对象。来源缺失、权限/其他错误均拒绝定稿。仅没有外部来源且冻结门禁完整的恢复成果允许缺少原会话。`TestFinalizationDistinguishesMissingCaptureFromMissingSource` 实际删除来源并核对 Finalized 不变，另覆盖缺失会话/权限错误；`TestSetSnapshotMissingRelatedThoughtIsNotMissingCapture` 验证服务层实际来源删除错误分类。 |
| DS04 / A26 | `scope_changes` 记录明确删除、新用户证据、影响成员、剩余/替代覆盖。无授权遗漏仍阻断。`TestCaptureCanExplicitlyRemoveConfirmedModule` 通过 planner 路径验证未授权遗漏、用户删除及再确认、新五篇套提交、旧六篇套仍可读。 |
| DS05 / §8、A15、A52 | 生成请求加入适用事实/关系闭包及固定上游正文、文档/修订身份和 hash；无更细适用声明的接口/实体契约供模块和验证角色使用。仍检查预算，超限拒绝，不截断后宣称覆盖。契约/上游版本变化使相关成员失效，不能只更换 dependency ID 后复用解释。`TestGenerationReceivesContractsAndImmutableUpstream` 捕获实际请求，核对认证/幂等、事务约束、关系和磁盘不可变上游正文/hash，并验证 scope 审查接收原始需求。 |
| DS06 / A22、§9 | 单篇内容警告进入联合 issue 台账，附成员、真实正文位置及候选 hash；重新检查不能删掉已知风险。流程性 `manual_review` 提示由套级语义/人工审查门禁承担；来源未验证等内容风险须逐项处置。`TestSourceWarningSurvivesJointReviewAndNeedsRiskDecision` 覆盖模型通过不能抹除风险、无处置不能 ready、实际人工审阅/提交/定稿及交付 `review.json` 保留风险和理由。 |
| DS07 / A41、A46、A47 | resource scheme 不再直接意味着 partial；按冻结绑定、共享许可、实际字节和离线依赖判定。`TestPublicFrozenAttachmentDeliveryAndGitClosure` 实际上传 PNG 和文本附件，正式交付含两个资源并改写链接；缺失、损坏、不可分享资源拒绝交付。 |
| DS08 / A33、A38 | 普通 revision 导出检查精确可达性：个人已发布历史或保留套 manifest 选中的精确修订。准备中的同 ID 新 revision 不因旧成员 active 而获得读取资格。内部生成仍可显式读取 FrozenRevision。`TestOrdinaryExportRejectsPreparedRevisionOfExistingMember` 覆盖 active 成员准备新版本、提交前拒绝、提交后与旧历史可读。 |
| DS09 / §19、§20 | 套级 ContentPaths 包含个人 HEAD、当前 Thought 投影、当前绑定记录/公开资源，以及套保留版本闭包；不等待搜索/embedding 追加 Git 路径。`TestPublicFrozenAttachmentDeliveryAndGitClosure` 在没有索引服务帮助的临时仓库只提交此清单，再实际 clone；普通成员和资源均可读取，私有 intent/run 不在 clone 中。`TestDefaultPublicationClosureExcludesPrivateConversationAndEvidence` 同时验证实际套级提交回调及私有 canary。 |
| DS10 / A30、A39、P4 | 重试在启动新 attempt 前，按会话输入代次、set ID 和旧/新 run ID 前置条件持久化关联；旧请求幂等回放不能覆盖更新的会话任务。`TestSetRetryPersistsNewRunBeforeReturningAndRejectsStaleBinding` 用真实 HTTP handler 验证重试后重新 GET 会话恢复新任务，无需 SSE，并拒绝迟到旧重试。 |
| DS11 / A39、A55 | 任务 SSE 先核对服务端持久会话关联，再使用选择/操作 epoch、请求序号和持久 state_version 保护 UI。轮询被事件超越时继续提供后备刷新；其他页面创建的权威新任务可被采用。Node 覆盖同会话旧/新 attempt、同 attempt 响应逆序、旧状态版本、事件/轮询交错和 A→B→A。 |
| DS12 / A42、A55 | 版本选择先查目标 manifest：保留存在的 key，否则选择目标首个成员；用户手输无效固定链接仍报明确错误。Node 验证成员增减；Chrome 的 `document set reader pins version...` 在 desktop/mobile 实际点击新增 storage、切回不含 storage 的旧套，并验证前进/后退和刷新。 |

本轮验证（Linux、Go 1.24.13、临时工作区和本地确定性模型网关）：

| 检查 | 本轮实际结果 |
| --- | --- |
| 相关 Go 包与服务测试 | PASS；`/tmp/thoughtflow-ds-review-focused-final.log`；后续新增的独立 scope 对照反例已纳入最终完整 Go 检查。新增测试位于 `documentsetflow/review_regression_test.go`、服务层 `document_sets_review_test.go` 及 profile 测试。 |
| 完整 `make ci-check` | PASS；`/tmp/thoughtflow-ds-review-ci.log`：fmt-check、全量 Go test、build、node-check、node-test、i18n-check、browser-test、e2e-test 八阶段全部通过。Node 组件 147 项、i18n 6 项。 |
| 定向 race | PASS；`/tmp/thoughtflow-ds-review-race.log`：DS01–DS10 后端反例和独立范围审查，包含真实磁盘、并发生成、HTTP 任务绑定、资源和 Git 克隆；flow 111.262s、服务 2.517s、profile 1.390s。store 由 flow 场景执行，直接包过滤器无匹配测试；不将此记作全仓 race。 |
| 浏览器 | PASS；完整 Chrome 矩阵以及修改后的成果集 desktop/mobile 成员增减、版本/历史导航。Firefox/Safari 可选 smoke 沿用环境跳过，不计通过。 |
| 六篇真实进程及包验证 | PASS；`/tmp/thoughtflow-ds-review-acceptance.log`，显式选择六篇场景时其他两个顶层测试按名称过滤跳过；完整 API/SSE 另由 ci-check 通过。`/tmp/thoughtflow-ds-review-acceptance/` 含 set.json、delivery.zip（15 文件）、backup.zip（60 文件）；两包 complete，分别核对 14/59 个清单文件的字节长度和 SHA-256。 |
| diff 空白检查 | PASS；`git diff --check`。 |
| 当前修改的远端同 SHA CI | 未验证：须核对包含本轮修复的提交，不能沿用基线结果。 |

真实外部模型调用和生产工作区迁移均未执行；不把确定性模型夹具当作真实模型质量验证。远端基线 CI [40454691693](https://github.com/muidea/thought-flow/actions/runs/40454691693) 的 Ubuntu/macOS 两项已核实成功，仅证明基线；本轮修复提交的远端 CI 尚未验证。

## e002559 后 Ubuntu CI 任务成员越界修复

本轮 CI 在 `documentsetflow.execute` 更新 `run.Members[i]` 时发生越界。已在本地通过重复运行 `TestSetRetryPersistsNewRunBeforeReturningAndRejectsStaleBinding` 复现相同堆栈（`/tmp/thoughtflow-ci-run-members-repro.log`）。该测试构造的失败任务有完整计划却没有成员进度；若后台任务先于测试清理进入生成循环，就会崩溃。此前本地一次通过没有覆盖这个调度分支，不作为该分支正确的证明。

修复包含两层：测试夹具按冻结计划建立完整成员状态，并等待生成器实际启动后继续检查会话持久关联；执行器及重试/幂等重放入口校验冻结计划和成员数量、顺序、身份。损坏记录在调用模型、修改会话关联或发布成果之前拒绝，执行阶段保留 `failed_terminal` 诊断。

已通过 GitHub CLI 读取精确提交 `e002559264992bfac9279020f962c978bc852af2` 的 [CI run 40463976541](https://github.com/muidea/thought-flow/actions/runs/40463976541)，确认 macOS 成功，Ubuntu 在 Go test 阶段发生上述相同 panic；完整失败日志保存于 `/tmp/thoughtflow-e002559-ci-failed.log`。这不是将 DS06/DS11 静态发现推断为远端失败原因。

新增 `run_members_test.go`：`TestExecuteRejectsInvalidMemberProgress` 覆盖缺失、截断、错序、重复、多余成员进度；`TestRetryRejectsInvalidMemberProgressBeforeBinding` 覆盖损坏旧任务和 queued 重放。原 HTTP 重试回归保证真正进入成员生成，避免清理提前取消后出现虚假的通过。

本轮验证：上述三个测试连续 50 次通过（`/tmp/thoughtflow-ci-run-members-fixed.log`），定向 race 连续 20 次通过（`/tmp/thoughtflow-ci-run-members-race.log`）。随后与 DS06/DS11 增量修改一起进行的十轮 race 和完整矩阵见下节。新修复的远端同 SHA CI 尚未验证。

## e002559 增量复核：DS06 / DS11

保留上面的 A01–A56 与 DS01–DS12 对应关系。本节仅记录两项增量防护及 CI 越界修复，不将已经关闭的其他十项重新列为缺陷。

- **DS06 / A22、§9**：套级内容警告使用成员 key 与去除显示序号后的单篇问题内容生成语义 ID，并在生成报告 hash 前按 ID 排序；套级校验版本更新为 `document-set-v2`。旧审批不能直接作为新校验版本的证据，需重新校验和处置。`Ready` 对已知风险的完整重验保持严格，不以忽略差异或删除风险消除失败。
- 原单篇校验已有 `finish()` 排序后重新赋号，因此评审所述 S1/S2 仅因 map 顺序交换而失败的路径，本轮没有复现；此次改动消除跨次审批依赖临时序号的隐患，而不把源码推演写成实际故障复现。
- `TestSingleRiskIdentityIgnoresDisplayOrdinal` 验证显示序号变化不改变风险身份，而成员、来源、位置、证据或规则变化不能复用身份。扩展 `TestSourceWarningSurvivesJointReviewAndNeedsRiskDecision`：两条不同位置的未验证来源，50 次重验风险和 `Ready`，实际人工处置、提交、定稿、正式交付，并逐项核对 `review.json` 中身份/位置/处置映射；真实正文或冻结来源改变时旧审批失效，删除已知风险仍拒绝。
- **DS11 / A39、A55**：SSE 事件请求序列与有效任务读取序列分离。只有持久会话绑定核验通过的事件才推进有效任务请求序号；后到但不相关的事件不能使有效读取失效。已合格事件还保留到达序号，拒绝迟到的旧会话绑定查询，不会在将计数后移后重新引入旧查询覆盖新查询。
- 修复前后对照：新增“无关事件不能使新任务失效”的 Node 场景在加载 `e002559` 原始 `web/app.js` 时失败，实际仍为 `old-run`（`/tmp/thoughtflow-ds11-baseline-repro.log`）；修复后同场景通过。对照通过只读加载旧源码进行，没有替换正在验证的工作树文件。
- Node 新增终态无轮询下的有效新任务读取与旧/其他会话任务事件交错，以及同/不同 attempt 的会话绑定查询逆序；保留同 attempt 任务响应逆序、状态版本、轮询交错、同会话新旧任务与 A→B→A 选择保护。此处的会话绑定校验不是多用户权限校验。

定向 Go 测试已连续 3 次通过（`/tmp/thoughtflow-ds-delta-focused.log`）；Node 149/149 通过（`/tmp/thoughtflow-ds-delta-node.log`）。新增/修改的风险校验及任务成员防护、HTTP 重试回归经 `-race -count=10` 通过（`/tmp/thoughtflow-ds-delta-race.log`）：documentset 1.391s、documentsetflow 210.708s、application/service 12.125s；这不是全仓 race。完整 `make ci-check` 八阶段全部通过（`/tmp/thoughtflow-ds-delta-ci.log`）：fmt-check、全量 Go test、build、node-check、node-test、i18n-check、browser-test、e2e-test；Node 149 项、i18n 6 项。浏览器与 API/SSE 使用临时工作区及确定性本地网关；可选 Firefox/Safari 的环境跳过不计为通过。本轮未调用真实外部模型、未操作生产工作区。

上述结果对应包含本节的修复提交；该提交精确 SHA 的远端 Ubuntu/macOS CI 待核实，不能沿用 e002559 的 macOS 通过结果，也不把本地全矩阵通过写成远端验证。

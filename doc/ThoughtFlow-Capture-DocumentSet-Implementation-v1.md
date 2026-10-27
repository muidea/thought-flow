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

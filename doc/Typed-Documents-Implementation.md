# 类型化文档实现与验收记录

日期：2026-10-21。对应 `ThoughtFlow-Typed-Documents-Closure-Plan-v1.md`。
范围调整：用户已明确确认“当前阶段不用考虑兼容性”。因此当前阶段不实现旧格式、旧二进制和旧文档的迁移兼容；A20 的旧资料迁移部分属于验收豁免，不计为测试通过。既有 v1 模板文件保留，但不为其伪造 v2 定稿证据。

## 使用与恢复

1. 在合稿中明确选择设计文档、调研报告或博文 v2，填写模板显示的必要输入；缺少信息可以保存草稿，不能直接定稿。
2. 保存为笔记后，从详情打开“文档检查与定稿”。编辑正文、用途、读者、参数及来源；来源的私有摘录保存在运行目录，不包含在正文导出中。
3. 保存草稿后选择独立语义审查，或显式选择人工审查，再执行检查。硬错误先修改；需要人工判断的问题逐项确认并填写理由。没有模型 Key 时使用人工路径。
4. 选择 proposal 或 ready_for_use 并定稿。BLOCKER 未解决时只能交付提案；每次修改都产生新草稿，原定稿仍然保留。编辑过程中其他客户端更新版本时，保留本地文本，重新打开最新版本后人工合并，不自动覆盖。
5. “导出草稿”固定当前已保存修订，“导出定稿”固定最近定稿。未保存的编辑不属于导出内容。公开地址同样固定修订，并要求已有公开分享开关；定稿本身不会发布文档。

API 客户端先 GET `/api/documents/{id}` 获取 `head.generation`，写请求使用 `If-Match: <generation>`。保存、检查和定稿分别调用 `/revisions`、`/validate`、`/finalize`；检查和定稿传稳定的 `operation_id` 以识别重试。428 表示缺少版本前提，409 表示版本冲突，422 表示未满足交付规则。遇到冲突先读取最新快照并合并，不能盲目替换版本号重发旧正文。

工作区 `documents/{id}` 中的 HEAD 为发布点，Thought Markdown 是当前正文投影。每次正文发布还冻结带独立哈希的 projection.json，保存创建时间、类型、来源、标签及 Original / Extracted Content / Links 等恢复元数据；规范正文仍只读取 body.md。投影失败时重新读取会尝试从不可变修订恢复；已定稿版本仍可通过明确的 final 导出读取。不要手工修改修订、报告、定稿记录或 HEAD。运行目录丢失时恢复对应备份；未知完成状态的生成/审查任务需显式重试，不会自动再次请求模型。普通 Markdown 编辑会在下次读取时导入新草稿，需要重新检查与定稿。

删除笔记先在 HEAD 持久化 deleted_at 并递增 generation，再删除 Markdown，两个步骤与写入使用同一文档锁。公开链接、草稿/定稿导出、报告读取及后续写入均拒绝已删除对象；保留的不可变历史只供内部存储审计。删除中断后重试只补清理，不能被投影恢复或旧请求复活。缺少文件且 HEAD 标为 pending 时，使用冻结投影元数据恢复；删除标记始终优先。

## M0：契约及写入边界

规范正文是 `markdown.ExtractDocumentBody` 适配的 AINotes，随后冻结为独立 `body.md`。正文规范化版本 `utf8-lf-eof-v1` 只处理 CRLF 和文件末尾换行，保留缩进、连续空行及尾部硬换行空格。模板、正文、契约、来源各自计算哈希。稳定 JSON 使用固定结构字段顺序和 Go encoding/json 的 map key 排序。

状态分离：修订是不可变草稿或被定稿记录引用的历史版本；报告状态为 passed / failed / needs_review / error；报告另有 current / stale / unavailable 新鲜度；定稿交付级别为 proposal / ready_for_use。`passed` 只表示声明的检查范围完成，不能证明所有事实正确。无 Key 或主动选择人工审查时，语义检查仍为 not_run，最终记录标为 structure_and_manual。

所有 WriteThought 调用均经过 documentflow 的本地守卫。写入锁覆盖 HEAD 检查、修订发布、Thought 原子写入和投影确认；模型调用在锁外进行。存储层不调用模型。

| 入口 | 接入方式 |
| --- | --- |
| Capture create | 参数、模板快照和来源冻结；实际正文建修订并做结构检查 |
| Capture preview | 候选文档、ContextHash、修订和验证报告绑定；不增加付费审查调用 |
| Capture update/repeat | 原有严格预览门槛保留；合并后正文重新检查；类型化目标使用 generation CAS |
| Capture supplement | 独立新草稿及来源关联，不把补充片段当作父文档的完整验证结果 |
| Compose async | 入队前冻结模板与模型输入；私有输入保存在运行目录 |
| Compose save | 保留持久化 SaveCommand/Thought ID；当前正文另建修订及报告，编辑后不继承旧结构 |
| Thought PATCH / ApplyDraftInternal | 同一底层守卫；公开类型化编辑要求 If-Match，内部归档也带目标 generation |
| Refiner / Expander | 标签、摘要、索引状态不改变正文修订；参与验证的标题或正文变化另建修订 |
| Search / TopicStore | 同一 WriteThought 守卫；搜索订阅 patched/expanded/deleted，文档投影变化通知索引 |
| 外部 Markdown 编辑 | 读取时比较权威修订指纹并导入新草稿；手写 valid 无效 |
| 导出 / 公开访问 | 从明确修订读取原始 body.md；final 必须属于已发布的定稿链，公开地址另受现有开关控制 |

普通笔记保持原来的轻量存储和处理方式，不强制语义审查或定稿。

## M1：存储、最终正文检查与界面

- `internal/pkg/documentstore`：不可变修订、报告、定稿链、HEAD generation、同文件系统 staging/rename、文件与目录 fsync、投影 pending 标记。未发布完整目录不成为当前版本；未发布的定稿记录不能导出为 final。
- `internal/pkg/documentvalidation`：固定 goldmark v1.7.13，按 AST 验证实际正文；关闭 HTML 解析以对应前端 html:false；围栏关闭由真实 block parser 标记。代码中的标题、引用及占位符不被当作正文结构。
- `internal/pkg/documentflow`：统一协调、CAS、结构报告、独立语义审查、人工确认、定稿操作幂等、显式修复范围及预算。迟到报告只属于原修订。
- 类型化 Thought 使用 sections-v2 明确边界；正文出现 Original / Extracted Content / AI Notes / Links 或代码围栏时不截断。边界损坏报错，不用截断结果重新证明原文完整。
- 笔记详情的“文档检查与定稿”提供正文/参数编辑、四组问题与定位、逐项确认理由、独立保存/检查/定稿，以及当前草稿和最近定稿的不同导出地址。编辑 epoch 阻止晚到响应抹掉新输入。合稿创建表单显示 Profile 的必要输入。

## M2 / M3：类型规则、审查与修复

三个内置类型使用 v2 Profile，必需输入和 AST 章节映射与模板一起冻结。未知 schema、未知强制规则明确失败。

- 设计：G / D / T 目标—决策—验收行，结构化 requirements、constraints、terms，一致的失败/恢复说明，适用时的实施前置条件和回滚；BLOCKER 或 unresolved_blockers 只允许提案交付。
- 调研：Q / F / C 与 S 引用的可追溯行；来源定位、来源版本/哈希、支持性和比较口径的独立人工/语义审查。引用存在与原文已核验分开。
- 博文：opinion / tutorial / case 分支；观点不要求部署章节；教程要求环境、输入、步骤、预期输出、验证和失败处理，未执行示例要披露；用户已验证示例可显式声明 examples_execution=user_verified；案例明确真实、匿名化或假设性质。
- Assumptions 明确渲染到正文。生成与审查分开调用，审查不具备工具和发布能力。非法 JSON、缺少问题位置/证据/依据、超时均不伪装通过；允许显式改用结构检查与人工审查。
- 修复仅接受所选问题和允许章节；AST 范围外字节必须相同，结果另建修订并重新检查，最多 2 次（可配置）。界面保留修改前后内容供复核，不自动定稿。

## 配置、隐私与运行约束

配置节 `[documents]`：enabled、families（逗号分隔 design,research,article）、semantic_review、allow_manual、review_timeout_seconds（默认 45）、concurrency（默认 2）、max_repair_attempts（默认 2）。硬上限：正文 1 MiB、来源 100 条、单份报告 1 MiB、单条私有证据 64 KiB；正文另受冻结 Profile 的字数范围约束。

语义审查和修复沿用已有 LLM 供应商、Key、模型及传输策略；不会访问引用 URL，不启用 Reader，不执行文档中的命令。无 Key 不发送模型请求。诊断中的凭据 URL、敏感查询参数和认证片段脱敏。来源清单拒绝带凭据的定位信息。

Git 使用明确文件清单：已引用修订的 body/contract/source-manifest/profile/manifest/projection、head（含删除标记）、报告和定稿记录。运行目录中的 document-evidence、compose-inputs、任务及模型中间数据不在清单中。导出只返回正文，不包含 Thought 包装、内部报告和私有摘录。

私有证据为运行目录内 0600 文件。需要日后复核时，必须同时备份内容目录和运行目录的 document-evidence / compose-inputs；证据丢失显示 unavailable，不继续声称可复核。来源更新只显示 changed，不改写历史快照。

仅支持同一工作区一个 ThoughtFlow 写进程，不提供跨进程协同协议。外部编辑器不参加进程内锁；读取前检查能发现已发生的修改，不能保证阻止外部编辑器在随后任意时刻覆盖文件。关闭新功能不会把草稿变为定稿，也不影响读取已经定稿的历史正文。

验证任务有超时、并发上限、jobstore 状态和退出取消；重启对未完成审查标为 canceled，不自动重放未知收费调用。Compose 已完成草稿恢复为成功，未开始任务使用冻结输入恢复，正在执行且完成情况不明的调用转为显式重试。生产 Compose Teardown 已接入取消与 worker 等待。修复模型调用结束后释放并发额度，再进行独立复验，同时继续跟踪 worker，避免并发额度为 1 时相互等待；功能或类型关闭时，检查与修复在调用供应商前拒绝。已完成验证 operation_id 重试返回原报告；定稿 operation_id 重试返回同一记录，不接受变更请求。

## 验收映射

| 编号 | 实际证据 |
| --- | --- |
| A01 | 三类型验证/定稿/逐字导出：documentflow、HTTP 单测及真实 API e2e |
| A02–A04 | documentvalidation：缺章节、占位词、输入、重复/未知/空引用、代码围栏与作用域标题 |
| A05–A06 | documentflow：旧报告拒绝、迟到审查不更新新版本 |
| A07 | 生成输入持久化与哈希检查、冻结 Profile 不依赖运行时 Registry；来源/契约更换改变身份 |
| A08–A09 | 原确认不适用于新修订；并发编辑/定稿只允许一个 CAS 发布 |
| A10 | 保留并运行既有 Compose Capture 成功后 MarkSaved 失败重试用例；不重写已保存 Thought |
| A11 | documentstore 注入 revision_written 故障，重启只暴露原 HEAD |
| A12 | HEAD pending 投影恢复；导出独立于 Thought 投影；未发布定稿链节点不可见 |
| A13 | 外部编辑导入新修订；手写 valid 标记无效 |
| A14 | 无 Key、超时、非法审查响应、人工审查替代路径；模型测试使用本地 HTTP fixture |
| A15 | G/D/T、Q/F/C/S、约束冲突、术语与必需输入检查及可定位语义问题 |
| A16 | opinion / tutorial 分支与未运行披露，不互套部署要求 |
| A17 | Reader 原有零 fallback 请求测试继续运行；凭据诊断脱敏与私有证据不进入 Git/导出 |
| A18–A19 | 原有 Git 删除、无关暂存保护及素材响应交错回归测试继续运行 |
| A20 | 旧资料迁移按本轮范围调整排除；普通笔记测试继续运行，不自动标为验证通过 |
| A21–A22 | final/draft 明确模式、公开开关与历史版本固定；保留分节词及 Markdown 空白往返（含合稿 UI → 保存命令 → 草稿落盘 → Thought） |
| A23 | 原始来源有变更时报告 changed，历史依据不改写；私有证据不可用时 unavailable |
| A24 | 审查与 Compose 退出取消、冻结输入恢复、已完成 operation 重试、未知完成状态不自动重放；并发额度为 1 时修复复验可完成；定稿来源 I/O 在文档锁外执行 |

故障测试验证提交边界与文件系统恢复行为，没有执行真实断电或跨文件系统断电试验。语义测试使用本地模型协议 fixture，没有把真实供应商回答当成事实保证。浏览器验证以当前可用 Chrome 为准，Firefox/Safari 未运行的环境会明确跳过。

## 本轮验证结果

2026-10-21，最终代码使用缓存中的 Go 1.26.7 工具链执行（项目声明 Go 1.24）；通过 Makefile 准备 DuckDB 链接参数。

- `make ci-check`：全部目标通过，包含格式、全量 Go 测试、构建、JS 语法、119 项 Node 组件测试、6 项 i18n 测试、完整浏览器测试和 30 项真实 API/SSE 测试。浏览器执行当前可用的 Chrome，Firefox / Safari 因环境缺失跳过。
- `go test -race ./internal/pkg/document... ./internal/pkg/markdown ./internal/pkg/composedraft ./internal/modules/capture/biz ./internal/modules/compose/biz`：通过。
- `go test -race -run 'TestDocument|TestHardErrors|TestEvidence' ./internal/modules/application/thoughtflow/service`：新增文档 HTTP 范围通过。
- `git diff --check`：通过。

曾尝试全量 application HTTP service 的 race 测试，现有 vendored DuckDB 驱动在 `vector_getters.go:187` 触发 `checkptr: misaligned pointer conversion` 后中止。因此不能声称全量 HTTP race 通过；未关闭 checkptr 来掩盖该错误。普通全量 Go 测试和上述独立文档 HTTP race 均通过。

未执行 Firefox/Safari、真实断电、真实外部 LLM 质量/准确率评估或多进程协同测试；这些结果不由单测或本地模型 fixture 代替。当前仅支持单工作区单写进程，旧资料迁移兼容按用户要求排除。


## 10bde94 评审问题修复

| 问题 | 实现与专项回归 |
| --- | --- |
| 删除后公开定稿仍可读 | HEAD 删除标记先于文件清理发布；公开链接及 API 读写/导出返回 404。HTTP 回归覆盖公开前后行为，协调层回归覆盖删除中断、重启、旧定稿请求重试、后台迟到报告和清理重试。 |
| 标题引号/反斜杠累积 | 顶层 front matter 标量统一使用 parseScalar 反转义；测试 user_title / extracted_title 连续读取 5 次后标题、正文、修订、generation 不变，并用原 generation 完成检查和定稿。辅助分节换行也在冻结前对齐编码，避免 Links 的 EOF 换行制造伪修订。 |
| 正文引用地址绕过检查 | 引用同时提取裸 URL、Markdown 链接与自动链接的真实目标，逐一检查安全性及与冻结 Locator 的对应关系；不再用清单地址替代正文地址。回归含错误地址、凭据、敏感查询参数、空地址及诊断脱敏。 |
| 段首引用误当定义 | 普通段落中的 [S1] 记为引用使用；定义限于参考章节或明确的参考列表条目。测试中文/英文段首引用、普通引用列表项，同时保留真实重复定义的硬错误检查。 |
| 首次投影缺失不能恢复 | 新修订在 HEAD 发布前持久化并校验 projection.json；故障注入在 head_published 处打断首次创建，重启直接 ReadThought 即恢复原元数据及正文，不产生额外修订。 |
| 可确认语义错误被 failed 拒绝 | failed 只表示不可人工覆盖的硬错误；可确认语义 error 的总体及语义运行状态为 needs_review，结构运行仍为 passed。无理由拒绝，逐项确认后以 structure_and_manual 定稿；结构硬错误继续阻塞。 |

确定性验证器版本提升为 final-body-ast-v2.2，旧报告不能直接用于新规则下的定稿；历史定稿仍保留原记录与正文。新增专项测试位于 documentflow/review_regression_test.go、documentvalidation/references_test.go 和 HTTP documents_test.go，真实 API e2e 补充三类型删除后的导出/写入撤销。

本轮执行结果：

- `make ci-check` 通过：全量 Go、构建、JS 语法、119 项 Node、6 项 i18n、可用 Chrome 浏览器，以及含三类型删除撤销断言的 30 项 API/SSE 测试。
- `go test -race ./internal/pkg/document... ./internal/pkg/markdown ./internal/modules/capture/biz ./internal/modules/compose/biz` 通过；强化首次恢复元数据断言后另跑 `go test -race ./internal/pkg/documentflow`，通过。
- 新增 HTTP 文档范围 `go test -race -run 'TestDocument|TestHardErrors|TestEvidence' ./internal/modules/application/thoughtflow/service` 通过。
- 更新前端定稿门槛断言后补跑 `make node-check node-test`，119 项全部通过。
- `git diff --check` 通过。全量 HTTP race 的既有 DuckDB checkptr 中止限制沿用前节记录，不计为通过。

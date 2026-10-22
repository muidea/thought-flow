# ThoughtFlow 全功能审计修复记录

评审输入：[82b91d5 全功能审计](ThoughtFlow-Full-Function-Audit-82b91d5-2026-10-22.md)。本记录覆盖其中 CP-01..05、STG-01..11、D1..5、W1..10 共 31 个编号问题，保留原报告不变。

代码基线为 `82b91d5`，修复与本记录随本次提交保存。以下结果来自提交前对应工作树的开发侧本地验证，不代表已在新 SHA 上完成独立复核或远端 CI。当前阶段按用户“当前阶段不用考虑兼容性”的要求，不实现旧资料迁移 A20。

## 1. 采集、加工、合稿

| 编号 | 修复后的行为与持久化结果 | 回归证据 |
|---|---|---|
| CP-01 | URL 会话归档命令保留实际 URL，能够通过真实 Capture 校验并写入 Markdown | `capture/biz/scratchpad_test.go`：`TestURLCaptureCommandPassesRealCaptureValidation` |
| CP-02 | 网页正文进入提炼输入，保留用户旁注；`last_refined_input_hash` 独立记录实际输入，编辑后重新提炼 | `refiner/biz/full_audit_test.go`：本地 HTTP 页面独有事实进入真实本地摘要，验证旁注及缓存失效 |
| CP-03 | 本地 Thought 读取正文；其他来源使用实际提供的 Snippet，并明确为部分摘录；只有标识/元数据的来源不可用，不能包装成伪正文；Web 搜索来源传入片段 | `compose/biz/service_test.go`：不同来源片段中的独有事实进入合稿输入；原保存幂等、冻结来源测试继续保留 |
| CP-04 | Expander 各阶段共享截止时间，单个失败不取消其他分支，仅更新成功阶段字段 | `expander/biz/full_audit_test.go`：遵守 context 的延迟 provider 在搜索失败后仍完成，旧失败阶段数据保留 |
| CP-05 | 每资源同类任务合并；独立 job lease、心跳和持有者校验；忙时等待，取消后保留可恢复任务；模块 Run 恢复、Teardown 取消并等待退出 | `jobstore/runner_test.go`、`thoughtlock/lease_test.go`、refiner/expander/search 服务测试：合并、锁释放后执行、心跳、取消、同 ID 恢复、最后一条索引事件不遗漏 |

后台 Job 文件改用临时文件、Sync、rename，并在同一工作区 Store 实例间共享锁。Refiner/Expander 使用 `markdown.UpdateThought` 合并其负责的字段，避免陈旧全量快照覆盖专题反链。Repeat 归档也走 Thought 编辑锁。

## 2. 专题、检索、Git

| 编号 | 修复后的行为与持久化结果 | 回归证据 |
|---|---|---|
| STG-01 | 专题事件只发送实际 topic.yaml、index.md、membership、approval 及反链文件路径，包括已删除的路径 | `topic/biz/full_audit_test.go`：真实 EventHub → debounce → Git HEAD，成员/提案/删除可提交，私有候选不入库 |
| STG-02 | Compose 生命周期只刷新候选；正式专题正文以当前内容为基线保留，不从初始模板重建 | 同一集成测试：人工确认正文经过无关 Compose 事件后保持不变 |
| STG-03 | 同工作区同专题更新共享互斥；追加前重读最新状态；提案接受校验正文基线，反链按最新 Thought 合并 | `topicstore/concurrency_test.go`：8 个陈旧快照并发添加，所有成员、人工正文及反链保留；原 stale proposal 测试 |
| STG-04 | 创建、改规则、手工刷新枚举历史会话并更新候选，正式收录仍遵守 AutoWeave | `topic/biz/full_audit_test.go`：`TestHistoricalCandidatesRefreshAndUniqueLineage` |
| STG-05 | 根据 SourceThoughtID lineage 为会话生成唯一候选来源 | 同上：普通会话与 reopen 会话不重复 |
| STG-06 | thought.deleted 清除成员关系和失效 membership，保留作者正文并标明来源删除需审阅，发布具体 Git 删除路径 | 专题集成测试及 Store 测试 |
| STG-07 | 启动自动调度重建，状态反映 rebuilding/failed；读取全部 Markdown 并完成向量计算后，以事务替换文本与向量 | `search/biz/full_audit_test.go`：恢复与失败保留旧索引；`searchdb/full_audit_test.go`：替换失败回滚 |
| STG-08 | topic.updated 仅更新成员专题集合；重建复用匹配输入 hash、模型、维度的缓存；调度合并任务 | Search 完整审计测试：topic 更新不增加 embedding 调用 |
| STG-09 | 提炼不顺带计算索引向量；统一由独立 embedding provider 处理规范化文本；变更后失效旧 JSON/ARRAY 向量，专题缓存也验证内容版本和维度 | Search 四种 Key 组合使用真实 HTTP embedding endpoint；PATCH/缓存断言；SearchDB 旧向量失效测试 |
| STG-10 | HTTP 接通 topic_id；标签和专题按完整集合成员匹配，%/_ 不作为通配符 | HTTP service、API e2e 及 `TestFiltersMatchWholeElementsIncludingWildcards` |
| STG-11 | 运行态 `git-pending.json` 保存 queued/in-flight 路径，成功才确认；失败退避重试并跨重启恢复；冲突在暂存前拒绝；停机后到达的事件仍持久化 | `git_sync/biz/recovery_test.go`：失败后重启无新编辑仍补交，并发新路径不被旧成功回执清除；原删除/暂存隔离测试 |

索引成功后通过受锁的 Markdown 字段合并写回 `index_status`，不改变笔记 UpdatedAt。索引重建本身仍以数据库事务为发布点。

本地 API e2e 另发现了持久化 HNSW 覆盖向量的事务更新可导致当前 DuckDB/VSS 扩展发生 SIGSEGV。已将扩展建索引操作与写入串行，并在向量变更事务前移除可重建 HNSW 索引，提交后由后续查询按需重建；JSON/ARRAY 数据不因移除加速索引而丢失。`TestVectorIndexesSurviveTransactionalUpdatesAndReopen` 覆盖反复读写、重启、全库替换及删除。HNSW 不可用时仍使用已有 ARRAY/JSON 评分路径。

## 3. 类型化文档

| 编号 | 修复后的行为与持久化结果 | 回归证据 |
|---|---|---|
| D1 | 单独遍历完整正文 AST，标题、列表等位置的 Link/Image/AutoLink 都检查目的地址；代码示例仍不作为实际链接；诊断脱敏 | `documentvalidation/full_audit_test.go`：安全/凭据/敏感 query 节点矩阵（含 H1/H3）；`documentflow/full_audit_test.go`：不安全正文不能定稿 |
| D2 | Markdown 解析与投影恢复均要求内含 ID 与请求 ID 一致，错误在同步 journal 前返回 | `TestExternalIdentityMismatchCannotPublishAnotherDocument`：A 伪造 B ID，并发读 B 后两者 HEAD 不变 |
| D3 | scope 检查与模型提示使用冻结 Profile 的 heading level 及共享解析语义 | `repair_scope_test.go`：H2/H3/H6 范围；`TestRepairPublishesNewRevisionForCustomHeadingLevels`：H3/H6 真实 Repair 发布新修订并重新校验，邻接修改拒绝且 HEAD 不动 |
| D4 | 临时文件写入同步后独占发布，不覆盖同版本已有文件；稳定返回 ErrProfileConflict | `documentprofile/publish_concurrent_test.go`：两个 Registry 并发发布，唯一成功，重启后 hash 稳定 |
| D5 | Candidate 发布前检查来源凭据及 enabled/family/关闭状态，拒绝时不落 journal | `TestCandidateRejectsSensitiveSourcesBeforePublication`、`TestCandidateRespectsClosedDisabledAndFamilyGates` |

删除撤销公开定稿、标题往返稳定性、首次投影恢复、引用识别/URL 括号、人工语义确认、frozen CAS 等已有后端和 Web 回归均保留。

## 4. Web、事件、隐私和说明

| 编号 | 修复后的行为与持久化结果 | 回归证据 |
|---|---|---|
| W1 | Capture 每次选择和写操作更新 epoch；目标 ID 在 await 前固定；旧响应/错误不再覆盖新会话；预览、历史、消息、提交及启动恢复均检查选择有效性 | `web/app.test.js`：A→B→A 的旧历史/消息、预览切换；另补 Thought/Compose 迟到加载与本地编辑保护 |
| W2 | 搜索每次请求及清空均使旧 epoch 失效 | Node 测试：倒序响应及清空后旧结果不复活 |
| W3 | Reader enabled 与 configured 分开，默认无 Key Reader 的 URL 外发明确披露；设置实际加载报告，Capture/Compose 有双语提示，明确原站/GitHub 仍可联网 | HTTP privacy Key/开关矩阵、真实 API privacy 测试、Chrome UI 冒烟；浏览器 fixture 补上真实接口对应数据 |
| W4 | api Error 保留 code、status、requestID，锁错误处理可读取原错误码 | Node 409 结构化错误测试，保留锁重试测试 |
| W5 | SSE 白名单包含 thought.patched、Compose 创建/保存/删除；前端处理删除并使缓存失效 | `web/events.e2e.test.js`：真实二进制 HTTP 操作 → SSE 帧，校验事件与实际资源身份 |
| W6 | 远程标签锁按接收页本地时间记录 TTL，心跳刷新本地接收时刻 | `session-lock.test.js`：不同时间基准的双 VM + 共享 BroadcastChannel |
| W7 | 新建、切换、重开会话在更换 ID 前释放旧锁 | Node 新建释放旧 thought/session 参数断言及原锁测试 |
| W8 | 发布与取消关闭 channel 使用同一互斥，保持非阻塞投递 | `eventstream/concurrency_test.go`：并发发布/取消压力及 race |
| W9 | SSE ticker 使用配置的正数 heartbeat，非法值回退默认值 | HTTP service 测试实际短间隔 heartbeat flush |
| W10 | README/使用配置说明纠正 CWD 路径、DuckDB/CGO 必需、keyword HTTP 与内部语义能力、专题 API/Web、Git 本地提交及无 Key 的联网边界 | 文档与实际入口核对；保留现有默认配置/目录测试 |

Workspace 幂等维护私有运行态的 `.gitignore`，保留用户已有规则；`workspace/ignore_test.go` 以真实 Git 验证私有候选/会话/自定义运行目录（含空格及方括号）忽略，正式文档仍可跟踪。

## 5. 验证账本

验证环境：Linux，`GOTOOLCHAIN=go1.24.13 GOCACHE=/tmp/thoughtflow-go-build`，使用 Makefile 的 DuckDB 链接设置。

- 最终完整 `make ci-check` 通过：fmt、全量 Go 测试、build、JavaScript 语法、Node、i18n、browser 和 API/SSE e2e 各阶段均成功。另行补验运行 context 取消与 Git 停机后迟到事件的回归，相关测试及 race 通过。
- Node：127 项通过；i18n：6 项通过；JavaScript 语法检查通过。
- Chrome 桌面/移动及交互浏览器套件：19 项通过，2 项跳过（未安装 Playwright Firefox；Linux 无 Safari/WebKit 自动化），没有将跳过记成通过。
- 真实 API/SSE e2e：31 项通过；覆盖补充事件合同后，已实际运行独立二进制服务。
- scoped race：jobstore、thoughtlock、eventstream、topicstore、documentflow、documentprofile、documentvalidation、workspace、git_sync/biz 通过。全量 HTTP/DuckDB race 未重跑，不声称覆盖已有 DuckDB checkptr 问题。
- `git diff --check` 通过。提交后应在固定新 SHA 上由 CI/评审独立复跑；对应修复提交为包含本记录的提交。

## 6. 范围与仍需单独处理的边界

这些编号修复不等于所有产品功能已经验收，也不将报告的“其他建议”自动视为已交付能力：

- Scratchpad 首次 Commit 的完整持久化幂等 reservation 仍需单独设计；本次修复 Repeat 锁路径，不宣称首次归档双击/失败重试整体幂等。
- Compose 非 Thought 来源接受显式摘录并标注部分内容；尚未实现所有资源类型的全文 resolver。只有部分来源可用时沿用跳过不可用来源的行为。
- Compose 删除不等于已具备逐任务取消远端模型请求。v2 之外的来源冻结能力不扩称为完整支持。
- 当前支持单写入进程。进程内共享锁不提供跨进程事务，也不承诺多客户端同时直接写 Markdown 的完整一致性。
- 语义/hybrid 仍是内部能力；公开 HTTP 搜索入口固定 keyword。专题提案有 API，尚无完整 Web 审核入口。Git 是本地提交，未增加远端 push/pull/冲突合并。
- Origin/Host/CSRF、多用户鉴权、默认 Reader 外部协议兼容专项未列为本轮已通过验收；公开访问开关、备份及部署边界见使用文档。
- 本次未推送远端，不能把基线 SHA 的旧 CI 记录作为本轮修复的验收结果。

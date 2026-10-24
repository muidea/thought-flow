# Capture 对话归档收口实施记录

日期：2026-10-23。对应 [收口方案 v1](ThoughtFlow-Capture-Archive-Closure-Plan-v1.md)。

实施对象为 `f64c68728f9dbc5e9793b5b10265f8c09a456696` 后的当前工作树，包含先前尚未提交的回答协议及 Provider 参数处理修改。本文不能作为某个尚未产生的提交 SHA 的远端 CI 证明。历史数据兼容不作为本阶段目标；没有操作真实会话、调用外部模型或重启用户服务。

## 代码落实

| 项目 | 实现 |
| --- | --- |
| C01 | Scratchpad 原子 Update、session/input generation、字段 CAS；模型在事务外执行，预览发布同时核对输入代次、操作 ID 与 Profile hash。旧预览不能修改候选 HEAD。保存期间的晚输入保留；删除不会抹掉正在保存的恢复命令。会话文件通过临时文件、fsync、rename 持久化。 |
| C02 | 会话级保存锁与持久化 SaveCommand，冻结 Thought ID、新建/更新方式、目标快照、正文、参数、Profile 和来源。预留 ID 仅供受信内部命令使用。更新在写入锁内检查目标快照；失败重试核对冻结内容，保留后续人工编辑。补充反链去重。完成操作先写 outbox，事件/Git 使用稳定操作身份并可重放；SSE 历史去重，Git 继续使用其持久队列和路径集合合并。 |
| C03 | Provider 的真实请求包含候选 Profile 的 inputs、required/default、规则和 contract hash；限制候选数及总字节，已选 Profile 优先保留。请求返回的契约身份由请求侧 descriptor 绑定，不信任模型自报 hash。 |
| C04 | 后端计算实际缺项和 readiness；v2 必填参数需确认。缺项/缺来源在正文生成前阻塞。字段 PATCH 支持确认、清空、未知键拒绝和 If-Match；用户确认值及确认来源清单不被模型覆盖；修改输入后撤销旧代次的自动归档意图，先重新预览。换模板重新校验输入。 |
| C05 | 有 ID 的 contradiction/limitation/diagnostic，保存状态、字段/消息来源和解决理由；只有明确的解决操作能解除矛盾。重复模型报告不能重开已解决项。局限和运行诊断不直接当作事实矛盾。 |
| C06 | 实际渲染 Markdown 经最终 AST 校验，结构问题进入同一有界 repair；v2 不以本地占位 fallback 冒充有效交付。缺证据/确认返回补充入口，不要求模型编造。保存使用预览冻结的参数、Profile、来源。 |
| C07 | 独立保存私有草稿接口，真实物化 Markdown，返回 draft_thought_id；不清空对话，不设置 committed。候选与 Thought 使用同一 ID；补齐归档清除草稿标记。安全检查持续生效，私有草稿不能公开、正式导出或定稿，通用 refiner 不覆写它。 |
| C08 | GET 只读；POST 接受持久生成/自动归档命令。服务端执行 preview→commit；模型识别归档意图后也可在后端排队。恢复扫描、任务取消及退出等待接入现有 BackgroundRoutine 生命周期。前端提供缺项表单、来源、问题解决、草稿和重检；只允许 valid 预览严格提交；按会话选择 epoch 隔离迟到响应。轮询/SSE 不重新生成，未提交的表单输入及其原始 generation 保留。 |

事件交付采用可重放的至少一次语义，不声称跨文件、EventHub 和 Git 的分布式恰好一次事务。相同保存请求不会生成第二份 Thought；重放用稳定事件 ID、SSE 去重及下游幂等更新收敛。目标被后来编辑时返回冲突，保留字节，不自动覆盖或重新创建副本。

## API 与操作状态

- `POST /api/capture/sessions/:id/archive/preview`：显式生成，`commit_after_preview: true` 表示本次通过校验后由服务端继续归档；运行时返回 202。
- `GET /api/capture/sessions/:id/archive/preview`：只读取 preview、operation、persisted 和 scratchpad；不调用模型、不推进 HEAD。
- `PATCH /api/capture/sessions/:id/parameters`：`If-Match: <session_generation>`，body 为 `values`、`resolutions`，可选 `source_links`。空值明确清空；来源链接的输入不表示已核验。
- `POST /api/capture/sessions/:id/archive/draft`：显式保存未完成草稿；不会授予公开/定稿权限。
- 原严格 `POST .../archive` 门禁继续保留，无效/过期预览不能归档。

操作状态包括 preparing、needs_input、blocked、ready、saving、draft_saved、saved、failed、interrupted、conflicted。preparing/saving 和停机中断的任务可由恢复扫描继续；已冻结保存命令复用原 ID。目标冲突不由启动扫描盲目覆盖。GET 返回的是当前状态，后台输入变化可能使刚生成的预览失效；调用方需读取状态并显式重新预览。

这些接口调整不提供旧客户端兼容桥。当时 JSON 容错为显式开关；2026-10-24 用户已要求内置默认启用并移除开关，兼容通道增加有界临时故障恢复，详情见 [后续核对](ThoughtFlow-Capture-Archive-Followup-2026-10-24.md)。原有解析及业务校验保持。

## 专项验收索引

以下为实际自动化覆盖点，不代表真实模型知识质量或真实断电实验。

| 用例 | 回归证据 |
| --- | --- |
| T01 | HTTP handler + 磁盘 Store 阻塞生成期间追加消息，重载仍保留，旧预览不发布。 |
| T02 | 参数/Profile/策略/目标及删除变更拒绝旧预览，旧结果不能产生 HEAD。 |
| T03 | 新预览先发布，再交付旧预览，HEAD revision/generation 不变；上下文更新核对排队时的输入代次；确认参数后再启动的旧任务直接丢弃，旧错误反馈也不能改动新代次。 |
| T04 | 8 个并发请求使用真实临时工作区，只有一份 Markdown 和一次首次事件集合；另保留阻塞写入器交错测试。 |
| T05 | MarkCommitted 和最终完成记录分别注入故障；重启重试同 ID；人工编辑存在时返回冲突并保留。 |
| T06 | 保存冻结 A 时追加 B，保存完成和重载后 B 仍在，LastActive 可恢复。 |
| T07 | 保存命令重载、revision_written/head_published、提交标记、完成记录、补充反链的故障回归；没有模拟物理断电。 |
| T08 | 可控 HTTP Provider 真实 payload，研究/设计模板 inputs 与 hash 均进入请求。 |
| T09 | ready+缺参数仍由后端阻塞，生成器调用数为零；真实必填 key 返回。 |
| T10 | 已确认值不被模型覆盖；未知键、清空与过期 generation 回归；切换模板清空并重新检查参数。 |
| T11 | 未解决矛盾保留；有理由解决后重复报告不重开；局限/诊断非业务阻塞。 |
| T12 | 缺 Q/F/C 的最终 AST 问题进入下一次 repair，次数有界，不能错误标 valid。 |
| T13 | 缺来源先阻塞；演练来源保持 unverified；草稿不伪造证据。 |
| T14 | 比较落盘正文、参数、Profile hash、SourceManifest；使用同一对象的不可变修订。 |
| T15 | 无效类型化候选物化为真实草稿；重启可读，敏感链接拒绝；正式导出/定稿拒绝。HTTP 对照回归开启公开分享，验证普通内容可读而私有草稿仍返回 not_final；API e2e 另验证草稿状态与归档身份。 |
| T16 | 草稿补齐归档复用 ID；真实临时工作区只有一份投影，投影及不可变修订中的草稿标记同时清除，并可按人工确认规则定稿为 proposal。 |
| T17 | Chromium 实际点击预览/草稿、刷新、确认输入和归档；中英文词条完整性测试。 |
| T18 | 已接受命令丢弃请求 context 和进程内队列，再加载磁盘恢复完成；停机取消并等待；GET 不产生新操作。浏览器草稿刷新与 API/SSE 配合验证恢复显示。 |
| T19 | 现有会话切换/迟到请求回归继续保留；新轮询、参数和草稿动作使用相同 selection epoch。 |
| T20 | 真实可控 Provider HTTP 回归覆盖 JSON 严格/显式容错/非法结果/混合功能/鉴权/限流/服务故障；详情见回答收口记录。 |
| T21 | `TestISA95ClosureNeedsInputsThenDraftThenSameThoughtArchive`：固定脱敏材料，从缺项阻塞到私有草稿，再确认参数/来源并同 ID 归档；来源始终未核验，不声称完成真实 ISA-95 事实核验。 |

后端主要测试位于 `archive_closure_test.go`、`archive_regression_test.go` 和 HTTP `capture_archive_regression_test.go`；Provider、事件及前端回归分别在所属包和 web 测试中。旧 `doc/repros/capture-archive-audit-2026-10-23.patch` 是修复前基线的缺陷复现工件，不是修复后应当通过的测试补丁。

## 验证结果

- 专项 Go 回归、临时文件故障注入与任务恢复测试通过。
- Node 组件 144/144、i18n 6/6 与 JS 语法检查通过。
- Go 1.24.13 下 `make ci-check` 全部通过：fmt、Go 全量测试、构建、JS 语法、Node、i18n、浏览器、API/SSE e2e（39/39）。另补充开启公开分享的 HTTP 对照回归通过。
- 专项 race 通过 Capture、HTTP 归档、Scratchpad、Provider 契约及事件回归，最后的排队上下文/自动授权保护与 ISA-95 草稿转归档演练也通过 race。
- 本机 Chromium 执行了实际交互；缺少可执行文件的其他浏览器仍按测试框架标为 skip，不计为实际浏览器验证。
- 先前完整矩阵曾分别出现浏览器连续阅读超时及后台更新使预览失效的 e2e 失败。保留这些记录；没有通过跳过断言或放宽超时宣布通过。
- 本文验收基于提交前工作树，没有发起推送，也没有新的 Ubuntu/macOS 远端 CI 结果。最终提交的远端双平台 CI 仍需在推送后验证。

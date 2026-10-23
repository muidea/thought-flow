# 专题工作区 R11 / R16 恢复修复

日期：2026-10-23。修复基线：`16deb908d4eaef5e17af84e4f332a687c0eac75c`。本记录对应后续工作区修改；最终评审与远端 CI 必须绑定实际提交 SHA，不能沿用基线提交的绿色结果。

## R11：歧义诊断保留权威元数据

重复身份扫描得到的候选保存到私有 `.content-registry/private/ambiguities/<id>.json`，保留冲突前权威快照。已有位置记录只改变隔离状态和推进 generation，不再被扫描得到的 `proposed` 默认记录覆盖：owner、placement、资源绑定引用、检索策略及其他已有字段均保留。重复诊断不以任意候选取代原件。

消除复制件后，仅当唯一剩余路径仍为原权威路径，才尝试恢复。恢复前校验正文身份、HEAD、工作/定稿不可变修订链各文件摘要、当前绑定清单摘要与资产文件摘要、定稿引用。无 HEAD 但仍有正式元数据或 journal 时拒绝恢复。校验不临时发布 active，也不通过会拒绝 ambiguous 的正常读取接口绕过检查。私有资源根使用独立锁，避免首次初始化持有 registry 互斥锁时再次获取它。

恢复推进 generation 并使位置 epoch 失效，不重置检索策略。只剩非原路径、没有可信冲突前权威记录，或验证失败时保持隔离；需要显式处理，不能通过单候选自动接管。原件与复制件都仍存在时继续报歧义。

回归使用真实临时工作区：正式文档上传附件后迁回全局，设置非默认 include 策略，删除任何 storage.snapshot，再将原件复制到同一全局树的 09 或 11 目录。两种确定扫描顺序均始终删复制件、留原件；核对完整 authority 元数据、generation、HEAD、正文与 revision、绑定摘要，以及 `ReadResource` 返回的真实附件字节。另覆盖只留复制件、破坏绑定、破坏修订正文及删除 HEAD，均拒绝自动恢复。旧测试已修正为始终保留原件。

## R16：首次识别终止冲突不阻断启动

`RecoverImports` 捕获冲突后重新读取持久 import state 与 operation。仅当二者均确认同一操作/专题已终止为 conflicted 时继续下一任务。错误仍可通过现有操作/导入查询 API 查看。操作记录写入失败、import state 写入失败、只有内存冲突而终止状态未落盘、损坏状态等均继续返回错误，不把所有 ErrConflict 无条件吞掉。

新增回归不先调用 RetryImport：同时留下两个 validated 导入，创建同名外来专题及人工综述，直接首次恢复。验证冲突任务终止、外来字节不变、后续正常任务完成、无关笔记可读写、再次恢复幂等。故障注入另覆盖 operation/state 持久化失败和损坏状态。

API e2e 还启动真实 thoughtflow 进程：用 HTTP 创建的两个空专题字节构造 durable validated checkpoint，另在目标创建真实同名外来专题，停机后放置 checkpoint，再首次启动。直接检查 ready、冲突任务可查询、后续合法任务 completed、人工综述保留、HTTP 新建笔记成功。测试只操作临时工作区，不操作用户服务或工作区。

## 验证证据

- 在独立临时 worktree 固定基线 `16deb908`，只放入新增 Go 回归并运行：两个原始缺陷均实际失败。R11 确定性检测到 generation/策略/绑定被替换；R16 首次恢复返回 generation_conflict。临时 worktree 已删除。
- 修复后的针对性 Go 测试与 race 已通过；真实进程首次启动 API 回归已通过。
- `make ci-check` 全部通过：格式、全量 Go、构建、Node 语法/组件、i18n、真实 Chromium 浏览器、API/SSE e2e，包含新增首次启动用例。
- 最后一次 `make fmt-check test` 再次通过。
- `go test -race ./internal/pkg/topicstore ./internal/pkg/objectlocation -run 'Test(AmbiguityPreserves|FirstImportRecovery|ImportRecoveryDoesNot|RebuildClears|InitializeCan)'` 通过：两种候选顺序的元数据/附件保留和损坏隔离、首次恢复多任务、四种持久化/状态失败、原有重建及初始化回归。
- `node --test --test-name-pattern='first startup' web/api.e2e.test.js` 单独通过：真实进程首次就绪与 HTTP 读写。
- `git diff --check` 通过。

命令环境为 Linux、Go 1.24.13、Node 20，使用 `GOTOOLCHAIN=go1.24.13 GOCACHE=/tmp/thoughtflow-go-build GOFLAGS=-p=2`。不将局部 race 视为全量 HTTP/DuckDB race，不将本机验证视为最终提交的 Ubuntu/macOS CI；本轮未提交或推送修改。历史兼容豁免沿用会话中用户明确要求，本轮不扩大该豁免。

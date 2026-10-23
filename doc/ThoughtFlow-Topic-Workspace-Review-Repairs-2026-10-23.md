# 专题工作区复核修复记录

日期：2026-10-23。修复基线：`7a7233a2f928f79a4c0c75e454d3e77cfc24d388`。本文件记录后续工作区修改，不把基线提交的 CI 结果当作修复后的验收结果；最终评审须绑定实际提交 SHA。

## 范围

针对复核 R1–R15 修复。用户此前两次明确免除开发阶段历史数据兼容，已同步修订正式设计：旧布局自动升级、旧 runtime 证据读取回退、旧 profile 与旧客户端参数兼容不作为本轮门禁。当前 aggregate 功能、新布局的不可变历史、显式迁移、恢复与私有数据保护仍须验证。

## 逐项修改与失败场景

| 发现 | 修改 | 本轮新增回归 |
|---|---|---|
| R1 | 未提交业务冲突持久化为终止 `conflicted`，启动跳过；未提交暂态失败保留可重试状态；已提交恢复失败仍明确报错。失败清理只删除有本操作发布记录且哈希未变的非共享目标副本 | 预览后修改、staged/promoted 中断后修改，重新构造 Registry 恢复；源可读、其他对象可写、新计划可执行；同哈希外来目标保留 |
| R2 | 增加明确的 `GetUnderLease` 与 `SourceHashUnderLease`，已有 namespace 租约内不调用重新加锁的来源回调 | 真正 thought 来源，在外层租约内等待 namespace writer 排队，再完成来源读取，读写均限时完成 |
| R3 | 每次导入恢复在任何目标写入前检查所有文件；topic/object 必须属于相同导入且元数据吻合，其余已存在字节必须吻合包摘要；外来冲突成为终止状态 | 4 个导入断点 × 专题、附件、综述、目录 4 类外来修改，重试与恢复均保留原字节 |
| R4 | 自动相对资源发现只允许受控 attachments 根；配置 runtime 根另外注册为私有，覆盖上传、绑定、读取，不能因位于 attachments 内被提升 | workspace 内 runtime、默认回退位置 `.thoughtflow`、attachments/runtime 的私有 canary；预览拒绝、无公开绑定、通用资源读取不可得、源保留 |
| R5 | 分开判断无 HEAD 的普通 note 与损坏正式链；存在 HEAD 后所有链路错误原样传播；有正式投影或残留 journal 却无 HEAD 也拒绝正常备份 | 定稿后产生新修订，删除祖先 body，再备份必须失败；既有普通 note 备份继续成功 |
| R6 | 包清单显式记录 owned/external_reference/missing/snapshot/planned；仅包内 owned 校验原件，不把外部关联伪造成 owned | 全局引用、其他专题引用、删除来源、混合 owned/reference 四组，空目标和已有外部原件目标往返，目录与必需分母不丢失 |
| R7 | 预览与执行允许同身份、同哈希的保留资源安全复用；普通对象目标不按附件复用 | 两篇共享附件对象依次迁入，同一对象专题与全局双向迁移 |
| R8 | 快照使用独立专题索引表，查询时按当前权威目录同步；返回 snapshot/entry/revision 身份，排除全局 thought 索引与原件操作按钮 | 创建真实快照、删除来源、重建、快照独有词搜索仍命中；global 不命中；解绑后立即消失 |
| R9 | 扫描实际正文图片/附件，外部或缺失资产记录 issues，快照及交付标 partial；不自动下载；阅读与下载界面明确提示；备份不冒称已验证离线完整 | 正常 HTTPS 图片定稿 → 快照 partial → 专题读取披露 → 交付清单 partial |
| R10 | 对外 `ReadThoughtRaw` 持租约覆盖位置解析到文件读完；已有租约的内部路径显式使用 UnderLease | 真实公开 HTTP 读取在解析后暂停，迁移 writer 必须等待，随后公开读取与迁移均成功 |
| R11 | 重建重新验证唯一对象后，即使路径未变，也将 ambiguous 恢复 active 并推进 generation | 双副本歧义 → 移除非登记副本 → 重建 → 正常读取；schema 缺失时初始化修复也不能重入 registry mutex |
| R12 | 本文锚点与 `#/` 应用路由分开处理，避免目录链接二次编码 | 真实 Chromium 点击本篇目录、滚动定位、刷新和历史前进后退 |
| R13 | 复制精确链接和目录链接使用响应确认的 revision；未确认 revision 时不启用精确复制 | 复制 rev-A → 服务端最新变 rev-A2 → 打开复制链接仍为 rev-A，桌面与窄屏均覆盖 |
| R14 | 连续阅读复用按 groups 排序和当前筛选的选择器，过滤 planned/missing/unavailable；final 还过滤无定稿项 | 分组顺序与数组顺序不同、有计划项及无定稿项，验证上一篇下一篇和目录选中 |
| R15 | 搜索独立 request epoch；管理操作绑定 topic/selection epoch、DOM 实例和动作序号；响应与确认按钮安装前检查作用域 | Node 真实函数覆盖搜索 old/new、专题切换后的活动/迁移/转换/导出；Chromium 覆盖迟到搜索响应 |

新增后端测试集中于 `objectlocation/review_regression_test.go`、`topicstore/review_regression_test.go` 和 HTTP `service/topic_workspace_test.go`；均使用临时文件/工作区。前端测试在 `web/app.test.js`、`web/app.browser.test.js`，API 回归在 `web/api.e2e.test.js`。

快照全文检索使用独立词法索引，不新增快照全文向量化，不改变标题/摘要/标签的向量输入规则；未宣称快照已有专用语义向量召回。完整备份保持外部关联事实，自包含交付需要显式固定快照。

## Ubuntu 基线 CI 失败定位

只读获取 [run 39907873628](https://github.com/muidea/thought-flow/actions/runs/39907873628) 的失败日志，确定失败在 API e2e 的 collection 生命周期：迁移执行期收到 `409 generation_conflict` 而测试直接期待 200。后台精炼可在预览后改变源文件，冲突保护应保留。

测试现在对 409 校验错误码与源归属不变，并以新 operation ID 获取新预览重试，有界等待；真正的冲突与源内容保护另有后端确定性故障测试，不以放宽 CAS 消除失败。

## 本地验证

环境：Linux，Go 1.24.13，Node 20，真实 Chromium；未操作真实用户工作区，未调用外部模型。命令使用 `GOTOOLCHAIN=go1.24.13 GOCACHE=/tmp/thoughtflow-go-build GOFLAGS=-p=2`。

已执行通过：

- `make ci-check`：fmt、全量 Go、构建、Node 语法与组件、i18n、真实浏览器和 API/SSE e2e 全部通过。单独执行的新增专题阅读浏览器场景同时通过桌面和窄屏。
- `go test ./internal/pkg/objectlocation ./internal/pkg/topicstore ./internal/pkg/documentflow ./internal/pkg/markdown`：存储与文档相关包通过。
- `go test -race ./internal/pkg/objectlocation ./internal/pkg/topicstore ./internal/pkg/markdown ./internal/pkg/documentflow -run 'Test(StaleMigration|MigrationConflict|RelativeDiscovery|AttachmentsCan|RebuildClears|ImportRecoveryNever|BackupRejects|BackupPreserves|DraftSource|RemoteImages)'`：新增存储与并发场景通过；该筛选在 markdown/documentflow 内没有匹配测试，不将其算作这两个包的完整 race。
- `go test -race ./internal/pkg/markdown ./internal/pkg/documentflow`：两个包的完整 race 另行通过。
- 最后补充 R11 初始化锁回归后，`go test -race ./internal/pkg/objectlocation -run 'Test(RebuildClears|InitializeCanRepair)'` 通过；随后 `make fmt-check test build` 再次通过。
- 最后单独核对 Node 组件：139/139 通过；i18n 6/6 已在完整门禁通过。
- `git diff --check` 通过。

未执行全量 HTTP/DuckDB race；不以局部 race 代替整个服务并发验收。未进行真实断电及所有文件系统/浏览器组合测试。

原 65 项矩阵继续见实施记录，区分自动化与代码核对；本轮补充不等于所有 65 项均进行了完整独立端到端复测。最终 SHA 的 Ubuntu/macOS 远端 CI 尚待提交后执行。

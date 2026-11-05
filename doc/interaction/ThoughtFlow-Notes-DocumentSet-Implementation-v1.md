# 笔记、专题与成果集交互实施记录 v1

日期：2026-11-05。依据 [已确认的交互设计](ThoughtFlow-Notes-DocumentSet-Interaction-v1.md)。本文记录产品代码，独立 HTML 原型仍仅用于设计对照。

## 1. 页面调整

| 设计要求 | 产品实现 |
| --- | --- |
| 单篇与成果集共同发现 | 笔记默认显示独立文档和成果集卡片；服务端完成完整分组后再过滤、排序、分页。支持全部／单篇／成果集及按文件平铺。 |
| 清楚的计数口径 | 聚合显示内容项数，平铺显示正文数；来源材料不计入正文。侧栏使用聚合内容数。 |
| 先理解整套，再进入正文 | 打开成果集先看业务目标、目录、版本、保存／检查／批准状态、待决事项及来源。 |
| 草稿和正式内容统一阅读 | 同一个阅读器展示已保存归档草稿及可达的正式套版本，保留其各自身份。 |
| 固定版本与章节 | 阅读链接包含 set、kind、version、member、section；默认选择一经读取便固定到 URL。版本切换保留存在的成员，否则回退首篇并解释。无效手工深链接保持错误。 |
| 已有定稿仍为默认 | 有定稿时默认读取定稿，同时提示其他更新版本；失败的新任务不会替换旧正文。 |
| 正文优先 | 移除笔记详情／状态／运行信息的主标签页；信息、来源和处理记录放入次级折叠区。导入、继续采集、加入成文来源、分享、编辑和删除保留相应入口。 |
| 原件与副本区分 | 固定正文可进入可编辑 Thought 副本，再返回固定原件。正式受管成员继续走现有文档编辑和并发协议。 |
| 专题组织三类内容 | 专题首页分别显示成果集、独立文档和来源材料。已有引用目录、规则及专题正文保留为相应次级入口。 |
| 专题版本策略明确 | 支持跟随最新归档、工作版本、定稿，或固定指定版本；没有对应版本时不降级到其他类型。 |
| 搜索保留上下文 | 命中成员 Thought 时，增加所属成果集、角色、状态及固定版本入口，原笔记结果与操作继续保留。 |
| 手机阅读 | 主导航、成果集目录和章节分别使用原生对话框抽屉；正文单栏，保留键盘／Esc 关闭和焦点管理。 |

原有 `#/notes?id=…` 仍可阅读单篇，旧专题中的 `set_revision` 固定链接导向统一阅读器。Capture 中打开已保存归档也进入成果集概览，不再使用逐篇展开的独立草稿窗口。

## 2. 数据与访问边界

新增 `internal/pkg/contentlibrary`，通过既有 Thought、对象位置、已保存归档 run、SetHead 及保留版本投影内容；不按标题或 Profile 猜测成员关系。

新增接口：

| 方法与路径 | 用途 |
| --- | --- |
| `GET /api/content-library` | 聚合／平铺清单、筛选、排序和分页计数 |
| `GET /api/content-library/sets/:id` | 默认或精确归档／正式版本的目录、正文及状态 |
| `GET /api/content-library/sets/:id/markdown` | 指定已保存版本的阅读用 Markdown ZIP |
| `GET /api/topics/:id/content-library` | 专题的成果集、独立文档及来源材料 |
| `GET /api/topics/:id/content-links` | 已持久化的成果集引用策略与 generation |
| `PUT /api/topics/:id/content-links` | 使用 operation ID 和 expected generation 保存引用策略 |

读取归档会复用完整冻结证据校验，验证原对话、计划成员及落盘 Markdown；正式版本仍由现有可达性检查读取。未提交候选不会因为新增阅读入口而变为可见。读取期间发现命名空间变化会有限重读，不能取得一致结果则返回冲突。请求代次与路由身份保护前端，防止旧响应覆盖新的内容选择。

专题策略保存在可选的 `topics/<id>/content-links.json`，采用独立 schema、generation、幂等操作和原子写入。没有该文件时兼容原有专题成员关系；与历史成员关联的专题按稳定 set 身份找到当前成果，而不是只比较新版成员文件 ID。明确固定的引用损坏或缺失时保留不可用提示。

该文件纳入专题 Git 路径和备份，导入校验共享的引用合同。备份保存引用身份及策略，不自动打包外部成果集；在缺少目标的工作区恢复后，应显示引用不可用，不能假称目标正文已恢复。已有专题删除门禁继续生效。

Markdown ZIP 将正文放入 `documents/`、原始对话放入 `sources/`，保持各文件字节；说明文件标明这是阅读下载，不代表正式交付批准、不包含链接附件，也不是工作区备份。正式交付、诊断包、备份、定稿及工程移交仍调用原有工具和服务端门禁。

本次没有迁移真实工作区、修改用户配置或自动重启运行中的服务。新增展示和本地元数据操作不调用 LLM。

## 3. 实现位置与验证

- 产品入口与次级管理接线：`web/app.js`、`web/index.html`。
- 清单、概览、固定阅读器和专题引用交互：`web/content-library.js`。
- 排版与移动端：`web/styles.css`；文案同步中英文和 key registry。
- 统一内容投影：`internal/pkg/contentlibrary`。
- 专题引用合同：`internal/pkg/contentreference`。
- HTTP：`internal/modules/application/thoughtflow/service/content_library.go`。
- 原归档完整性校验复用：`internal/pkg/documentsetflow/archive_thoughts.go`。
- 专题路径、备份导入与删除接线：`internal/pkg/topicstore`。

新增确定性测试覆盖：聚合先于分页、来源不计入正文、精确候选不可达、冻结文件篡改拒绝、笔记副本不替代原件、失败新任务保留旧内容、阅读 ZIP 完整字节、HTTP 协议、搜索／副本的成果集上下文、专题引用 CAS／幂等／重载、历史成员关联、缺失目标可见及外部引用备份恢复。

浏览器使用实际嵌入式产品资产与临时接口夹具，覆盖桌面和手机的归档／正式阅读、章节深链接和刷新、逻辑成员链接、版本成员增减与前进后退、定稿默认与失败更新、专题固定引用保存后重载、无效深链接、移动端抽屉和原有导入／编辑／交付入口。夹具不调用真实模型。

最终本地工作树验证（基线 `2c128bb`，尚未提交）：

| 命令／检查 | 结果 |
| --- | --- |
| `GOMAXPROCS=2 GOFLAGS=-p=2 make ci-check` | 通过全部目标：fmt、全量 Go、build、Node 语法／组件、i18n、浏览器、API／SSE e2e。见 [门禁记录](validation/ci-check.log)。 |
| Node 组件／i18n | 162／162、6／6 通过。 |
| `GOMAXPROCS=2 GOFLAGS=-p=2 go test -race ./internal/modules/application/thoughtflow/service ./internal/pkg/topicstore -run 'TestContentLibrary\|TestTopicSetReferences\|TestBackupPreservesExternal' -count=1` | 两个包通过。见 [并发回归记录](validation/race.log)。 |
| `git diff --check` | 通过。 |
| 实际产品截图 | Chromium 桌面／手机 8 张，截图过程未记录脚本异常。 |

原型的 70 项检查与产品测试分别计数，不互相替代。这些是本地临时工作区及模拟 Provider 的结果，不是新提交的远端 Ubuntu／macOS CI 证明。大规模库的完整性读取成本及真实模型内容质量不在本次测试结论中。

新二进制已构建；未自动重启用户运行中的服务。

## 4. 产品截图

以下为真实 `web/` 产品资产配合临时接口夹具的截图，不是独立原型，也不代表生产工作区内容。

| 页面 | 桌面 | 手机 |
| --- | --- | --- |
| 笔记清单 | [桌面截图](implementation-screenshots/desktop-notes.png) | [手机截图](implementation-screenshots/mobile-notes.png) |
| 成果集概览 | [桌面截图](implementation-screenshots/desktop-overview.png) | [手机截图](implementation-screenshots/mobile-overview.png) |
| 固定版本阅读 | [桌面截图](implementation-screenshots/desktop-reader.png) | [手机截图](implementation-screenshots/mobile-reader.png) |
| 专题内容 | [桌面截图](implementation-screenshots/desktop-topic.png) | [手机截图](implementation-screenshots/mobile-topic.png) |

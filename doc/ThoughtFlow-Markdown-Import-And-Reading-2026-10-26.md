# Markdown 导入与完整阅读

日期：2026-10-26。基于 `bcd1666` 后的实现。

## 使用方式

在“笔记 → 全部 → 导入 Markdown”选择一个或多个 `.md` / `.markdown` 文件并导入。逐文件显示结果：成功项可以打开对应 Thought，失败项显示原因，其他文件继续处理。导入不依赖外部模型，保存后沿用已有索引、元数据处理和 Git 事件链路。

支持 UTF-8（可包含 BOM），单文件上限 32 MiB。空文件、无效编码、NUL 或超限文件明确拒绝，不猜测编码、不截断。网络中断时不自动重发，请先核对列表，避免重复导入。

按用户确认，本轮只上传 Markdown；本地图片和附件不一并收录。原始链接保持不变，导入入口和导入文档的阅读入口均提示附件未导入，不能将它当成离线完整资源包。

## 转换与信息保留

- 每个文件建立新的 Thought 身份和安全路径。来源中的 ID、路径、文档契约和处理状态不直接取得权威地位，也不覆盖已有笔记；重复导入仍创建独立对象。
- 尽力从 YAML 中提取标题和标签；没有可用标题时取 Markdown 标题，最后回退到文件名。不能解析的元数据不阻止导入，其原文仍完整保留。
- 完整原文件存放在生成 Thought 自身的 Original 区域；可编辑正文初始取相同内容。`markdown_import` 记录原文件名、字节数和 SHA-256。未知 YAML、注释、正文、链接、脚注、嵌套章节和代码块均保留，不依赖临时上传文件。
- 导入文档使用 `sections-v3`，保留 BOM、CRLF/LF、空白和文件末尾换行。解析只去掉 Thought 的外层封装，不再对嵌入原文件统一换行；分区仍有唯一边界和完整性校验。
- 正文编辑保留 Original；类型化文档同步也不规范化导入原文件。原文件下载核对字节数和摘要，不将已被外部改动的内容冒充原文件。原文在 Thought 内，沿用现有迁移、Git 和备份对 Markdown 的保留路径，无独立临时源文件依赖。
- 不把原文中的 HTML 当作可执行页面。阅读使用既有安全 Markdown 渲染器，源码显示使用文本转义，下载为 attachment / nosniff。

## 阅读

列表、详情和搜索预览抽屉增加“完整阅读”。独立阅读窗口支持桌面、窄屏、展开阅读区域、本篇目录、Thought 源码、全部信息、下载 Thought，以及导入文件的原件下载。

正文包含实际 `ai_notes`、原始内容、抓取内容、链接、摘要、要点和扩展内容；相同内容只显示一次。全部信息提供完整 Thought 元数据、资源绑定、任务及 Git 记录。源码读取实际存储的 Thought 文件。详情正文取消原来的 62vh 高度上限；普通预览补上此前遗漏的 `ai_notes`。

阅读请求、源码请求和关闭操作使用代次隔离；迟到的源码响应不会覆盖新选中的阅读模式或文档。目录按钮只在阅读区域滚动，不改写 SPA 路由。

## API

| 接口 | 行为 |
| --- | --- |
| `POST /api/thoughts/import` | multipart/form-data，单个 `file` 字段，完整读完请求后保存，201 返回 CaptureResult |
| `GET /api/thoughts/:id/markdown` | 私有 API 下载实际 Thought Markdown，沿用位置租约及删除检查 |
| `GET /api/thoughts/:id/import-source` | 私有 API 下载摘要一致的原始导入文件；缺失 404，原件被改动 409 |

浏览器为多文件逐个调用导入 API，按文件保留成功和失败明细，不承诺批次原子性。多 part、截断请求、格式或大小错误在保存前拒绝。

## 验证

新增 Go 回归覆盖原文逐字节读写、新服务实例重读、编辑保留、复杂/非法 YAML、同名重复导入、新身份隔离、非法编码和超限拒绝；HTTP 回归覆盖上传、下载、完整请求检查和删除撤销。API e2e 使用真实进程和临时工作区，核对实际 Markdown 文件及原件下载。

Node 回归覆盖全部正文内容和 multipart boundary；Chromium 桌面/窄屏通过真实鼠标点击覆盖上传、部分文件失败、正文末尾、目录、源码、全部信息、展开/关闭及迟到响应隔离。上传夹具校验 BOM/CRLF 原始字节，脚本片段不执行。

最终本地验收（Go 1.24.13）：

- 完整 `make ci-check` 退出码 0：fmt、全量 Go、构建、JS/Node、i18n、浏览器矩阵及 API/SSE。最终轮构建包含全部阅读入口，日志 `/tmp/thoughtflow-import-ci-final.log`，退出记录 `/tmp/thoughtflow-import-ci-final-status.json`。
- 导入业务及 HTTP 导入回归带 `-race` 通过，日志 `/tmp/thoughtflow-import-race.log`；Markdown/documentflow 现有回归由完整 Go 测试覆盖。
- Chromium 新增桌面/窄屏定向回归通过，日志 `/tmp/thoughtflow-import-browser.log`；最后的全量浏览器矩阵同样通过。Node 及 i18n 的补充构建检查日志为 `/tmp/thoughtflow-import-final-build.log`。
- `git diff --check` 及新增文件空白检查通过。

验收期间未操作真实工作区、调用真实模型或重启现有服务，尚未取得本轮提交的远端 CI。原件保留验证基于真实临时文件和服务；浏览器上传/阅读交互使用隔离的脚本化 HTTP 服务，不冒充对真实用户文件的操作。

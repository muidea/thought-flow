# ThoughtFlow LLM 缓存使用率核对（2026-11-04）

## 1. 结论与范围

用户提供的 18:43:22–18:50:13 八次请求均为 thought-flow / gpt-6-luna / responses，成功返回但显示缓存 0%，输入从 15,293 增至 31,541 tokens，并在三个账号之间轮换。

源码确认两个影响复用的因素：ThoughtFlow 没有发送稳定的网关会话标识；成员生成把变化的 Profile、参数和成员信息放在可复用的大段冻结内容之前。请求越来越长不等于可缓存的共同前缀越来越长。另有可观测性缺口：ThoughtFlow 成功日志未记录缓存计数及其是否由上游提供。

不能仅凭这些记录认定每次请求的缓存键都在变化。当前本地 AetherRelay 已对未声明会话的请求生成内容锚点缓存键，账号路由与缓存身份分别处理。也不能从界面文字证明在线计数一定为已知零、所选上游一定支持相同缓存能力，或账号切换就是唯一根因。

本次只核对与记录缓存问题，没有修改缓存请求、网关代码、账号调度、真实配置或运行中服务，也没有调用真实模型。归档草稿进入笔记列表的修复另行实施，不能将其测试当作缓存命中实测。

## 2. 可复核的代码依据

核对 ThoughtFlow 当前工作树（HEAD `d0405bd8f8911d807a0d63d2d2146028bee812a7`，存在本地增量），以及本地 AetherRelay 源码（HEAD `cb59e8ac8b75fdee1cff28fe96df7d0e401ac4db`）。这些不是在线服务构建版本的证明。

| 位置 | 核对结果 | 含义 |
| --- | --- | --- |
| ThoughtFlow `internal/pkg/ai/generation.go` 的 `WithGenerationScope` / `executeGeneration` | scope_id 仅用于日志关联；X-Request-ID 每次新建；没有显式 Session-Id、X-Session-Affinity 或 prompt_cache_key | 现有 scope 日志不代表已向网关声明稳定会话；每请求 ID 应继续保持唯一，不能挪作会话 ID |
| ThoughtFlow `internal/pkg/ai/refiner.go` 的 `responseCompletionWithFormat` | 两条消息：system + 整段 user；store=false；使用 `/v1/responses` | 每次是自包含请求；仅换成 Responses 或声明 JSON 输出不会自动形成可复用会话 |
| 同文件 `GenerateDocument` | user 依次为 Profile JSON、Parameters、Source context、Previous draft、Repair issues；Profile 中的说明、章节和 ID 按角色改变 | 角色差异很早进入有效提示词，大段共享 Capture 位于差异之后 |
| ThoughtFlow `internal/pkg/documentsetflow/archive_draft.go` | 每个成员携带完整 capture、shared_design、member 及累计 upstream_drafts | 输入增长与上游正文累积一致，但 member 及外层 context 的标题等内容也变化，当前并非逐轮追加的稳定消息序列 |
| AetherRelay `.../service/proxy/codex_compat.go` 的 `codexSessionHash` | 未声明逻辑会话时使用 request scope；显式会话与 client/model 命名空间共同形成路由身份 | 若在线采用此 Codex 路由，ThoughtFlow 当前请求不能复用同一逻辑会话的账号绑定 |
| AetherRelay `.../blocks/codexaccountpool/biz/biz.go` | 按 SessionHash 查已有账号绑定，但仍执行可用性、并发等调度限制 | 稳定会话可提供账号复用机会；不能保证永不换账号，也不能绕过故障切换 |
| AetherRelay `codexPromptCacheHash` / `fingerprint.go:conversationAnchorHash` | 缓存键优先使用声明身份，否则按 system/instructions 和 input/messages 首条消息生成内容锚点 | 缓存键与账号路由分别处理；对 ThoughtFlow 的双消息结构，首条是 system，同一模式下锚点可能稳定，不能声称缺客户端 key 必然导致缓存键每次变化 |
| AetherRelay `web/admin/index.html:usageCacheRate` | `cached_input_tokens_known=false` 时显示“—”，已知零可显示 0% | 需核实线上版本及具体事件原字段；不能把缺少计数直接计成真实零命中 |

上表的 Codex 账号池结论仅适用于对应路径。仅有 `gpt-6-luna` 模型名和 responses 端点不足以确定在线实际 Provider；需要具体事件的 Provider、路由身份与构建版本加以对应。

## 3. 为什么完整上下文仍可能不命中

Prompt caching 复用的是符合上游模型规则的相同提示词前缀及缓存边界，不能按“正文中某处含有同一段文字”复用。相同缓存键也不等于内容已命中；跨账号、组织、区域或执行机器的复用能力不能默认成立。

当前生成请求近似：

```text
稳定 system
变化的角色 Profile + 角色参数 + 当前成员 context 标题
冻结对话 + SharedDesign
变化的当前成员 + 累计上游正文
```

下一成员即使重复发送大段冻结对话，前面已经发生差异。小段相同 system 是否达到可缓存长度、采用什么边界，取决于真实模型及上游协议；不能为网关别名套用其他模型的固定 token 门槛。

OpenAI 官方还说明部分模型的隐式缓存以消息边界为查找点；改写同一条 user 消息与保留旧消息再追加，不具有相同的缓存行为。具体适用性需按实际模型核验。当前本地 AetherRelay Codex 规范化会移除不支持的 `prompt_cache_options` 与嵌套 `prompt_cache_breakpoint`，因此不能直接照搬其他 OpenAI 原生模型的 TTL 或 breakpoint 配置。

官方依据：[Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)。官方规则用于解释缓存条件，不代表中转模型别名已获得对应能力。

## 4. 建议的后续代码收口

按以下顺序实施，先补证据，再评价提升幅度。

1. **稳定会话身份。** 为同一 Capture / 成果集的关联生成设置独立的缓存会话上下文，发送网关已支持的 Session-Id；必要时显式设置 `prompt_cache_key`。值应由应用、Provider、模型、Capture/成果集身份经命名空间及摘要生成，不含正文、邮箱、凭据；不要使用每次变化的 run/call/request ID。X-Request-ID 继续每次唯一。Provider/模型变化后使用新命名空间，不强制账号池忽略限额或故障切换。
2. **稳定内容与任务内容分开。** 针对成员生成增加明确的上下文 DTO：将同一冻结源的原始 Capture、SharedDesign 及共享合同放在稳定消息前部；当前角色 Profile、成员身份、依赖选择、修复问题放在后续独立消息。相同冻结源的序列化应确定；上游固定正文也要按固定身份、hash 和顺序传递。现有来源状态、完整原文、严格 schema 和业务校验不能为命中率而删除。公共 `GenerateDocument` 的普通单篇路径需保留兼容，不能只改 JSON map 的键名顺序就宣称完成。
3. **缓存计数可观测。** 在 JSON 与 SSE 两种成功返回中读取 input/output tokens、cached tokens、上游是否明确提供该值；日志与私有调用账本区分 unknown、known-zero、known-hit。记录安全会话摘要、请求 ID、阶段和可用的模型标识；跨网关用请求 ID 对齐 Provider、缓存键来源和账号切换原因。不记录原始提示词或账号秘密。
4. **受控回归与线上复核分开。** 本地 mock 验证稳定身份、消息顺序和计数存在性。确认允许的数据与模型调用范围后，再对同一冻结源做线上有限重复验证；比较缓存读取 tokens、实际输入成本与时延。不要为预热新增无业务意义的调用，不保证达到固定命中率。

最低回归门禁：

- 同一 Capture 连续调用及显式新 attempt 的会话稳定，跨 Capture/Provider/模型隔离；请求 ID 每次不同。
- 不同成员实际 HTTP payload 的共享消息逐字一致；Profile、成员和修复内容仅在后续消息变化，输入预算按完整重排后的 payload 计算。
- 用户新增上下文后，不混入旧冻结内容；重排不改变原证据、来源状态、依赖闭包、JSON 强制策略和批准门禁。
- JSON 与 SSE 分别覆盖 cached tokens 缺失、显式零和正数；上游缺失不能被补成已知零。
- 账号不可用仍正常故障切换；不把缓存未命中当作请求失败重试。
- 保留 `store=false`，不因缓存优化改成服务端会话存储或引入 `previous_response_id`；缓存前缀与直接复用旧生成结果分开，所有新结果仍须通过原校验。

## 5. 本轮验证及仍缺的在线证据

已运行 AetherRelay 管理界面用量组件测试：45/45 通过，日志 `/tmp/thoughtflow-cache-ui-observation-test.log`。该结果验证本地界面对未知与已知缓存值的处理，不证明用户八次请求的上游是否实际读取缓存。

已运行 AetherRelay 现有定向 Go mock 测试，覆盖稳定会话、无会话时逐请求路由、缓存键生成及内容锚点稳定、HTTP/SSE 转换用量与缓存计数存在性，全部通过；日志 `/tmp/thoughtflow-gateway-cache-audit-test.log`。其中 HTTP/SSE 用量转换夹具是网关转换入口，不将其宣称为 ThoughtFlow 原生 Responses 的线上复现。本次没有为取得该结果修改 AetherRelay 源码。

没有读取该八次请求的完整在线交互归档，尚缺实际 Provider/构建、会话及缓存键摘要、缓存计数 known 标志、上游缓存字段、账号切换原因与相邻请求的有效提示词结构。故当前结论为已确认可优化的客户端结构和观测缺口，而非已完成线上根因唯一归属或缓存提升验证。

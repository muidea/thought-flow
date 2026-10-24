# AetherRelay 客户端收口方案对照核对

日期：2026-10-24。核对方案：`~/aispace/AetherRelay/docs/design/thought-flow-client-closure-plan-2026-10-24.md`。

本记录前文为修改前核对，实施结果见文末。

核对对象是 ThoughtFlow `e01cf3cd943de5cc56c23568cd608a41df6e93ac` **加当前未提交工作树**，包括新增 `json_capability.go`、`json_retry.go` 与相关回归；不能将这些实现当作 HEAD 或线上版本已有能力。本次只新增核对记录，保留此前实现，不修改 AetherRelay、不发布、不重启服务、不调用真实模型。

## 结论与范围对齐

仍有对应代码需要收口。首要问题是生成请求重试裁决：当前新增的有界恢复虽然限制了次数，但将一般 502/网络错误当作可以重发，与方案要求的“已发送后结果未知不自动再生成”不一致。写入一个 Thought 的幂等性不能证明上游只生成一次。

用户最新明确要求 **JSON fallback 默认启用，不通过参数控制**；此前还要求同 Provider/模型沿用已确认降级。因此方案中的显式允许开关、`required | preferred | off`、旧配置兼容均不按原文新增。周期 TTL 重新协商也会改变“同配置持续 fallback”的行为，不能把文档中的可选 15 分钟示例直接实施为新要求。保留本地 JSON/业务校验，并清楚披露它不等于上游原生格式保证。

## 逐项对照

| 方案项 | 当前情况 | 判断 |
| --- | --- | --- |
| 文本不传格式参数 | `chatCompletion`、Classify 不传 `response_format` | 已有，无需重复实现 |
| 仅明确格式拒绝才降级 | `onlyResponseFormatUnsupported` 检查错误码及全部 feature；普通 400、混合功能、鉴权不降级 | 已有，默认自动启用符合最新要求 |
| 跨模块能力共享与单飞 | 规范化 endpoint + 凭据 SHA256 + model；首探测等待支持取消，异常释放等待者 | 已有，Mock 回归覆盖 |
| 生成请求统一重试 | 初始/文本 SDK 仍最多三次；兼容通道另有最多三次；业务还有重试 | 未关闭，优先修复 |
| 统一操作截止时间 | Capture 对话、归档分别建立操作 context，下层协商/兼容传递同一 context | 部分已有；不能把逐请求 HTTP timeout 当作完整操作预算 |
| 缓存边界和能力恢复 | `sync.Map` 无容量、无 TTL、无显式路由修订身份 | 缺少容量边界；TTL/路由恢复策略需遵循最新范围 |
| JSON 与业务校验 | 字段类型、非空回答、模板允许章节、正式内容验证均存在 | 主链已有；原生路径的错误终态、Classify 截断判断仍需统一 |
| 可关联诊断 | 本地状态码、错误码、feature、原因/字段存在；未结构化保留网关 request ID、failure_class、原始错误码 | 未关闭 |
| 发布核对 | 本地工作树尚未提交；没有本次线上构建版本及实际请求证据 | 未完成，不能以单测代替 |

## C1：生成请求不能因结果未知而自动重放

位置：

- `internal/pkg/ai/refiner.go:815–825`：聊天 SDK 默认 `WithMaxRetries(2)`；普通文本、Classify、原生 JSON 都受影响。Embedding 在独立构造器中，应独立保留其策略。
- `internal/pkg/ai/json_retry.go:23–52`：兼容通道对一般网络失败、429、5xx 最多尝试三次；即使发送后连接被关闭，也会重发。
- `internal/modules/capture/biz/scratchpad.go:543–600`：Capture 外层默认最多四次，直接以 `ProviderError.Retryable` 决定重放。原生 JSON 未进入 fallback 时仍可与 SDK 叠加，预算允许时可达到 12 次客户端 HTTP 请求，尚未计代理内部执行次数。
- `internal/modules/refiner/biz/service.go:267–271`：后台 Job 同样依据 Retryable 再次调用。

建议收口：聊天生成 SDK 默认零自动重试；操作策略区分“明确格式拒绝且未执行”“明确未开始生成的临时拒绝”“已发送结果未知”“内容无效”。明确格式拒绝最多进入一次兼容通道；一般 EOF、读超时、502 或单独的 `retryable:true` / `X-Should-Retry:true` 不能证明生成未发生。参数 fallback 与故障重试分开裁决。未知结果保留输入和诊断，终止自动生成，用户可显式重试。代理已负责生成前的有限恢复时，客户端避免再次叠加。

当前 Mock 用例 `TestAutomaticJSONFallbackRecoversOnlyTransientFailures/connection-recovers` 在服务器完整读取请求体后断开连接，仍期望第二次请求；`gateway-recovers` 同样允许一般 502 重放。它们实际通过，证明这是当前策略，而非仅凭推断提出的风险。这两类验收预期需要修改。

另需核对重启恢复：`capture_archive.go:167` 会恢复 preparing/interrupted；进入 `PreviewAndArchive` 后可能重新生成。应分别恢复已持久化输出/保存命令与结果未知的模型调用，不能用本地保存幂等替代模型执行幂等。该重启分支本次仅作源码核对，未执行故障注入。

## C2：统一截止时间与取消，而非每次请求重新计时

已有：Capture 对话在 `scratchpad.go:511` 建立 context，归档在 `capture_archive.go:122` 建立 context，协商等待和兼容请求继续传递它们，兼容退避可取消，没有在这些内部循环重新建立截止时间。

未充分覆盖：Provider 自身只配置 `http.Client.Timeout`，无截止时间的调用方不具有覆盖能力等待和全部 HTTP 尝试的总预算；SDK 内置退避使用 `time.Sleep`，等待不及时响应取消。应在业务边界统一截止时间且只允许缩短，取消/超时不继续协商、重试或提交输出。不要误改已经正确共享 context 的 Capture 路径。

回归需覆盖：排队能力等待消耗预算、协商后只剩部分预算、发送后读超时、取消退避、无截止时间调用的边界约束。当前取消测试只覆盖自定义兼容等待，不能证明全部 SDK/业务路径。

## C3：保留网关诊断，分离 Retryable 与自动重放权限

位置：`refiner.go:176–184,942–983`、`provider_errors.go:10–15`。

当前 ProviderError 缺少响应 `X-Request-Id`、`failure_class`、结构化的上游原始错误码/重试提示。`wrapSDKError` 对多数错误仅按 HTTP 429/5xx分类，网关 `error.retryable:false` 没有参与裁决；除 conversion 错误外，网关自身 code 也被通用错误码覆盖。fallback 终态通过 `Retryable:false` 防止外层放大请求，代价是丢失“后续是否值得用户重试”与“能否立即自动重放”的区别。

建议收口：保留本地稳定 code，同时独立保留网关 code、failure_class、request ID 及重试提示；关联会话/归档 operation/job 和调用阶段。成功协商/兼容响应也应记录安全 request ID，便于回查网关；不得记录 Authorization、API Key 或提示词正文。用独立策略判断自动重放，不能直接消费 Retryable。

当前 AetherRelay `proxy/api_error.go` 已输出 failure_class、retryable、feature 等字段，`proxy/requestid.go` 已写出 `X-Request-ID`，客户端可以消费，不需要从自由文本或 token 估算推断。`X-Client-Request-Id` 的上游会话含义不能与响应 `X-Request-ID` 混用。

## C4：缓存生命周期按最新需求收口

位置：`internal/pkg/ai/json_capability.go:16–38,44–83`。

已有共享身份、凭据摘要、首次单飞、取消释放。缺少有界注册表/闲置回收；不同配置持续构造会累积条目。实现容量边界时需考虑 Provider 持有旧状态指针，简单删 map 会让相同身份新旧实例各自探测，不满足跨模块单飞。

建议先在保持活跃身份共享的前提下限定闲置容量/回收；同配置已有 unsupported 判断继续沿用。若后续需要支持网关内部路由变化，应约定可观测的路由修订或失效信号；endpoint/credential/model 不变时客户端无法凭空识别 Provider 变化。TTL 与重新探测属于单独行为选择，不新增配置开关，也不默认每 15 分钟产生一次格式拒绝。

## C5：JSON 契约不能只在 fallback 路径终止

已有：`BuildCaptureContext` 检查 JSON 字段类型及非空 assistant_reply；`documentprofile/render.go:82–99` 检查标题、必需章节、未知章节。正式文档还经过结构与引用等校验，不能移除。

仍需统一：原生 JSON 路径的解析/截断/无候选等错误可触发外层重试，而 fallback 路径转终态。`recoverableArchiveDocumentGenerationError` 还允许原生截断进入有界重新生成；`Classify` 的 `chatCompletionWithMaxTokens` 没有与通用 helper 一样检查 `FinishReason==length`。应统一业务入口的截断拒绝、诊断与重放裁决，保留经业务授权的显式修复与普通原样重放的区别。

补充 `finish_reason=length` 但 content 恰好是可解析 JSON 的回归，不能仅以“解析成功”认定模型完整输出。上述 Classify 边界本次只作源码核对，未执行专用复现。

## 验证与下一批门禁

本次实际执行 AI 定向 Mock HTTP 回归，14 个顶层测试通过（含其子场景）：

```sh
GOTOOLCHAIN=go1.24.13 GOCACHE=/tmp/thoughtflow-go-build GOFLAGS=-p=2 \
CGO_LDFLAGS=-L/usr/lib/gcc/x86_64-linux-gnu/12 \
go test -count=1 -v ./internal/pkg/ai \
  -run 'Test(AutomaticJSONFallback|JSONRequestsDoNot|JSONFallback|JSONCapability|CachedJSONFallback|PlainTextCompletion)'
```

日志：`/tmp/thoughtflow-aetherrelay-plan-audit-test.log`。`git diff --check` 通过。此前当前工作树的 AI/Capture race 通过，但它不覆盖尚未新增的目标策略。上一轮启动的完整 ci-check 仍在浏览器步骤，未取得最终结果，不计作本次完整通过。

下一批先修 C1/C2/C3，再处理 C4 和 C5 的针对性回归。验收必须包含：原生与兼容通道的发送后断线不重放、代理恢复不叠加、取消不续期、网关 request ID 可关联、无效/截断内容不落盘、同配置首轮仅一次协商及后续直接 fallback。最后跑相关包/race 与完整 ci-check；提交、构建、运行版本分别记录。线上真实模型及配置核对属于发布验收，本次未执行。

## 对应代码实施

- C1：聊天 SDK 通用重试关闭，单次格式拒绝协商保留；删除基于一般 429/5xx/网络错误的兼容恢复循环。新增 AutoRetry 与 Retryable 分离，Capture/Refiner 自动重发只消费前者。持久化归档生成开始标记，启动不重做结果未知的模型调用。
- C2：生成 Provider 和 Capture/Compose/Refiner 操作边界建立有限预算，已有较短 context 继续沿用；能力排队、格式/兼容调用不续期。
- C3：结构化保留网关 request ID、code、failure_class、retryable 和能力字段，安全日志关联 session/operation/job；常规错误日志不输出提示词/凭据。
- C4：256 项有界注册表，操作租约固定当前共享状态，闲置 LRU 回收，容量全被占用时等待可取消。无 TTL/新配置开关；容量回收后下次业务请求可以重新协商。
- C5：原生/兼容 JSON 错误均进入明确终态，Classify 拒绝截断；语义审查/修复接入共用格式协商，原有字段/章节/业务校验保留。

新增回归覆盖上述失败分支、总预算、缓存容量与并发租约、归档重启保护和失败后显式重试。最终验证结果待完成后记录；未提交、部署或核对线上实际新版本。

最终本地验收（Go 1.24.13）：

- 完整 `make ci-check` 通过：fmt、全量 Go 测试、构建、JS 语法、Node、i18n、浏览器及 API/SSE e2e。日志：`/tmp/thoughtflow-client-closure-ci-final.log`。
- AI/Capture/Refiner 全包 race 通过；补充 AI/Capture/HTTP 服务的缓存、预算、失败诊断、显式重试与启动保护定向 race 同样通过。日志：`/tmp/thoughtflow-client-closure-race-final.log`、`/tmp/thoughtflow-client-closure-race-regression.log`。
- `git diff --check` 和全部新增文件空白检查通过。没有将先前允许自动重放一般 502 的通过结果充当最终策略验收。
- 未提交或推送代码，未重启当前工作区服务，未请求真实外部模型；运行服务需加载本次构建后生效。未取得新提交的远端 CI，线上代理/客户端版本联合验收仍未执行。

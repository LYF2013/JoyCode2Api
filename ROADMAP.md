# ROADMAP.md

JoyCode2Api 的真实进度源。项目介绍见 `README.md`，工作约定见 `CLAUDE.md`。

## 当前阶段

openai-response 模型（GPT-5.6 Sol、GPT-6 Astra）已切换到上游原生 Responses 通道并完成四组组合验证；GPT-6 Astra 由此打通。Kimi-K3 已完成适配验证。

## 已完成

- **openai-response 模型切换原生 Responses 通道 + GPT-6 Astra 打通**（2026-09-11）
  - 根因：GPT-6 Astra 在 chat completions 网关不存在（`6002 模型不存在`），它只在 color 网关 `responses_completions`（rawUrl `/api/saas/openai/v1/responses`）上，要求 label 原名 + 真 Responses 格式（`input` 而非 `messages`）；直连 v1/v2 responses 路径返回 403「请升级版本」，必须走签名网关。
  - 改动：`pkg/joycode/client.go` 加端点映射；新增 `pkg/openai/upstream_responses.go`（嵌套 SSE 解包、chat↔Responses 双向翻译、流式/非流式四个 handler）；`chat.go`/`responses.go` 的 `openai-response` 分支改走原生通道，去掉小写连字符转换。
  - 上游响应是嵌套 SSE：外层每个 `data:` 包裹一条内层 SSE 行（`data: event: xxx` / `data: data: {...}`）。
  - 验证（临时实例 34892 实测）：GPT-6 Astra 与 GPT-5.6 Sol × `/v1/chat/completions`、`/v1/responses` × 流式/非流式共 8 组全部通过；GPT-6 Astra 工具调用 + 工具结果回传两轮正常；GLM-5.3 回归正常。
  - 注意：测试用的是本仓库新编译二进制，线上 `~/JoyCodeProxy/JoyCode2Api-darwin-arm64` 仍为旧版，需重新部署生效。

- **Kimi-K3 适配验证**（2026-08-13）
  - 上游模型列表出现后自动生效，`adapterType` 为默认空值，模型名原样透传，**代理层无需适配代码**。
  - 验证通过：`/v1/chat/completions` 非流式 + 流式（`reasoning_content` 增量、usage、`[DONE]` 完整）、tool calling（`finish_reason: tool_calls` 参数正确）、`/v1/messages` 非流式 + 流式（Anthropic 事件生命周期完整）。
  - K3 是推理模型，响应带 `reasoning_content`；`ModelCapabilities` 补充 `"Kimi-K3": {Reasoning: true, MaxTokens: 16384, Ctx: 200000}`（Vision 与 MaxTokens 待上游确认）。
  - 对照组：同时段 GLM-5.2 5/5 正常，K3 约 4/13 成功——间歇挂死为上游 K3 专属问题，见「阻塞 / 上游限制」。
- **OpenAI Responses API 端点**（2026-08-07，commit cf7b3f2）
  - 新增 `/v1/responses` 端点，支持 Codex CLI 通过 `wire_api=responses` 接入。
  - 实现 Responses API ↔ Chat Completions 双向转换（流式 SSE + 非流式）。
  - SSE 事件格式兼容 Codex CLI 的 `ResponsesStreamEvent` 反序列化（每个事件的 data payload 含 `type` 字段，`response.created`/`response.completed` 数据嵌套在 `response` 字段下）。
  - 验证：Codex CLI 端到端测试通过，`codex exec "Say hello in 3 words"` 正常返回。
- **GPT-5.6 Sol 模型适配**（2026-08-07）
  - 上游模型列表中 `GPT-5.6 Sol` 的 `extJson.adapterType` 为 `openai-response`，上游 API 实际接受的模型名为 `gpt-5.6-sol`（小写 + 空格替换连字符）。
  - 代理层在 `adapterType == "openai-response"` 时自动转换模型名，客户端传展示名 `GPT-5.6 Sol` 即可。
  - 模型适配器类型缓存（10 分钟 TTL），避免每次请求查询上游模型列表。
  - 验证：`/v1/responses`、`/v1/chat/completions` 流式+非流式全部通过。
- **模型列表动态化**（2026-07-28，commit 009896b）
  - Dashboard `/api/models` 从上游 `ListModels` 实时获取，无账号时回退硬编码列表。
  - Settings、AccountDetail 页面默认模型下拉改为动态获取，请求失败时回退到 fallback 列表（不再出现空下拉）。
  - 验证：`/v1/models` 返回上游 15 个模型，Settings 下拉与之一致。
- **Claude 原生通道开关**（2026-07-28）
  - Settings 系统设置新增「启用 Claude 原生通道」开关，绑定 `enable_claude` 设置。
  - 仅影响 `/v1/messages`（Anthropic 协议），对 `/v1/chat/completions` 无影响。
  - 验证：开启后 `Claude-Opus-4.7`、`Claude-Opus-4.8-hq` 走原生通道成功返回。
- **模型名透传修复**（2026-07-28）
  - `resolveModel`（Anthropic）和 `ResolveModel`（OpenAI）去掉 `joycode.Models` 硬编码白名单，模型名非空即原样透传上游。
  - 验证：`Claude-Opus-4.8-hq` 经 `/v1/messages` 实测返回正常内容。

## 阻塞 / 上游限制

- **Kimi-K3 上游间歇性挂死**（2026-08-13 实测）：部分请求被上游网关卡住约 180s，随后返回京东 HTML 错误页（非 JSON）。非流式表现为代理重试后 500，流式表现为只有 keepalive 无数据。同时段 GLM-5.2 全部正常，属 K3 上游专属问题，代理层无法修复，只能等上游稳定或加重试容忍。
- JoyCode 上游模型列表（`/v1/models`）里的 `label` 与实际请求需要的模型名不一致：
  - `adapterType: "openai-response"` 的模型（`GPT-5.6 Sol`、`GPT-6 Astra`）：走原生 Responses 通道，用 label 原名，代理由 `upstream_responses.go` 处理（2026-09-11 起，此前的小写连字符方案已废弃）。
  - `adapterType: "anthropic"` 的 Claude 模型：`Claude-Opus-4.8` 需用带 `-hq` 后缀的真实名（`Claude-Opus-4.8-hq`），代理不做映射，由客户端直接传真实名。
  - `adapterType: ""` 的模型（GLM、DeepSeek 等）：label 即为 API 名，直接使用。
- Claude 模型走 `/v1/chat/completions`（OpenAI 协议）返回 `choices: null`，上游普通 SaaS 通道不支持该模型。需走 `/v1/messages`（Anthropic 协议）+ `enable_claude=true`。

## 待办 / 待确认

- 待确认：`Kimi-K3` 的 Vision 能力和 MaxTokens 上限（`ModelCapabilities` 先按 K2.6 口径填了 16384/200000）；thinking 参数控制是否支持（未加入 `ReasoningModels`）。
- 待确认：`pkg/anthropic` 的 `TestTranslateRequest_Basic`、`TestResolveModel_UnknownDefaults` 在干净工作区（HEAD）即失败（2026-08-13 发现，2026-09-11 复验），为预存问题，与本次改动无关。
- 待确认：是否让代理自动做 Claude 模型 label → chatApiModel（`-hq`）映射。当前决定不做，由客户端直接传真实名。
- 待办：`enable_claude` 目前需 Dashboard 手动开启，重置 DB 会丢失，是否需要默认值或持久化说明。
- 待办：新增模型（`MiniMax-M3`、`Claude-Opus-4.8`、`GPT-5.6 Sol` 等）尚未在 `ModelCapabilities` map 中配置 capabilities。

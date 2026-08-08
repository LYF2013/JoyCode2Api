# ROADMAP.md

JoyCode2Api 的真实进度源。项目介绍见 `README.md`，工作约定见 `CLAUDE.md`。

## 当前阶段

OpenAI Responses API 支持 + GPT-5.6 Sol 模型适配已完成并验证。

## 已完成

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

- JoyCode 上游模型列表（`/v1/models`）里的 `label` 与实际请求需要的模型名不一致：
  - `adapterType: "openai-response"` 的模型（如 `GPT-5.6 Sol`）：需转为小写连字符格式（`gpt-5.6-sol`），代理已自动处理。
  - `adapterType: "anthropic"` 的 Claude 模型：`Claude-Opus-4.8` 需用带 `-hq` 后缀的真实名（`Claude-Opus-4.8-hq`），代理不做映射，由客户端直接传真实名。
  - `adapterType: ""` 的模型（GLM、DeepSeek 等）：label 即为 API 名，直接使用。
- Claude 模型走 `/v1/chat/completions`（OpenAI 协议）返回 `choices: null`，上游普通 SaaS 通道不支持该模型。需走 `/v1/messages`（Anthropic 协议）+ `enable_claude=true`。
- 上游不存在独立的 Responses API 端点（`/api/saas/openai/v1/responses` 返回 `the current API does not exist`），`openai-response` 类型的模型仍走 chat completions 端点。

## 待办 / 待确认

- 待确认：是否让代理自动做 Claude 模型 label → chatApiModel（`-hq`）映射。当前决定不做，由客户端直接传真实名。
- 待办：`enable_claude` 目前需 Dashboard 手动开启，重置 DB 会丢失，是否需要默认值或持久化说明。
- 待办：新增模型（`MiniMax-M3`、`Claude-Opus-4.8`、`GPT-5.6 Sol` 等）尚未在 `ModelCapabilities` map 中配置 capabilities。

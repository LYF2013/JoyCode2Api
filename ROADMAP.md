# ROADMAP.md

JoyCode2Api 的真实进度源。项目介绍见 `README.md`，工作约定见 `CLAUDE.md`。

## 当前阶段

模型列表动态化 + Claude 模型路由修复已完成并验证。

## 已完成

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

- JoyCode 上游模型列表（`/v1/models`）里的 `label` 与实际请求需要的 `chatApiModel` 不一致：
  - `Claude-Opus-4.8` / `4.6` / `Sonnet-4.6` 直接用 label 请求会报 `read first line: EOF`，需用带 `-hq` 后缀的真实名（`Claude-Opus-4.8-hq` 等）。
  - `Claude-Opus-4.7` 用 label 即可，不带 `-hq`。
  - `-hq` 真实名只在本机 JoyCode IDE 的 `state.vscdb` 里可查，`/v1/models` 接口不返回。
- `Claude-Opus-4.8` 走 `/v1/chat/completions`（OpenAI 协议）返回 `choices: null`，上游普通 SaaS 通道不支持该模型。

## 待办 / 待确认

- 待确认：是否让代理自动做 label → chatApiModel（`-hq`）映射。当前决定不做，由客户端直接传真实名。
- 待办：`enable_claude` 目前需 Dashboard 手动开启，重置 DB 会丢失，是否需要默认值或持久化说明。

# CLAUDE.md

JoyCode2Api：JoyCode → Anthropic / OpenAI 协议翻译器。项目介绍与使用方式见 `README.md`，本文件只写工作约定，进度见 `ROADMAP.md`。

## 项目规范优先级

- 新项目先写 `CLAUDE.md`；新目录先定结构约定。
- 已有项目先读并遵守项目内 `CLAUDE.md`。
- 进入已有项目时同时检查 `ROADMAP.md`，先了解当前阶段、阻塞和下一步。
- 项目级 `CLAUDE.md` 优先于全局规则；但全局红线和安全约束不能降低。
- 没有规范的工作空间不动手，除非任务就是补规范。

## Roadmap 维护规则

- `ROADMAP.md` 是项目真实进度源，记录当前阶段、已完成、进行中、待办、阻塞、最近验证。
- 每次完成开发、修复、文档补齐或重要调研后，必须同步更新 `ROADMAP.md`。
- 纯查询、只读分析、临时命令、未改变项目状态的操作，不需要更新 `ROADMAP.md`。
- 只有已经实现并验证过的事项才能放进「已完成」。
- 未确认的信息写「待确认」，不要猜。
- 做完代码但未验证时，不得把事项标为已完成。
- `README.md` 写项目介绍和使用方式；`ROADMAP.md` 写会变化的进度。

## 构建与验证

前端打包进 Go 二进制（`go:embed`），改前端后必须先构建前端再编译 Go：

```bash
cd web && npm run build          # 产物输出到 cmd/JoyCode2Api/static/
cd .. && CGO_ENABLED=0 go build -o JoyCode2Api ./cmd/JoyCode2Api/
```

只改 Go 代码时跳过第一步。验证：

```bash
go build ./...        # 编译通过
go test ./...         # 测试通过
./JoyCode2Api serve --skip-validation --tls=false   # 起服务，curl 实测接口
```

- 前端构建产物（`cmd/JoyCode2Api/static/`）随仓库提交，改前端后要一并提交。
- 运行数据库在 `~/.joycode-proxy/proxy.db`（含账号凭据），绝不进仓库。

## 关键约定

- 模型名对外用展示名（`/v1/models` 返回的 label），但上游实际请求名可能不同。代理层根据 `adapterType` 自动处理：
  - `adapterType: ""`（默认）→ 直接用 label，走 `/api/saas/openai/v1/chat/completions`
  - `adapterType: "openai-response"` → 模型名转小写连字符（`GPT-5.6 Sol` → `gpt-5.6-sol`），仍走 chat completions 端点
  - `adapterType: "anthropic"` → Claude 模型走 `/v1/messages` 原生 Anthropic 通道，需 `enable_claude=true`（Dashboard 系统设置里可开）
- Claude 系列模型对 `/v1/chat/completions`（OpenAI 协议）返回 `choices: null`，上游普通 SaaS 通道不支持该模型。
- 模型解析优先级：客户端指定 → 账号默认 → 系统默认 → `joycode.DefaultModel`。

## 模型问题排查方法论

模型调用返回空（`choices: null`、`usage: null`）或报错时，按以下步骤排查：

1. **查模型列表**：`curl http://localhost:34891/v1/models -H "Authorization: Bearer joycode"` 确认模型在列表中
2. **查 adapterType**：上游模型列表的 `ext` 字段含 `adapterType`，决定模型走哪个通道。可通过上游 `modelList` API 获取原始数据
3. **测 chat completions 端点**：`curl -X POST /v1/chat/completions` 用不同模型名变体（原名、小写、带后缀）测试
4. **测 responses 端点**：`curl -X POST /v1/responses` 测试 Responses API 格式
5. **对比正常模型**：用已知可用的模型（如 `GLM-5.2`）对比，排除是代理层还是上游的问题
6. **关键区别**：`choices: null` = 上游不接受该模型名或端点；`code: 1032` = 端点正确但请求格式/模型名不对；`code: 6002` = 模型名不存在

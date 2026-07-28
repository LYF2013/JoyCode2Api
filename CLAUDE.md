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

- 模型名对外用展示名（`/v1/models` 返回的 label），但上游实际请求名可能不同（如 `Claude-Opus-4.8` 的真实 API 名是 `Claude-Opus-4.8-hq`）。真实名只在本机 JoyCode IDE 的 `state.vscdb` 里能查到。代理层对模型名原样透传，不做映射。
- Claude 系列模型走 `/v1/messages` 原生 Anthropic 通道，需 `enable_claude=true`（Dashboard 系统设置里可开）；对 `/v1/chat/completions`（OpenAI 协议）无影响。
- 模型解析优先级：客户端指定 → 账号默认 → 系统默认 → `joycode.DefaultModel`。

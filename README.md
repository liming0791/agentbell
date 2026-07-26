# AgentBell

AgentBell 是一个面向 Coding Agent CLI、IDE 和 Desktop Agent 的跨平台通知适配层。它不自建消息中间件，而是：

1. 为支持确定性扩展的 Agent 安装生命周期 Hook 或插件。
2. 把不同 CLI 的事件归一化成统一通知事件。
3. 调用飞书官方 `lark-cli`，把完成、失败或等待授权等事件发送到指定会话。

不同产品只发送能够被官方 Hook 载荷确定证明的状态。Codex 当前不能在
`PermissionRequest` 中区分人工审批与原生 `auto_review`，因此暂不发送 Codex
审批提醒；完成通知继续使用 `Stop`。

当前版本为 `v0.2.0-rc.3` Technical Preview。M1 本地开发版本已实现 Go 单文件 Core、
持久队列、Codex / Claude Code / Kimi Code / OpenCode / Qoder Adapter、`agentbell setup`、
`agentbell test`，以及 macOS LaunchAgent、Windows 登录计划任务和 Linux 用户服务
（见 [M1 setup 验收记录](./docs/m1-setup-validation.md)）。这些 M1 能力尚未发布；
Node.js Hook runtime 仅保留为 M0 迁移期原型，不是正式 Hook 数据面。

## 首期范围

- 确定性接入：Codex CLI/Desktop、Claude Code CLI/Desktop、OpenCode CLI/Desktop、Kimi Code CLI、Qoder CLI/IDE。
- 技术验证接入：ZCode、Tencent WorkBuddy、TRAE IDE。
- 等待官方 Hook：Kimi Work；不使用 Skill/MCP 软触发冒充生命周期通知，不进入首期运行时适配范围。

各产品的证据、等级和限制见 [兼容矩阵](./docs/compatibility.md)，被厂商能力阻塞的事项见 [项目待办](./TODO.md)。

## 目录

```text
agentbell/
├─ core/                     # Go 原生 Core、队列、Service 和 Adapter
├─ packages/
│  ├─ cli/                 # 用户安装、检测、绑定和诊断入口
│  └─ hook-runtime/        # M0 Hook 协议原型，非正式数据面
├─ adapters/               # 机器可读的 Agent 适配器目录
├─ plugins/
│  ├─ codex/agentbell/     # Codex 插件
│  ├─ claude/agentbell/    # Claude Code 插件
│  ├─ kimi/agentbell/      # Kimi Code CLI 插件
│  ├─ opencode/agentbell/  # OpenCode 插件
│  └─ qoder/agentbell/     # Qoder 插件
├─ schemas/                # 统一事件协议
├─ scripts/                # 仓库校验脚本
└─ docs/                   # 架构与开发说明
```

## 本地开发与检查

当前协议原型要求 Node.js 20 或更高版本；仓库开发工具使用 Node.js 20.19 或更高版本，
推荐 Node.js 24。

```bash
npm ci
npm run ci
npm run perf:emit
npm run doctor
npm run setup:plan
```

`doctor` 和 `setup:plan` 在源码仓库中检查 bootstrap 环境，不修改用户环境。安装 Core 后，
`agentbell doctor --json` 由原生 Core 提供运行诊断。

仓库已配置 GitHub Actions：Pull Request 和 `main` 推送会执行 Node/Go 跨平台测试、
race detector、三平台 emit 性能门禁和六目标构建。`vX.Y.Z` 标签会生成 checksum、
Technical Preview manifest、构建证明和 GitHub Release；npm Trusted Publisher 就绪后
再发布 workspace。

当前 Core 命令面（`setup`/`test` 与服务管理为 M1 本地开发能力）：

```text
agentbell version --json
agentbell setup [--dry-run|--json]
agentbell test [--channel <id>] [--json]
agentbell emit --adapter codex --surface cli --runtime host --stdin
agentbell service <install|status|uninstall>
agentbell service run --foreground
agentbell doctor --json
agentbell queue list --state dead
agentbell queue retry <event-id>
agentbell adapter <detect|plan|install|verify|uninstall|diagnose> <codex|claude-code|kimi-code|opencode|qoder>
agentbell adapter uninstall all [--dry-run]
agentbell uninstall [--dry-run] [--json]
```

## 目标体验

```bash
npx @agentbell/cli@latest setup
```

这条命令（M1 切片 1 起由 Core 实现，macOS 已实机验收）负责：

- 检测已安装的 Codex、Claude Code、Kimi Code、OpenCode、Qoder；
- 在用户确认后安装飞书官方 `lark-cli`；
- 引导完成飞书应用配置和最小范围登录授权；
- 创建或选择通知会话，并为它命名；
- 安装对应 Hook（当前支持 Codex、Claude Code、Kimi Code、OpenCode 与 Qoder）；
- 按平台安装登录自启动后台服务，并用 `agentbell test` 发送测试通知。
- 用 `agentbell uninstall` 一次预检并移除后台服务与五个产品 Hook；npm bootstrap 随后
  删除其管理的 Core 版本，默认保留配置和队列。

更完整的设计见 [架构说明](./docs/architecture.md)、[兼容矩阵](./docs/compatibility.md)、
[适配器协议](./docs/adapter-contract.md)、[安装与运维](./docs/operations.md)、
[M0.5 验收记录](./docs/m0.5-validation.md)、[M0.5 执行计划](./docs/m0.5-execution-plan.md)、
[M1 setup 验收记录](./docs/m1-setup-validation.md) 和 [CI/CD 与发布](./docs/ci-cd.md)。

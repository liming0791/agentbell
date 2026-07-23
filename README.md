# AgentBell

AgentBell 是一个面向 Coding Agent CLI、IDE 和 Desktop Agent 的跨平台通知适配层。它不自建消息中间件，而是：

1. 为支持确定性扩展的 Agent 安装生命周期 Hook 或插件。
2. 把不同 CLI 的事件归一化成统一通知事件。
3. 调用飞书官方 `lark-cli`，把完成、失败或等待授权等事件发送到指定会话。

当前版本为 `v0.2.0-rc.1` Technical Preview：Go 单文件 Core、持久队列、前台
Service、Codex 参考 Adapter、npm bootstrap 和六目标 Release 流水线已经实现。Node.js
Hook runtime 仅保留为 M0 迁移期原型，不是正式 Hook 数据面。

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
│  └─ hook-runtime/        # Hook 输入归一化与飞书发送
├─ adapters/               # 机器可读的 Agent 适配器目录
├─ plugins/
│  ├─ codex/agentbell/     # Codex 插件
│  ├─ claude/agentbell/    # Claude Code 插件
│  └─ kimi/agentbell/      # Kimi Code CLI 插件
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

M0.5 命令面：

```text
agentbell version --json
agentbell emit --adapter codex --surface cli --runtime host --stdin
agentbell service run --foreground
agentbell doctor --json
agentbell queue list --state dead
agentbell queue retry <event-id>
agentbell adapter <detect|plan|install|verify|uninstall|diagnose> codex
```

## 目标体验

```bash
npx @agentbell/cli@latest setup
```

正式版本中，这条命令将负责：

- 检测已安装的 Codex、Claude Code、Kimi Code CLI；
- 在用户确认后安装飞书官方 `lark-cli`；
- 引导完成飞书应用配置和最小范围登录授权；
- 创建或选择通知会话，并为它命名；
- 安装对应 Hook，发送测试通知。

更完整的设计见 [架构说明](./docs/architecture.md)、[兼容矩阵](./docs/compatibility.md)、
[适配器协议](./docs/adapter-contract.md)、[安装与运维](./docs/operations.md)、
[M0.5 验收记录](./docs/m0.5-validation.md)、[M0.5 执行计划](./docs/m0.5-execution-plan.md)
和 [CI/CD 与发布](./docs/ci-cd.md)。

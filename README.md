# AgentBell

AgentBell 是一个面向 Coding Agent CLI、IDE 和 Desktop Agent 的跨平台通知适配层。它不自建消息中间件，而是：

1. 为支持确定性扩展的 Agent 安装生命周期 Hook 或插件。
2. 把不同 CLI 的事件归一化成统一通知事件。
3. 调用飞书官方 `lark-cli`，把完成、失败或等待授权等事件发送到指定会话。

当前仓库是可运行、可校验的项目骨架，重点先固定边界、目录和插件格式。现有 Node.js 代码是协议原型；面向大众发布的正式 Core 已确定采用 Go 单文件程序，以覆盖 Windows、macOS 和 Linux，并降低 GUI 应用找不到 Node.js/PATH 的风险。

## 首期范围

- 确定性接入：Codex CLI/Desktop、Claude Code CLI/Desktop、OpenCode CLI/Desktop、Kimi Code CLI、Qoder CLI/IDE。
- 技术验证接入：ZCode、Tencent WorkBuddy、TRAE IDE。
- 等待官方 Hook：Kimi Work；不使用 Skill/MCP 软触发冒充生命周期通知，不进入首期运行时适配范围。

各产品的证据、等级和限制见 [兼容矩阵](./docs/compatibility.md)，被厂商能力阻塞的事项见 [项目待办](./TODO.md)。

## 目录

```text
agentbell/
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
npm run doctor
npm run setup:plan
```

`setup:plan` 只输出安装计划，不会修改用户环境，也不会安装 `lark-cli`。

仓库已配置 GitHub Actions：Pull Request 和 `main` 推送会执行跨平台测试，`vX.Y.Z`
标签会通过 GitHub OIDC 发布两个 npm workspace 并创建 GitHub Release。首次连接远程
仓库和 npm Trusted Publisher 的步骤见 [CI/CD 与发布](./docs/ci-cd.md)。

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

更完整的设计见 [架构说明](./docs/architecture.md)、[兼容矩阵](./docs/compatibility.md)、[适配器协议](./docs/adapter-contract.md)、[产品决策](./docs/decisions.md)、[项目待办](./TODO.md)、[开发路线](./docs/development.md) 和 [CI/CD 与发布](./docs/ci-cd.md)。

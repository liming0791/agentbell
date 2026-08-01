# AgentBell

AgentBell 是一个面向 Coding Agent CLI、IDE 和 Desktop Agent 的跨平台通知适配层。它不自建消息中间件，而是：

1. 为支持确定性扩展的 Agent 安装生命周期 Hook 或插件。
2. 把不同 CLI 的事件归一化成统一通知事件。
3. 调用飞书官方 `lark-cli`，把完成、失败或等待授权等事件发送到指定会话。

不同产品只发送能够被官方 Hook 载荷确定证明的状态。Codex 当前不能在
`PermissionRequest` 中区分人工审批与原生 `auto_review`，因此暂不发送 Codex
审批提醒；完成通知继续使用 `Stop`。

最新已发布版本为
[`v0.3.0-rc.6`](https://github.com/liming0791/agentbell/releases/tag/v0.3.0-rc.6)
Technical Preview。该版本已包含 Go 单文件 Core、持久队列、七个 Adapter、
`agentbell setup`、`agentbell test`、三平台用户服务及 M2 本地能力
（见 [M1 验收记录](./docs/m1-setup-validation.md) 和
[M2 验收台账](./docs/m2-validation.md)）。RC4 因原 npm scope 与既有第三方组织冲突
而保持未公开 Draft；RC5 已改用当前账号所有的 `@liming0791` scope，并发布
`@liming0791/agentbell-cli` 与 `@liming0791/agentbell-hook-runtime`。RC6 修复 setup/test
无法证明真实用户可见通知群的问题，并让 Windows 登录后台服务不再弹出常驻控制台。
仓库与 Release 均公开，npm bootstrap 可匿名下载原生 Core；Node.js Hook runtime
仅保留为 M0 迁移期原型，不是正式 Hook 数据面。

## 首期范围

- 确定性接入：Codex CLI/Desktop、Claude Code CLI/Desktop、OpenCode CLI/Desktop、Kimi Code CLI、Qoder CLI/IDE。
- M1.5 Pilot：QoderWork、TRAE IDE。
- 后续调研：ZCode、Tencent WorkBuddy；两者不进入 M1.5。
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
npm run check:docs
npm run perf:emit
npm run perf:m2
npm run smoke:https  # Linux / Ubuntu CI
npm run doctor
npm run setup:plan
```

`doctor` 和 `setup:plan` 在源码仓库中检查 bootstrap 环境，不修改用户环境。安装 Core 后，
`agentbell doctor --json` 由原生 Core 提供运行诊断。

仓库已配置分层 GitHub Actions：Draft PR 优先执行完整质量门禁，Ready PR 和直接
`main` push 执行 Node/Go 跨平台测试、race detector、三平台 emit 性能门禁、真实
TLS/HTTPS Relay smoke 和六目标 Core + bridge 构建；Markdown-only 改动走无依赖的
轻量文档检查。`vX.Y.Z` 标签会生成 checksum、Technical Preview manifest、构建证明
和 GitHub Release；两个 npm workspace 已配置 GitHub OIDC Trusted Publisher，发布
流程见 [CI/CD 与发布](./docs/ci-cd.md)。

当前 Core 命令面（`setup`/`test` 与服务管理为 M1 本地开发能力）：

```text
agentbell version --json
agentbell setup [--dry-run|--json]
agentbell test [--channel <id>] [--json]
agentbell emit --adapter codex --surface cli --runtime host --stdin
agentbell service <install|status|restart|uninstall>
agentbell service run --foreground
agentbell doctor --json
agentbell queue list --state dead
agentbell queue retry <event-id>
agentbell settings <show|channel|event|template|quiet-hours> ...
agentbell policy <status|explain> ...
agentbell bind <create|complete|status|cancel> ...
agentbell hook <conflicts|reconcile> [all|codex|claude-code|kimi-code] ...
agentbell bridge doctor --json
agentbell plugin verify <bundle> [--json]
agentbell relay <configure|run|bind create|peers list|peers revoke|receipts list> ...
agentbell relay connector <add|list|remove|pair> ...
agentbell remote <configure|pair|test|emit|drain> ...
agentbell adapter <detect|plan|install|verify|uninstall|diagnose> <codex|claude-code|kimi-code|opencode|qoder|qoder-work|trae>
agentbell adapter uninstall all [--dry-run]
agentbell uninstall [--dry-run] [--json] [--delete-remote-credential --confirm-delete-remote-credential]
```

RC6 包含 M2 的一次性绑定、完整 Channel 事务、stable Hook/Service bridge、
`service restart`、Hook 冲突审计、受 sidecar/部分投递账本保护的 upgrade/rollback、
`plugin verify` 与 Release keyless 插件签名，以及 relay pairing/ingress、远端
metadata-only outbox、`remote test`、独立 Host connector registry、WSL/SSH/container
无监听配对/拉取、HTTPS push 和后台调度。真实旧 Release 到本地候选的 lifecycle、
macOS Host→Linux container stdio 以及隔离 Linux container TLS/HTTPS E2E 已通过。
macOS 真实 M1 形态的 LaunchAgent 也已备份后迁移到 stable bridge，并通过后台飞书
投递；真实 macOS 还已完成上一公开 Release 安装、最终 Draft 升级、旧版回滚、后台
飞书发送和统一卸载。它们仍是 Technical Preview：macOS 断网恢复、Windows/Linux
实机和独立跨主机端到端尚未补齐，不能把这些局部证据解释为完整可用的 M2 产品流程。
准确进度见
[M2 实施计划](./docs/m2-execution-plan.md#当前实现进度)。

## 目标体验

```bash
npx @liming0791/agentbell-cli@next install-core --version 0.3.0-rc.6
npx @liming0791/agentbell-cli@next setup
```

这组命令（M1 切片 1 起由 Core 实现，macOS 已实机验收）负责：

- 检测已安装的 Codex、Claude Code、Kimi Code、OpenCode、Qoder、QoderWork、TRAE；
- 在用户确认后安装飞书官方 `lark-cli`；
- 引导完成飞书应用配置和最小范围登录授权；
- 创建或选择通知会话，并为它命名；
- 安装对应 Hook（当前支持 Codex、Claude Code、Kimi Code、OpenCode、Qoder、
  QoderWork 与 TRAE）；
- 按平台安装登录自启动后台服务，并用 `agentbell test` 发送测试通知。
- 用 `agentbell uninstall` 一次预检并移除后台服务与七个产品 Hook；npm bootstrap 随后
  删除其管理的 Core 版本，默认保留配置、队列、远程 sidecar/peer 和私钥。私钥删除
  必须同时给出删除与二次确认参数。

更完整的设计见 [架构说明](./docs/architecture.md)、[兼容矩阵](./docs/compatibility.md)、
[适配器协议](./docs/adapter-contract.md)、[安装与运维](./docs/operations.md)、
[M0.5 验收记录](./docs/m0.5-validation.md)、[M0.5 执行计划](./docs/m0.5-execution-plan.md)、
[M1 验收记录](./docs/m1-setup-validation.md)、[M1.5 验收记录](./docs/m1.5-validation.md)
、[M2 实施计划](./docs/m2-execution-plan.md)、[M2 验收台账](./docs/m2-validation.md)
和 [CI/CD 与发布](./docs/ci-cd.md)。

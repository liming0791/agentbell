# 开发路线

## M0：仓库骨架

- Node.js monorepo。
- 统一事件 Schema。
- Codex、Claude Code、Kimi Code CLI 插件清单与 Hook。
- 无副作用的环境检测和安装计划。
- Hook 运行时的飞书消息发送接口。

## M0.5：产品 Core 与 Adapter SDK

- 状态：退出验收通过，发布 `v0.2.0-rc.2` Technical Preview。
- 执行计划：[M0.5 执行计划](./m0.5-execution-plan.md)。
- [x] 将正式 Core 迁移为 Go 单文件二进制；Node.js 保留为 npm bootstrap。
- [x] 实现 `agentbell emit`、本地持久队列和前台用户级 Service。
- [x] 实现 Adapter manifest、dry-run、安全 JSON 合并和精确卸载。
- [x] 完成 Codex Adapter 生命周期与三平台 fixture。
- [x] 建立 Windows x64/arm64、macOS Intel/Apple Silicon、Linux x64/arm64 构建流水线。
- [x] 给事件协议补充 Surface、运行位置、幂等键、隐私级别和优先级。
- [x] 为未签名产物建立 `technical-preview` gate；正式签名留在 M1。

## M1：本机安装闭环与 Verified Adapters

- `agentbell setup` 检测 CLI 和操作系统。
- 经用户确认后运行 `npx @larksuite/cli@latest install`。
- 引导 `lark-cli config init --new`、`lark-cli auth login --domain im`。
- 获取或创建目标会话，写入 `~/.agentbell/config.json`。
- 完成 Codex、Claude Code、OpenCode、Kimi Code、Qoder 的 CLI/Desktop 本地适配。
- 注册系统登录自启动，提供 `agentbell test` 和产品级统一卸载。

## M1.5：首期 Desktop Pilot

- ZCode：确认插件 manifest、Hook schema、Stop/等待事件和 marketplace 安装路径。
- WorkBuddy：确认第三方 Hook 插件的技术格式和生命周期事件。
- TRAE：按版本、地区和账号动态探测 Hooks 是否可用。
- 每个 Pilot 完成实机矩阵后再升级为 Verified。

## M2：大众产品体验与远程环境

- 用一次性绑定码承接桌面 CLI 与飞书会话的关联。
- 提供“通道名称、通知模板、事件开关、免打扰时间”设置。
- 支持升级、回滚、Hook 冲突检测和插件签名校验。
- 扩展跨主机、跨 relay 的幂等和团队级事件策略。
- 增加 WSL Host Bridge；SSH、容器和 Vendor Cloud 使用显式远程 shim/relay。

## M3：平台化

- 插件市场或一条安装指令分发。
- 兼容更多 Coding Agent CLI。
- 持续跟踪 Kimi Work 官方生命周期 Hook；满足 [TODO.md](../TODO.md) 的解锁条件后才创建 Pilot Adapter。
- 提供企业管理员策略、审计与团队通道。
- 在统一事件协议之上增加其他通知传输层。

## 验证矩阵

每个 Verified Adapter 至少覆盖：

- Windows PowerShell / cmd；涉及 OpenCode 时补充 WSL。
- macOS Intel / Apple Silicon。
- Ubuntu/Debian 和一种 RPM 系发行版。
- shell 启动与 GUI 启动两种 PATH 环境。
- 完成、失败、等待授权、离线重试、重复事件和卸载恢复。

## 本地开发

```bash
npm ci
npm run ci
npm run doctor
npm run setup:plan
```

`npm run ci` 包含 lint、结构检查、Node/Go 覆盖率门禁和 npm 打包预检。性能门禁使用
`npm run perf:emit`。跨平台 CI、版本同步、npm Trusted Publishing 和 GitHub Release
流程见 [CI/CD 与发布](./ci-cd.md)，运行与恢复见 [安装与运维](./operations.md)。

调试 M0 迁移期 Node Hook 原型（正式 M0.5 数据面使用 Go Core）：

```bash
echo '{"hook_event_name":"Stop","cwd":"/tmp/demo"}' \
  | node packages/hook-runtime/bin/agentbell-hook.mjs --source codex --dry-run
```

Windows PowerShell：

```powershell
'{"hook_event_name":"Stop","cwd":"C:\\work\\demo"}' |
  node .\packages\hook-runtime\bin\agentbell-hook.mjs --source codex --dry-run
```

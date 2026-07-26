# AgentBell 兼容矩阵

更新时间：2026-07-26。产品能力变化很快；每次发布 AgentBell 适配器前必须重新跑版本探测和端到端测试。

## 等级说明

| 等级 | 含义 | 对用户的承诺 |
| --- | --- | --- |
| A — Verified | 有公开、确定性的生命周期 Hook/事件 | 在标明的 Surface 和运行位置中可承诺稳定触发 |
| B — Pilot | 官方存在 Hook/插件入口，但规范、版本或开放范围仍需实机确认 | 可进入首期 Beta，不宣传完全兼容 |
| C — Assisted | 只能靠 Skill/MCP/提示词约定或厂商自己的通知 | 尽力触发，不承诺每次必达 |
| D — Unsupported | 没有公开、安全、稳定的事件入口 | 不做进程注入、私有数据库监听或 UI 猜测 |

## 首期产品范围

| 产品 | Surface | 官方入口 | 关键事件 | 平台/范围 | 当前等级 | 首期动作 |
| --- | --- | --- | --- | --- | --- | --- |
| Codex | CLI | `hooks.json`、插件 Hook | `Stop` | Windows/macOS/Linux | B | Go Adapter 已实现；macOS 0.146 Stop 黑盒复验通过（2026-07-25），Windows/Linux 产品实机待验 |
| Codex | ChatGPT Desktop 本地代码会话 | 与 CLI 共享 Codex 配置层和 Hook | `Stop` | 以厂商支持平台为准；仅本地会话 | B | 与 CLI 共用 Go Adapter；Hook 重排后须重新信任并分叉/新建任务，macOS 已定位旧任务不热加载 |
| Claude Code | CLI | settings Hook、插件 Hook | `Stop`、`StopFailure`、`Notification`、`PermissionRequest` | Windows/macOS/Linux | B | Go Adapter 已实现并通过三平台 fixture；产品实机矩阵待验 |
| Claude Code | Desktop Code tab | 与 CLI 共享 settings、Hooks 和插件 | 同上 | Windows/macOS/Linux beta；本地会话 | B | 与 CLI 共用用户级 Go Adapter；Desktop 本地会话实机待验 |
| OpenCode | CLI/TUI | JS/TS/npm 插件事件 | `session.idle`、`session.error`、`permission.asked` | Windows/macOS/Linux | B | M1 新增 npm 插件 |
| OpenCode | Desktop | 配置适用于 Desktop，插件可订阅 Session 事件 | 同上 | Windows/macOS/Linux | B | M1 与 OpenCode CLI 共用适配器 |
| Kimi Code | CLI | TOML Hook、`kimi.plugin.json` | `Stop`、`StopFailure`、`PermissionRequest` | Windows/macOS/Linux | B | Go Adapter 已实现；macOS 实机验收通过（2026-07-25），Windows/Linux 待验；`Notification` 语义尚未完成验收，不声明支持 |
| Qoder | CLI/IDE/JetBrains 插件 | 共享 `settings.json` Hook、插件 Hook | `Stop`、`PostToolUseFailure` | CLI 三平台；IDE/JB 以厂商平台为准 | B | M1 新增 Claude-compatible dialect |
| ZCode | Desktop ADE | Beta 插件市场，官方列出 Hook 组件 | 待确认 | Windows/macOS；Linux beta | B | 做插件格式与 Stop 事件 spike |
| Tencent WorkBuddy | Desktop | 官方插件市场列出 Hook 插件，可加第三方市场 | 待确认 | Windows/macOS | B | 向厂商确认 Hook schema，并实机验证 |
| TRAE | IDE | v3.5.66 起官方更新日志声明支持 Hooks | 预计含 `Stop` | Windows/macOS；版本/账号分批开放 | B | 按版本和账号能力探测，不静态假设 |
| Kimi Work | Desktop Work mode | 官方 Plugin Center、Skill、定时任务 | 未公开生命周期 Hook | Windows/macOS | D / Waiting | 不进入一期适配器；等待公开、确定性的官方 Hook |

## 已确认的复用关系

1. Codex CLI 与 Codex Desktop 本地执行面共享 Codex 配置层，因此不需要两个完全独立的插件。
2. Claude Code Desktop 的 Code tab 使用同一底层引擎，并明确共享 settings、Hooks 和插件。
3. OpenCode 的配置适用于 TUI、CLI、Desktop 和 GitHub Action；插件可用 `session.idle` 识别一轮完成。
4. Qoder IDE/JetBrains 插件与 CLI 共享 Hook 配置，格式与 Claude Code 高度兼容。
5. Kimi Code 使用自己的 TOML/插件 Hook 格式，需要单独 dialect。

## 实际交付状态

机器可读 catalog 中所有可接入产品当前均为 `pilot`。Codex 已完成 Core Adapter
实现、生命周期 conformance fixture 和跨平台 CI，并在 macOS 使用 ChatGPT.app
内置 Codex 0.146 完成 CLI Stop 黑盒复验。Desktop 已确认共享
`~/.codex/hooks.json`，但非托管 Hook 的位置化信任和任务启动快照仍有稳定性约束，
因此不把本次 Desktop 结果升级为 Verified。
Claude Code 已完成共享 user-settings Go Adapter、三平台 conformance fixture 和
CLI/Desktop 配置复用实现；Kimi Code 已完成 Go Adapter 实现与 macOS CLI 实机验收
（2026-07-25）。OpenCode、Qoder 等产品仍保留 M0 协议原型或 catalog 记录。公开文档
证明产品存在确定性 Hook，不等于 AgentBell 已完成真实产品矩阵，因此不能据此标成
Verified。

## 公开证据

- Codex：[Hooks](https://learn.chatgpt.com/docs/hooks)、[配置基础](https://learn.chatgpt.com/docs/config-file/config-basic)
- Claude Code：[Desktop 共享配置](https://code.claude.com/docs/en/desktop)、[Hooks](https://code.claude.com/docs/en/hooks-guide)
- OpenCode：[Plugins 与 `session.idle`](https://opencode.ai/docs/plugins/)、[跨 Surface 配置](https://opencode.ai/docs/config/)
- Kimi Code：[Hooks](https://www.kimi.com/code/docs/en/kimi-code-cli/customization/hooks.html)、[Plugins](https://www.kimi.com/code/docs/en/kimi-code-cli/customization/plugins.html)
- Qoder：[IDE/CLI Hooks](https://docs.qoder.com/extensions/hooks)
- ZCode：[Beta 插件系统](https://zcode.z.ai/cn/docs/plugin)
- WorkBuddy：[插件系统](https://www.codebuddy.cn/docs/workbuddy/Plugins)
- TRAE：[Hooks 上线记录](https://www.trae.ai/changelog)
- Kimi Work：[Plugin Center](https://www.kimi.com/help/kimi-work/plugin-center)

## 首期交付分组

“第一期覆盖”拆成两个对用户透明的通道：

### Phase 1 GA（目标，尚未达成 Verified）

- Codex CLI + Desktop 本地会话
- Claude Code CLI + Desktop Code tab 本地会话
- OpenCode CLI + Desktop
- Kimi Code CLI
- Qoder CLI + IDE

### Phase 1 Beta

- ZCode
- Tencent WorkBuddy
- TRAE IDE

上述产品都会出现在第一期产品中，但安装器会显示 Verified 或 Pilot，不会用一个模糊的“已支持”掩盖可靠性差异。后续出现只能软触发但仍有明确用户价值的产品时，可以使用 Assisted 等级。

### Waiting / Roadmap

- Kimi Work：记录在产品目录和 [TODO.md](../TODO.md) 中，但 `phase1=false`，不会生成或安装适配器。只有官方公开确定性生命周期 Hook 并满足适配器验收门槛后才进入 Pilot。

## 后续优先候选

下列产品已有 Hook 或类似扩展信号，适合在相同框架中继续接入：

- Gemini CLI
- GitHub Copilot Agent / VS Code Agent Hooks
- Tencent CodeBuddy CLI/IDE
- Factory Droid
- Cursor、Windsurf、Cline、Roo Code、Aider、Amp

新增产品前先进入 `discover -> prototype -> verified` 流程，不以“能安装 Skill”直接等同于“能可靠收到完成事件”。

## 当前未解决问题

1. ZCode 官方文档说明插件包含 Hook，但未公开完整 Hook schema 和事件清单。
2. WorkBuddy 公开文档说明支持 Hook 插件和第三方市场，但缺少可直接实现的生命周期技术参考。
3. TRAE 从 v3.5.66 起声明支持 Hooks，版本、地区、个人/企业账号的可见性可能不同。
4. Kimi Work Plugin Center 没有公开第三方生命周期 Hook；项目已明确延期，不用 Skill/MCP 软触发替代。
5. Claude/Codex Desktop 的远程或云会话不能自动视为本机 Hook；需按任务实际执行位置判断。
6. Codex `PermissionRequest` 当前不暴露有效审批人；AgentBell 不发送该事件，避免把
   原生 `auto_review` 误报成人工待审批。恢复条件见 [TODO](../TODO.md)。
7. Codex 没有公开的已有任务 Hook 重载接口；CLI `/hooks` 只适合审核当前已发现的
   Hook，公开文档没有为 Desktop/IDE 提供等价的重载接口。首次安装后的可靠激活仍需
   分叉或新建任务，恢复条件见 [TODO](../TODO.md)。

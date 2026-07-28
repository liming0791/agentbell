# AgentBell 产品决策记录

## D1：正式 Core 技术栈

状态：已确认（2026-07-23）。

决策：使用 Go 单文件 Core，Node.js 只做 npm bootstrap 和协议原型。

原因：

- GUI Agent 不一定继承终端 Node.js/PATH。
- Hook 需要低冷启动和跨平台绝对路径。
- 用户不应为了通知工具维护另一套 JavaScript 运行环境。

正式版本不以用户本机已安装 Node.js 作为 Hook 运行前提。

## D2：“第一期覆盖”的口径

状态：已确认（2026-07-23）。

决策：Phase 1 交付按能力公开标记三种等级：

- Verified：确定性 Hook，完成实机验收。
- Pilot：官方已有 Hook/插件，但版本或规范仍在变化。
- Assisted：Skill/MCP 软触发，明确不承诺每次必达。

不能为了营销数字把三种能力都写成“完全支持”。被厂商能力阻塞、尚无安全接入方式的产品进入 Waiting，不计入 Phase 1 已交付适配器；D4 是 Kimi Work 的明确例外处理。

## D3：运行位置范围

状态：已确认（2026-07-23）。

决策：

- Windows、macOS、Linux 本机为 GA 范围；
- Windows WSL 为首期 Beta；
- Docker、SSH Remote、Vendor Cloud 放到后续阶段。

首期不把“桌面系统已支持”扩张解释为该系统上的所有远程、容器和云任务均已支持。

## D4：Kimi Work 等无公开生命周期 Hook 的产品

状态：已确认（2026-07-23）。

决策：Kimi Work 暂不实现 Assisted Adapter，不进入 Phase 1 运行时适配范围。等待厂商提供公开、确定性的生命周期 Hook 后再开发。

解锁条件：

1. 官方提供能区分完成、失败、等待输入或授权的生命周期事件；
2. 官方文档公开第三方 Hook/插件的安装位置、事件输入结构和兼容版本；
3. Hook 可以 fail-open，不因 AgentBell 或网络故障阻塞原任务；
4. 安装、验证、升级和卸载可以稳定自动化。

在此之前，不以 transcript/log 轮询、UI 注入、自然语言提示或 Skill/MCP 主动调用替代生命周期 Hook。跟踪项见项目根目录 [TODO.md](../TODO.md)。

## D5：Desktop 产品的厂商原生通知

状态：待确认。

ZCode、Kimi Work、WorkBuddy 已经具备不同程度的飞书、微信、Bot 或小程序能力。推荐 AgentBell 同时支持两种模式：

1. Unified：Hook 进入 AgentBell，再统一走用户配置的飞书通道。
2. Native delegated：安装器帮助用户开启厂商自带的手机通知，但明确消息不经过 AgentBell。

Unified 便于统一模板、通道和审计；Native delegated 可以更快覆盖没有公开 Hook 的产品。

## D6：Codex 审批通知的语义门禁

状态：已确认（2026-07-25）。

决策：Codex `PermissionRequest` 缺少明确的当前回合
`approvals_reviewer=user` 时，不发送 `approval.required`。安装器只保留 Stop，并精确
移除旧版 AgentBell 的 PermissionRequest；Core 继续抑制旧任务快照发来的模糊事件。

原因：该 Hook 在原生审批路由之前触发，`permission_mode=default` 同时覆盖人工审核和
`auto_review`。通知“需要你审批”必须证明用户真的要采取动作，不能把自动审核过程误报
成人工待办。恢复条件由项目根 [TODO](../TODO.md) 跟踪。

## D7：M1.5 Desktop / IDE Adapter 范围

状态：已确认（2026-07-26）。

决策：

- QoderWork 作为独立 Adapter/profile 进入 M1.5；官方 schema 复核后确认它使用单个
  shell command，而 Qoder 使用 exec-form `command + args`，因此只共享通用 JSON
  合并基础设施，不共享 dialect、配置所有权、检测、重启与卸载。
- TRAE IDE Adapter 进入 M1.5；本决策不外推到独立的 TRAE Work Desktop 产品。
- 现有五个 Adapter 的 IDE 表面只进入待实机验收矩阵，不创建开发任务。
- ZCode 与 WorkBuddy 不进入 M1.5；Kimi Work 继续等待公开生命周期 Hook。

原因：M1.5 只纳入已发现公开、可验证 Hook 且需要独立产品生命周期管理的表面，避免把
共享协议误当成共享 Adapter，或用调研项扩大交付范围。

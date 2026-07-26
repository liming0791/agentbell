# AgentBell M1 切片 1 验收记录：setup 本机安装闭环

验收目标：`agentbell setup` / `agentbell test` 在 macOS 本机完成真实飞书闭环。
验收日期：2026-07-25；环境：macOS 26.4 arm64、Go 1.26.5、Node.js 22.22、lark-cli 1.0.30、
Codex 0.142.5。Core 为本地 dev 构建（`go build ./cmd/agentbell`），未走 Release。

## 范围

本切片交付：setup 环境检测、lark-cli 安装/初始化/登录引导、搜索或创建飞书会话并
原子写入平台目录 `config.json`、（经确认）安装 Codex Adapter、`agentbell test` 直连
通道验证、npm bootstrap 代理 `setup`/`test`、配置路径统一为平台目录。

原切片不包含 Claude Code/Kimi Code/OpenCode/Qoder 的 Go Adapter、登录自启动、
统一卸载、Windows/Linux 实机验证、版本发布。2026-07-25 同日后续工作已追加
Codex/Claude Code/Kimi Code Go Adapter、三平台登录服务和产品级统一卸载；新增验证见下文。

## 自动门禁

- `npm run ci` 全绿（lint、结构检查、Node 覆盖率、Go 覆盖率、pack 预检），2026-07-25
  最终工作树执行 exit 0。
- Go 覆盖率：总 79.4%（门禁 75%）；`event` 95.8%、`queue` 83.3%、
  `adapter` 83.0%、`setup` 83.2%（四个包门禁各 80%）。
- 新增测试：`config.Save` 写盘/校验/幂等；setup 全流程 fake Runner/Prompter 分支
  （含 dry-run 零副作用、已有配置合并备份、bot 建群邀请用户、损坏配置拒绝覆盖）；
  app 层 setup/test 命令用例；Node 侧 bootstrap 代理与平台路径用例。

## 真实链路证据（macOS 本机）

1. `agentbell setup --dry-run`：正确检测 codex/claude/kimi（PATH）、opencode
   （配置目录）和 lark-cli，输出计划且零副作用。
2. 交互 `agentbell setup`：复用已有 lark-cli 配置与登录；新建私有群
   「AgentBell 通知」（chat ID 已脱敏），bot 身份建群时通过
   `auth status` 的 `userOpenId` 邀请用户入群；配置原子写入
   `~/Library/Application Support/AgentBell/config.json`；确认后安装 Codex 钩子。
3. `adapter verify codex --json`：`installed: true`；用户已有 cmux 钩子完整保留
   （结构化合并）。
4. `agentbell test --json`：`ok: true`，测试消息真实到达飞书群（用户目检确认）。
5. 模拟 Codex Stop 事件经 hooks.json 中安装的命令行入队：
   `emit` exit 0 → Service 消费 → `history` 记录 `state: succeeded`，
   飞书收到 `task.completed` 通知（项目 agentbell，用户目检确认）。
6. 幂等：同一事件重复 emit，history 仍为 1 条，未重发。
7. 隐私：history 中 sessionId 为 `sha256:` 哈希，`privacyLevel: metadata-only`，
   无 cwd/summary/原始 Hook JSON。
8. `doctor --json`：config/larkCli 均 ok，队列 pending/inflight/dead 为 0。

## 验收中发现并修复的真实环境问题

- Codex 桌面（ChatGPT.app 内置 Codex）实机确认：桌面执行面共享 `~/.codex/hooks.json`，
  Stop 与 PermissionRequest 载荷与 CLI 同构（`hook_event_name`/`session_id`/
  `turn_id`/`cwd`）。后续 0.146 复验推翻了“动态读取后旧任务无需重启”的结论：
  非托管 Hook 被其他工具重排后，位置化信任会失效；重新审核的信任只在新任务稳定生效。
- Kimi Code CLI 实机验收（2026-07-25）：`adapter install kimi-code` 向
  `~/.kimi-code/config.toml` 追加标记分隔的 `[[hooks]]` 区域（Stop/StopFailure/
  PermissionRequest），`kimi -p` 非交互会话结束触发 Stop → `task.completed` 成功投递。
  Kimi 约束：`[[hooks]]` 每条只允许 event/matcher/command/timeout 四字段。
- `transport` 发送时多传 `--format json`：`lark-cli im +messages-send` 不支持该
  参数（M0.5 从未真实发送，未暴露）。已移除并补测试。
- bot 身份 `+chat-create` 建群不包含用户本人：setup 建群时解析
  `lark-cli auth status` 的 `identity`/`userOpenId`，bot 身份下自动 `--users` 邀请，
  否则用户收不到通知。已补测试。
- Codex Adapter 安装时向 hooks.json 顶层写入 `description` 字段：Codex 0.142.5
  严格解析 hooks.json，未知顶层字段导致整个文件被忽略，所有钩子（含用户已有钩子）
  失效。已改为不写入、重复安装时自动清除遗留字段，并补自愈回归测试；本机
  hooks.json 已通过重装修复（原文件有备份）。该问题由桌面 Codex 不触发通知的
  实机报告发现（2026-07-25），fixture 测试无法覆盖真实 Codex 的解析行为。
- Kimi Code 0.29.1 的 Stop 载荷只有 `session_id`，旧幂等键把同一会话后续所有 Stop
  永久折叠；PermissionRequest 的 `turn_id` 还可能是数字。Core 现接受字符串/数字标识，
  并在缺少 turn/task/tool 标识时加入发生时间，保留同会话后续回合。
- 原 Service 是从 Kimi shell 临时拉起的子进程，退出后没有系统托管；同时继承的 NVM
  PATH 与 Codex App GUI 环境不同。现由 macOS LaunchAgent 使用绝对 Core 路径常驻，
  `service install/status/uninstall` 负责生命周期，配置持久化绝对 `larkCliPath`。
- `adapter verify` 只能证明配置文件结构正确，不能证明 Codex 已信任 Hook 或 Kimi
  新会话已加载 Hook。`emit` 现按事件写入不含载荷/标识的 runtime proof；Codex
  `diagnose` 只接受配置变更之后的 `task.completed` proof，审批事件不能代替完成链路。

## Hook 稳定性修复复验

- 2026-07-25 最终完整 `npm run ci` 通过：Node 32 项；Go 总覆盖率 79.4%，
  `event` 95.8%、`queue` 83.3%、`adapter` 83.0%、`setup` 83.2%；结构检查与
  npm pack 预检通过。
- 收尾审计移除了未完成的 Codex `config.toml` 自动信任分支，并让 Kimi 复用 Adapter
  包内的原子写实现；Codex 仍以产品 `/hooks` 审核为信任边界，避免半合并代码导致
  Go 包无法编译。
- 修复版 Core 部署到 Codex/Kimi Hook 共用的绝对路径；macOS LaunchAgent
  `com.agentbell.service` 保持 running，stderr 为空，`doctor` 显示 config/larkCli
  为 ok 且 pending/inflight/dead 均为 0。
- 旧版 `diagnose codex` 曾被晚于 hooks.json 的 PermissionRequest proof 置为
  `runtimeVerified: true`，但这不能证明 Stop 健康；该结果已判定为假阳性并由按事件
  proof 取代。
- 隔离队列用同一 Kimi session 连续注入两次 Stop，得到两个不同 idempotency key 和
  两条 pending 事件，证明后续回合不再被永久折叠。
- Kimi Code 仍需在修复部署后启动新会话并完成一轮真实事件，才能补齐
  `diagnose kimi-code` 的 runtime proof；不得用旧会话结果冒充通过。

## M1 Adapter 与跨平台服务补齐

- Claude Code Go Adapter 结构化合并用户级 `settings.json`，使用绝对 Core 路径和
  exec-form 参数数组，覆盖 Stop/StopFailure/Notification/PermissionRequest；安装、
  重复安装、静态验证、runtime proof、精确卸载和三平台路径 fixture 已自动化。
- 官方资料确认 Claude Code CLI 与 Desktop Code tab 本地会话共享用户级 settings 与
  Hooks；实现因此复用一个 Adapter。Desktop 云会话不计入本机支持范围。
- Codex Adapter 在无 CLI PATH、但存在共享 `CODEX_HOME` 时也能检测 Desktop 安装；
  CLI 与 Desktop 继续共用 `hooks.json` 和 `/hooks` 哈希信任边界。
- `service install/status/uninstall` 新增 Windows 当前用户 ONLOGON 计划任务，以及
  Linux systemd user；无法连接 systemd user manager 时回退 XDG Autostart。
- `agentbell adapter uninstall all` 先预检再精确卸载 Codex、Claude Code、Kimi Code
  Hook，保留其他用户配置。
- `agentbell uninstall` 在同一预检流程中再停止并删除平台登录服务，默认保留配置、
  队列与诊断数据；npm bootstrap 等 Core 退出后仅删除受管的当前版本目录。
- 上述 Windows/Linux 与 Claude Desktop 结论当前来自官方协议、自动 fixture 和跨目标
  构建，不冒充真实机器登录重启或产品端到端验收。

## Codex Desktop 0.146 稳定性复验

- 复验时间：2026-07-25；ChatGPT.app 内置 `codex-cli 0.146.0-alpha.3.1`。
- 当前 Desktop 任务的 rollout 记录了 5 次 `task_complete`，AgentBell history 同期没有
  Codex `task.completed`；队列为空、Service running、stderr 为空，排除过滤和发送失败。
- 最后一次成功 Stop 在 19:29，`hooks.json` 于 19:30 被 cmux 重排；AgentBell Stop 的
  信任到 20:35 才更新。当前 Desktop 任务 19:39 启动，后续回合未热加载新信任。
- 使用 ChatGPT.app 同一内置 Codex 新开隔离 CLI 回合（正常信任模式、AgentBell state
  重定向到 `/tmp`）后，Stop 成功生成 `task.completed`，证明 Core、命令、载荷和
  Codex 0.146 Stop 实现本身正常，故障边界在旧 Desktop 任务的 Hook 信任快照。
- Codex `PermissionRequest` 在原生自动审核之前触发，公开载荷没有有效
  `approvals_reviewer`；AgentBell 现移除旧审批 Hook，并在 Core 侧抑制遗留任务发来的
  模糊审批事件。未来只有载荷明确声明 `approvals_reviewer=user` 才放行。
- 安装/重装后的验收步骤固定为：在 `/hooks` 审核 Stop → 新建任务 → 完成一轮 →
  `adapter diagnose codex` 必须看到新的 `task.completed` proof。
- 2026-07-26 文档复核确认：CLI `/hooks` 是审核当前已发现 Hook 的入口，但公开命令面
  没有 Hook 重载命令；公开文档也没有为 Desktop/IDE 提供等价接口。CLI `/fork` 或
  退出后 `codex resume --last`、Desktop/IDE 的分叉任务能力（当前版本可用时）可以
  保留全部或一份对话上下文并建立新运行时，但不属于同一运行时原地热加载。首次安装
  仍以新任务产生 runtime proof 为验收标准。
- 修复后再次完成六目标构建；本机 `emit` 性能门禁为 p95 `28.14ms`。修复版 Core 已
  部署到 Hook/LaunchAgent 使用的绝对路径，LaunchAgent 重启后 running，stdout/stderr
  为空；实际 hooks.json 中 AgentBell Stop 为 1、AgentBell PermissionRequest 为 0，
  cmux 的 Stop/PermissionRequest 均保留。

## OpenCode / Qoder Adapter 实机验收

- 验收日期：2026-07-26；环境：macOS 26.4 arm64、Go 1.26.5、OpenCode 已安装
  （`~/.opencode/bin/opencode`）、Qoder 未安装（以 `~/.qoder` 模拟配置目录验收）。
- OpenCode：detect 正确识别 CLI 与配置目录；install 写入
  `~/.config/opencode/plugins/agentbell.js`；verify 通过；已有用户插件（cmux）不受
  影响；重复 install 幂等；emit `session.idle` 经插件 spawn 入队，diagnose
  `runtimeVerified: true`；uninstall 精确删除 agentbell.js，cmux 完好。
- Qoder：detect 通过 `$QODER_CONFIG_DIR` 识别；install 结构化合并
  `settings.json`，用户已有 `model`/`permissions` 完好保留；verify 通过；重复
  install 幂等；emit `Stop` 经 exec-form 命令入队，diagnose `runtimeVerified: true`；
  uninstall 后 `model`/`permissions` 零丢失。
- 验收中发现并修复 event.go bug：`rawEvent` 缺少 `type` 字段，OpenCode 的
  `{"type":"session.idle"}` 被错误映射为 `agent.info`。修复后 `session.idle`
  正确映射为 `task.completed`。
- Fail-open：OpenCode 插件 spawn error handler 保证 Core 缺失不阻塞；Qoder Hook
  5s timeout 保证超时不阻塞。
- 修复后 `npm run go:check` 全绿：event 95.2%、queue 83.3%、adapter 81.1%、
  setup 83.9%。

## 发布边界

- 本切片仅为本地 dev 构建验证；版本号未 bump，未走 Release 流水线。
- setup/test 在 Windows、Linux 尚未实机验证。
- macOS/Windows/Linux 登录自启动代码已实现；Windows/Linux 实机登录重启仍待验。
- npm `setup --plan` 保持只读计划行为；真实执行由 Core 提供。

## M1 收尾决策

- 决策日期：2026-07-26。
- Windows/Linux 产品实机矩阵与服务登录重启验收经决策跳过，M1 阶段通过。
- 五个产品 Adapter 均保持 Pilot 等级；Windows/Linux 实机验证延后至 M2 或按需补验。

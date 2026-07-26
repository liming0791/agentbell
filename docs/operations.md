# AgentBell 安装与运维

最新已发布版本是 `v0.2.0-rc.3` Technical Preview，包含原生 Core、持久队列、Codex
Adapter 和 npm bootstrap。当前 M1 工作树/本地 dev 构建另已实现 `agentbell setup`、
`agentbell test`、Codex / Claude Code / Kimi Code / OpenCode / Qoder Adapter，以及
macOS、Windows、Linux 登录自启动；这些能力尚未发布，安装 rc.3 不会自动获得它们。
GUI 安装器和正式代码签名仍属于后续 M1 切片。

## 安装 Core

发布包可从私有 GitHub Release 下载。使用 npm bootstrap 时，私有仓库需要只读 GitHub
token；token 只进入 HTTP `Authorization` header，不写入 URL、安装元数据或仓库。

在 npm Trusted Publisher 启用前，先从 GitHub Release 取得已验证的 tgz：

```powershell
gh release download v0.2.0-rc.3 --repo liming0791/agentbell --pattern "agentbell-cli-*.tgz"
$env:AGENTBELL_GITHUB_TOKEN = gh auth token
npm exec --package .\agentbell-cli-0.2.0-rc.3.tgz -- agentbell install-core --version 0.2.0-rc.3
Remove-Item Env:AGENTBELL_GITHUB_TOKEN
```

npm registry 发布启用后，也可以直接运行
`npx @agentbell/cli@0.2.0-rc.3 install-core --version 0.2.0-rc.3`。

在 macOS/Linux 中把第一行改成
`export AGENTBELL_GITHUB_TOKEN="$(gh auth token)"`，完成后执行
`unset AGENTBELL_GITHUB_TOKEN`。bootstrap 先下载 `checksums.txt`，校验 SHA-256 后才把
Core 移入版本目录；校验失败的文件不会执行。
私有仓库使用 GitHub Releases API 定位 asset，并用同一个只读 token 下载；公开仓库和
`AGENTBELL_RELEASE_BASE_URL` 指向的测试镜像仍使用标准 Release 下载路径。

M0.5 产物未签名。`install.json` 和 `release-manifest.json` 的
`signatureStatus` 必须为 `technical-preview`。

## 平台路径

| 平台 | 配置 | 状态与队列 | Service 日志 |
| --- | --- | --- | --- |
| Windows | `%APPDATA%\AgentBell\config.json` | `%LOCALAPPDATA%\AgentBell\state` | 计划任务状态与 `doctor`；日志目录预留在 `%LOCALAPPDATA%\AgentBell\logs` |
| macOS | `~/Library/Application Support/AgentBell/config.json` | `~/Library/Application Support/AgentBell/state` | LaunchAgent 输出为 `~/Library/Logs/AgentBell/service.stdout.log` / `service.stderr.log` |
| Linux | `${XDG_CONFIG_HOME:-~/.config}/agentbell/config.json` | `${XDG_STATE_HOME:-~/.local/state}/agentbell` | systemd/XDG Service 输出为状态目录 `logs/service.stdout.log` / `service.stderr.log` |

测试和受控部署可以使用：

| 环境变量 | 用途 |
| --- | --- |
| `AGENTBELL_CONFIG` | 覆盖配置文件 |
| `AGENTBELL_STATE_DIR` | 覆盖状态目录 |
| `AGENTBELL_LOG_DIR` | 覆盖预留日志目录 |
| `AGENTBELL_DATA_DIR` | 覆盖 npm bootstrap 的 Core 安装根目录 |
| `AGENTBELL_RELEASE_BASE_URL` | 覆盖 bootstrap 的 Release 地址 |
| `AGENTBELL_GITHUB_TOKEN` / `GH_TOKEN` | 读取私有 GitHub Release；优先使用前者 |
| `CODEX_HOME` | 覆盖 Codex 配置目录，主要用于测试 |
| `CLAUDE_CONFIG_DIR` | 覆盖 Claude Code 用户配置目录，主要用于测试 |
| `KIMI_CODE_HOME` | 覆盖 Kimi Code 配置目录，主要用于测试 |
| `OPENCODE_CONFIG_DIR` | 覆盖 OpenCode 配置目录（默认 `~/.config/opencode`），主要用于测试 |
| `QODER_CONFIG_DIR` | 覆盖 Qoder 配置目录（默认 `~/.qoder`），主要用于测试 |
| `AGENTBELL_DEBUG=1` | 输出最小调试结果；仍不打印原始 Hook 输入 |

## 配置飞书通道

AgentBell 不保存或复制 `lark-cli` token。先按飞书官方 CLI 完成认证，再按上方
“平台路径”表创建 `config.json`（也可用下一节的 `agentbell setup` 生成）：

```json
{
  "defaultChannel": "team",
  "larkCliPath": "/absolute/path/to/lark-cli",
  "notifications": {
    "events": [
      "task.completed",
      "task.failed",
      "agent.waiting",
      "approval.required"
    ],
    "includeSummary": false,
    "privacyLevel": "metadata-only"
  },
  "channels": [
    {
      "id": "team",
      "name": "AgentBell Team",
      "type": "feishu",
      "chatId": "oc_replace_me",
      "as": "bot"
    }
  ]
}
```

配置解析是严格模式：未知字段、重复 channel、无效事件或无效身份会被拒绝。
`larkCliPath` 可省略，但 `setup` 和 `service install` 会探测并写入绝对路径，
避免从 GUI 或登录服务启动时丢失 NVM/Node 的 shell PATH。
当前 Go Adapter 固定生成 `metadata-only` 事件；`includeSummary` 与更高
`privacyLevel` 仅保留协议兼容性，不会开启 prompt、回复、代码或完整路径采集。

## 本机绑定与测试（M1 切片）

安装 Core 后，`agentbell setup` 提供交互式绑定流程：

1. 检测操作系统和已安装的 Agent CLI；
2. 引导安装 `lark-cli` 并完成 `lark-cli config init` 与
   `lark-cli auth login --domain im`；
3. 搜索或创建目标飞书会话；
4. 把配置原子写入平台目录的 `config.json`（见“平台路径”表）；
5. 可选安装 Codex / Claude Code / Kimi Code Adapter；
6. 可选安装平台原生的登录自启动后台 Service。

支持 `--dry-run`（只打印计划，不写入）和 `--json`（结构化输出）。Core 未安装时，
npm bootstrap 仍提供无副作用的 `agentbell setup --plan` 预览。

```text
agentbell setup
agentbell setup --dry-run
agentbell setup --json
```

`agentbell test` 通过 `lark-cli` 直接向通道发送一条测试消息，不经过本地队列，
用于验证绑定结果：

```text
agentbell test
agentbell test --channel team --json
```

`--channel <id>` 指定目标通道，缺省使用 `defaultChannel`。

## 安装 Codex Adapter

先查看计划，再安装并验证：

```text
agentbell adapter detect codex --json
agentbell adapter plan codex --json
agentbell adapter install codex --dry-run
agentbell adapter install codex
agentbell adapter verify codex --json
```

Adapter 结构化合并 `CODEX_HOME/hooks.json` 的 `Stop`，命令使用 Core 绝对路径。
安装前会备份原文件并写 owner receipt；重复安装不会新增重复 Hook，并会精确移除旧版
AgentBell 安装的 `PermissionRequest` 条目，不影响其他产品的审批 Hook。

Codex 的 `PermissionRequest` 发生在原生自动审核或人工审核之前，当前公开载荷没有
有效的 `approvals_reviewer` 字段；`permission_mode=default` 无法区分两者。为避免把
`auto_review` 误报成“需要你审批”，Codex 审批通知默认抑制。若未来载荷明确提供
`approvals_reviewer=user`，Core 已能只放行人工审核事件；厂商阻塞项见
[TODO](../TODO.md)。

非托管 Codex Hook 首次出现、命令变化或被其他工具重排后，需要在 Codex 的 `/hooks`
中重新审核并信任。Codex Desktop 0.146 实测旧任务可能保留启动时的 Hook/信任快照；
审核后要**新建 Codex 任务**，旧任务不能用来验收。`adapter diagnose codex` 只在配置
变更后真正收到 `task.completed` 时返回 `runtimeVerified: true`；审批或其他事件不会
再把完成链路误判为健康。

已有对话的处理方式按 Surface 区分：

- Codex CLI：若当前 `/hooks` 已列出 AgentBell Stop，可在其中信任后再完成一轮；这只
  解决“已发现但未信任”，不能证明运行时会发现会话启动后才写入的新 Hook。公开命令面
  没有 `reload-hooks`；未列出时使用 `/fork`，或退出后用 `codex resume --last` 继续
  原对话。两者都会建立新的运行时边界。
- Codex Desktop/IDE：公开文档没有提供 Hook 重载命令。使用界面的分叉任务能力
  （当前版本可用时）保留对话副本，或新建任务；完全重启 App 后重开原任务尚未完成
  实机验收，不作为 `runtimeVerified` 的标准步骤。

AgentBell 不写 Codex 私有的 `[hooks.state]` trust 数据。稳定、受支持的会话内
重载/信任接口由 [TODO](../TODO.md) 跟踪。

## 安装 Claude Code Adapter

Adapter id 为 `claude-code`：

```text
agentbell adapter detect claude-code --json
agentbell adapter plan claude-code --json
agentbell adapter install claude-code --dry-run
agentbell adapter install claude-code
agentbell adapter verify claude-code --json
```

Adapter 结构化合并 `$CLAUDE_CONFIG_DIR/settings.json`（默认
`~/.claude/settings.json`）的 `Stop`、`StopFailure`、`Notification` 和
`PermissionRequest`。命令使用官方 exec-form：绝对 Core 路径与独立参数数组，不经过
shell 拆词；因此同一配置可安全用于 Windows、macOS、Linux。Claude Code CLI 与
Desktop Code tab 的本地会话共享用户级 settings 和 Hooks；云会话不读取本机 Hook。
settings 通常会热重载，若 `/hooks` 没有出现新条目再重启会话。

Codex 与 Claude Code 的共享用户 Hook 输入目前都没有可靠、公开的 CLI/Desktop
判别字段，因此两种本地执行面都会以兼容值 `surface: cli` 入队；这不影响通知触发，
但不能把该字段当作 Desktop 使用统计。待厂商提供稳定判别字段后再细分。

## 安装 Kimi Code Adapter

命令面相同，Adapter id 为 `kimi-code`：

```text
agentbell adapter detect kimi-code --json
agentbell adapter plan kimi-code --json
agentbell adapter install kimi-code --dry-run
agentbell adapter install kimi-code
agentbell adapter verify kimi-code --json
```

Adapter 向 `$KIMI_CODE_HOME/config.toml`（默认 `~/.kimi-code/config.toml`）追加标记
分隔的 `[[hooks]]` 区域（`Stop`、`StopFailure`、`PermissionRequest`），保留用户已有
内容和格式；顶层内联 `hooks = ...` 写法会被识别为冲突并拒绝修改。Hook 在会话启动时
加载，安装后必须关闭旧会话并启动新的 Kimi 会话才生效。Kimi Code 0.29.x 的 Stop /
StopFailure 可能只有 session 标识；Core 会按每次发生时间生成幂等键，避免同一会话后续
回合被永久当成重复事件。数字形式的 `turn_id` 也会被正常接收。

## 安装 OpenCode Adapter

命令面相同，Adapter id 为 `opencode`：

```text
agentbell adapter detect opencode --json
agentbell adapter plan opencode --json
agentbell adapter install opencode --dry-run
agentbell adapter install opencode
agentbell adapter verify opencode --json
```

Adapter 向 `$OPENCODE_CONFIG_DIR/plugins/`（默认 `~/.config/opencode/plugins/`）写入
全局 JS 插件 `agentbell.js`，订阅 `session.idle`、`session.error`、`permission.asked`
三个事件。插件使用 `spawn` 调用 Core 绝对路径，`--fail-open` 保证 AgentBell 故障不
阻塞 OpenCode；已有用户插件不受影响。OpenCode CLI/TUI/Desktop 共享同一插件文件。

## 安装 Qoder Adapter

命令面相同，Adapter id 为 `qoder`：

```text
agentbell adapter detect qoder --json
agentbell adapter plan qoder --json
agentbell adapter install qoder --dry-run
agentbell adapter install qoder
agentbell adapter verify qoder --json
```

Adapter 结构化合并 `$QODER_CONFIG_DIR/settings.json`（默认 `~/.qoder/settings.json`）
的 `Stop` 和 `PostToolUseFailure`，使用 `claude-json-hooks` dialect 的 exec-form
命令格式。用户已有 `model`、`permissions` 等配置完好保留；Qoder CLI/IDE/JetBrains
共享同一配置文件。

## 运行 Service

三平台使用相同命令：

```text
agentbell service install
agentbell service status --json
```

`service install` 会把当前可用的 `lark-cli` 绝对路径迁移进配置。平台后端：

- macOS：`~/Library/LaunchAgents/com.agentbell.service.plist`；
- Windows：当前用户的 `\AgentBell\AgentBell` ONLOGON 计划任务；
- Linux：`${XDG_CONFIG_HOME:-~/.config}/systemd/user/agentbell.service`，若
  `systemctl --user` 不可用则写入
  `${XDG_CONFIG_HOME:-~/.config}/autostart/com.agentbell.service.desktop`。

systemd 与 LaunchAgent 安装后立即启动；XDG Autostart 在下一次桌面登录时启动。
Windows 的 npm 安装通常解析到 `lark-cli.cmd`；发送器会自动调用同目录
`lark-cli.ps1` 并按独立参数传递通知文本，避免 `cmd.exe` 对引号和元字符的二次解析。
若该配套 shim 缺失，事件会以永久配置错误进入 dead，而不是无休止重试。
升级 Core 或调整路径后可重复执行，不会创建多个同名服务。临时调试可前台运行：

```text
agentbell service run --foreground
```

Service 使用独占锁和心跳；第二实例会拒绝启动。队列发送失败按
`1s / 5s / 30s / 2m / 10m` 退避，五次失败后进入 `dead`。成功 history 保留 30 天；
dead 最长保留 90 天且最多 1000 条。

## 诊断与恢复

```text
agentbell doctor --json
agentbell adapter diagnose codex
agentbell adapter diagnose claude-code
agentbell adapter diagnose kimi-code
agentbell adapter diagnose opencode
agentbell adapter diagnose qoder
agentbell queue list --state pending
agentbell queue list --state inflight
agentbell queue list --state dead
agentbell queue retry <event-id>
```

`doctor` 会报告实际使用的 `larkCliPath`。Adapter 的 `verify` 只验证配置结构；
`diagnose` 还检查 Hook 是否在最后一次配置变更后真正到达过 Core。人工重试会重置自动
尝试次数，并记录 `manualRetries` 和 `lastRetriedAt`。Service 重启时会恢复过期
inflight 租约；损坏 JSON 被隔离为 `dead/*.corrupt`，不会阻塞其他事件。

## 卸载

产品级统一卸载使用：

```text
agentbell uninstall --dry-run --json
agentbell uninstall
```

它先只读预检服务定义与五个 Adapter 配置；全部可安全处理后，先停止并移除登录服务，
再精确删除 Codex、Claude Code、Kimi Code、OpenCode、Qoder 的 AgentBell Hook。经 npm bootstrap 调用
时，bootstrap 会等待 Core 正常退出，再删除它管理的当前版本目录；直接运行 Core
二进制时不做自删。配置、queue、history 和 dead 默认保留，方便恢复与诊断。

需要分步处理时也可使用：

```text
agentbell service uninstall --dry-run
agentbell service uninstall
agentbell adapter uninstall codex --dry-run
agentbell adapter uninstall codex
agentbell adapter uninstall claude-code
agentbell adapter uninstall kimi-code
agentbell adapter uninstall opencode
agentbell adapter uninstall qoder
agentbell adapter uninstall all --dry-run
agentbell adapter uninstall all
```

`uninstall all` 先对五个 Adapter 执行只读预检，确认配置结构可安全处理后才开始实际删除。
每个 Adapter 只删除与 receipt/命令/标记区域匹配的 AgentBell 条目，保留用户其他 Hook。
直接运行 Core 卸载命令后，可再删除对应版本的 Core 安装目录；经 npm bootstrap
运行时该步骤已自动完成。配置、queue、history 和 dead 默认保留，避免卸载造成诊断
记录丢失；只有用户明确不需要恢复时才手动删除这些数据。

## 隐私边界

默认队列只保存规范化元数据、项目显示名和哈希后的 session/task/turn 标识。原始 Hook
JSON、prompt、代码、完整回复、summary 和完整 cwd 不写入队列。`--fail-open` 保证本地
入队故障不阻塞 Agent；调试模式也不打印原始 Hook 输入。

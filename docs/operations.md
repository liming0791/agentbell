# AgentBell 安装与运维

最新已发布版本是 `v0.2.0-rc.3` Technical Preview，包含原生 Core、持久队列、Codex
Adapter 和 npm bootstrap。当前 M1/M1.5 工作树、本地 dev 构建另已实现
`agentbell setup`、`agentbell test`、Codex / Claude Code / Kimi Code / OpenCode /
Qoder / QoderWork / TRAE Adapter，以及 macOS、Windows、Linux 登录自启动；这些能力
尚未发布，安装 rc.3 不会自动获得它们。GUI 安装器和正式代码签名仍属于后续发布阶段。

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
| `QODERWORK_CONFIG_DIR` | 覆盖 QoderWork 配置目录（国际版默认 `~/.qoderwork`，CN 版默认 `~/.qoderworkcn`），主要用于测试 |
| `TRAE_CONFIG_DIR` | 覆盖 TRAE 配置目录（国际版默认 `~/.trae`，CN 版默认 `~/.trae-cn`），主要用于测试 |
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

## 本机绑定与测试（M1/M1.5）

安装 Core 后，`agentbell setup` 提供交互式绑定流程：

1. 检测操作系统和已安装的 Agent CLI；
2. 引导安装 `lark-cli` 并完成 `lark-cli config init` 与
   `lark-cli auth login --domain im`；
3. 搜索或创建目标飞书会话；
4. 把配置原子写入平台目录的 `config.json`（见“平台路径”表）；
5. 可选安装 Codex / Claude Code / Kimi Code / OpenCode / Qoder / QoderWork /
   TRAE Adapter；
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

`--channel <id>` 指定目标通道，缺省使用 `defaultChannel`。成功输出只包含 AgentBell
channel id、状态和发送时间，不回显真实飞书 chat id。

## M2 工作树命令（开发中，尚未发布）

当前工作树可以在隔离数据目录中预览本机设置、一次性绑定和 stable bridge：

```text
agentbell settings show --effective --json
agentbell settings channel list --json
agentbell settings channel add team --name "AgentBell Team" --chat-id <id> --as bot --dry-run
agentbell settings channel rename <id> --name <name> --dry-run
agentbell settings channel remove <id> --replacement-default <id> --dry-run
agentbell settings channel default <id> --dry-run
agentbell settings event enable task.failed --dry-run
agentbell settings template list --json
agentbell settings template preview <id> --event task.completed --source codex
agentbell settings quiet-hours set --timezone Asia/Singapore --start 22:00 --end 08:00 --days mon,tue,wed,thu,fri
agentbell policy status --json
agentbell policy explain --event task.failed --source codex
agentbell bind create --name "AgentBell Team" --as bot
agentbell bind complete --code-stdin
agentbell bind status --json
agentbell hook conflicts all --json
agentbell hook reconcile all --dry-run --json
agentbell bridge doctor --json
agentbell service restart --json
agentbell plugin verify <bundle> --json
agentbell relay configure --listen 127.0.0.1:8088 --dry-run --json
agentbell relay run --foreground
agentbell relay bind create --team team-main --source codex --runtime ssh --json
agentbell relay peers list --json
agentbell relay peers revoke <peer-id> --dry-run --json
agentbell relay receipts list --json
agentbell remote configure --team team-main --origin <origin-id> --runtime ssh \
  --outbox <absolute-path> --connector https --endpoint https://relay.example/v1/events \
  --dry-run --json
agentbell remote pair --code-stdin --endpoint https://relay.example/v1/pair --json
agentbell remote test --adapter codex --surface cli --wait 30s --json
agentbell remote emit --adapter codex --surface cli --runtime ssh --stdin --fail-open
agentbell remote drain --stdio
agentbell relay connector add --id build-primary --team team-main \
  --origin origin-build --runtime ssh --host-executable /usr/bin/ssh \
  --remote-executable /usr/local/bin/agentbell --host build.example.com \
  --user agentbell --known-hosts /absolute/path/to/known_hosts --json
agentbell relay connector list --json
agentbell relay connector pair --id build-primary --code-stdin --json
agentbell relay connector remove --id build-primary --revision <revision> --json
agentbell service restart
agentbell uninstall --dry-run --json
```

settings sidecar 缺失时 `show --effective` 从 M1 `config.json` 合成兼容默认值；第一次
写操作才创建严格的 `settings.json` v1。模板只接受 metadata-only 字段。Channel
list/add/rename/remove/default 与 `bind complete` 使用同一套原子配置事务；Channel
命令另支持 revision 检查。
绑定码只在 create 时显示明文，complete 从 stdin 读取；状态输出不包含码、码哈希或
飞书 chat id。setup 也可选择一次性绑定分支，并在生成码后安全暂停，完成绑定后再重跑。

npm bootstrap 另有 `upgrade --to <version>`、`rollback` 和 `versions` 的事务基础，
支持 checksum/manifest 校验、active/previous、journal、stable bridge、service restart、
smoke 和失败补偿；App 与三个旧 Adapter 已使用 active generation。sidecar 回滚
preflight、`plugin verify` 和五个插件的 Release keyless 签名/下载后复验已经接入自动
测试与工作流。跨旧 Release 的自动 lifecycle smoke 已接入 workflow；macOS 真实
LaunchAgent 的备份迁移与后台飞书投递已通过，真实新 RC 运行和 Windows/Linux
服务迁移仍未完成。
首次从 M1 升级时，bootstrap 会校验旧式 `install.json` 并把唯一的受管旧版本纳入
`previous`；若 `bin/` 中有多个有效旧版本，必须使用
`upgrade --from <legacy-version> --to <version>` 明确选择，不能按版本号猜测。服务切换
时，新 Core 在 active state 落盘后执行 `service install`，把旧服务定义迁到 stable
bridge；失败会移除新 active pointer，并由旧 Core 重装 legacy service。显式 rollback
保留当前协议版本的 stable bridge，继续验证其独立 checksum，并只重启服务。
Technical Preview 默认只在隔离数据目录执行 `--dry-run` 或自动 fixture；真实安装的
非 dry-run 升级必须先备份平台服务定义、受管 `bin/` 和当前 Core，并取得明确授权。

远端私钥默认进入 macOS Keychain、Linux Secret Service 或 Windows DPAPI；只有显式
同时传入 `--key-file` 和 `--acknowledge-file-fallback` 才允许使用权限为 0600 的文件。
`remote emit` 在事件发生处完成 metadata-only 裁剪并只写有容量上限的 durable outbox；
`remote pair` 的明文绑定码只从 stdin 读取。HTTPS connector 必须使用 HTTPS；只有明确的
loopback SSH tunnel 才允许 HTTP。本机 `remote.json` 只拥有本机 remote emit/HTTPS
durable outbox，不作为 Host pull 配置；Host 侧以严格的 `host-connectors.json` v1
登记多个 WSL/SSH/container target，只保存预期 team/origin、runtime 和 connector，
不含远端 outbox 或私钥。用户级 service 为每个 target 建立独立锁、退避和脱敏 runtime
proof，并另行持续 drain 本机 HTTPS outbox；单个远端失败不会停止本地飞书通知。
`relay connector pair` 通过同一 connector 的 bounded stdio 启动远端
`remote pair --stdio`，不开放 listener，且 hello 必须精确匹配 registry 中预期的
team/origin/runtime 后才消费本地绑定码并登记 relay peer。add/remove 后需
`agentbell service restart` 重新枚举 worker。端到端远程实机证据仍未完成，因此上述
自动测试不能替代跨主机实机验收。

相同 source idempotency key 的 Hook 重复进入 `remote emit` 时，会复用第一次已经耐久
写入的 outbox item；即使第一次已进入 history，也不会生成新的 nonce/envelope 或再次
投递。这个 producer 侧去重不放宽 relay 安全边界：直接提交同 delivery key、不同 exact
body 的传输请求仍会被拒绝。

`remote test` 每次生成新的 metadata-only `agent.info` 探针。未指定 `--wait` 时成功只
表示探针已经耐久写入 outbox；指定最长 10 分钟的 `--wait` 后，只有进入 history/ACK
才返回成功，timeout 或 dead 都会明确失败且保留可诊断状态。

在 Linux 开发机或 Ubuntu CI 上可执行 `npm run smoke:https`。该命令只使用隔离临时
目录、loopback listener 和临时 CA，验证正常 x509 信任链、SPKI pin、一次性 peer
配对、HTTPS ACK、断网恢复、producer 去重、metadata-only 落盘与 runtime proof；完成
后删除临时证书、私钥和状态。它不会连接飞书，也不能替代独立跨主机实机验收。

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

## 安装 QoderWork Adapter

QoderWork 使用独立的 Adapter id 和配置 profile：

```text
agentbell adapter detect qoder-work --json
agentbell adapter plan qoder-work --json
agentbell adapter install qoder-work --dry-run
agentbell adapter install qoder-work
agentbell adapter verify qoder-work --json
```

Adapter 按检测到的产品区域修改国际版 `~/.qoderwork/settings.json` 或 CN 版
`~/.qoderworkcn/settings.json`；也可用 `QODERWORK_CONFIG_DIR` 显式覆盖。它为 `Stop`、
`PostToolUseFailure` 和 `PermissionRequest` 结构化合并 shell-command Hook，不会读写
Qoder 的 `~/.qoder/settings.json`。安装、升级或卸载时保留其他用户配置和 Hook；Core
版本目录变化时会根据 receipt 迁移自己的旧命令，避免重复条目。QoderWork 不支持 Hook
热更新，配置变化后必须完全退出并重新启动；随后完成一个新任务，并用
`agentbell adapter diagnose qoder-work` 检查新的 runtime proof。

## 安装 TRAE IDE Adapter

TRAE Adapter id 为 `trae`：

```text
agentbell adapter detect trae --json
agentbell adapter plan trae --json
agentbell adapter install trae --dry-run
agentbell adapter install trae
agentbell adapter verify trae --json
```

Adapter 结构化合并 macOS/Windows 用户目录下的 Hook profile（国际版
`~/.trae/hooks.json`，CN 版 `~/.trae-cn/hooks.json`），保留格式版本
`1` 和用户已有 Hook。AgentBell 只安装一个 matcher 为
`idle_prompt|permission_prompt` 的异步 `Notification` Hook：
`idle_prompt` 映射为 `task.completed`，`permission_prompt` 映射为
`approval.required`；不同时安装 `Stop`，因此不会为同一轮同时注册两条完成链路。
Windows 命令使用 PowerShell 调用运算符与转义，macOS 命令使用 shell 引号，二者都固定
Core 绝对路径。

安装后进入 **设置 > Hooks > 全局**：

1. 打开“已配置的 Hooks”；
2. 把“Hooks 命令运行方式”设为“本地自动运行”；
3. 完成一个新任务。

不要保留“沙箱运行”：AgentBell 需要写用户级配置目录中的队列和 runtime proof。
TRAE 沙箱可能让 Hook 进程以 `0` 退出，却把这些写入隔离掉；这是 fail-open，不代表
通知链路已生效。`verify` 只检查配置；`agentbell adapter diagnose trae` 必须看到配置
变更后的 `task.completed` proof，才证明完成通知真实触发。

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
macOS LaunchAgent 会显式设置安装用户的 `HOME` 和固定 `PATH`；`HOME` 不可省略，
否则 `lark-cli` 可能找到配置却无法从登录 Keychain 读取凭据，表现为前台测试成功、
后台发送失败。
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
agentbell adapter diagnose qoder-work
agentbell adapter diagnose trae
agentbell queue list --state pending
agentbell queue list --state inflight
agentbell queue list --state dead
agentbell queue retry <event-id>
```

顶层 `doctor --json` 使用稳定的 `schemaVersion: 1`，汇总 config/settings、活动安装、
签名能力、secret store、队列、七个 Adapter 和 relay/connector 状态。它只报告路径或
凭据是否配置、后端类型和稳定错误码，不输出实际路径、checksum、secret、peer id 或
原始 Hook 内容。Adapter 的 `verify` 只验证配置结构；`diagnose` 还检查 Hook 是否在
最后一次配置变更后真正到达过 Core。人工重试会重置自动尝试次数，并记录
`manualRetries` 和 `lastRetriedAt`。Service 重启时会恢复过期 inflight 租约；损坏 JSON
被隔离为 `dead/*.corrupt`，不会阻塞其他事件。

## 卸载

产品级统一卸载使用：

```text
agentbell uninstall --dry-run --json
agentbell uninstall
agentbell uninstall --dry-run --json \
  --delete-remote-credential --confirm-delete-remote-credential
agentbell uninstall \
  --delete-remote-credential --confirm-delete-remote-credential
```

它先只读预检服务定义、七个 Adapter、`remote.json`、`host-connectors.json`、
`relay.json` 和活动版本指针；其中远程资产摘要只返回 connector/peer 计数、私钥后端、
活动版本与 generation，不返回 team/origin/peer id、私钥路径或 checksum。全部可安全
处理后，先停止并移除登录服务，再精确删除七个 AgentBell Hook。经 npm bootstrap 调用时，
bootstrap 会等待 Core 正常退出，再删除它管理的当前版本、active pointer 和 stable
bridge；直接运行 Core 二进制时不做自删。

配置 sidecar、host connector、relay peer、queue、history、dead、runtime proof 和远程
私钥默认保留。删除远程私钥必须同时提供
`--delete-remote-credential` 与 `--confirm-delete-remote-credential`；只给任一个参数、
或 `remote.json` 无法安全解析时，卸载会在修改服务/Hook 前拒绝。`--dry-run` 搭配两个
参数只报告 `would-delete`，不会访问或删除 secret store。

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
agentbell adapter uninstall qoder-work
agentbell adapter uninstall trae
agentbell adapter uninstall all --dry-run
agentbell adapter uninstall all
```

`uninstall all` 先对七个 Adapter 执行只读预检，确认配置结构可安全处理后才开始实际删除。
每个 Adapter 只删除与 receipt/命令/标记区域匹配的 AgentBell 条目，保留用户其他 Hook。
直接运行 Core 卸载命令后，可再删除对应版本的 Core 安装目录；经 npm bootstrap
运行时该步骤已自动完成。配置、queue、history 和 dead 默认保留，避免卸载造成诊断
记录丢失；只有用户明确不需要恢复时才手动删除这些数据。

QoderWork 不支持 Hook 热更新，安装或卸载后需要完全退出并重新启动。TRAE 需要打开
**设置 > Hooks > 全局 > 已配置的 Hooks**，并把“Hooks 命令运行方式”设为“本地自动
运行”；只有 `diagnose trae` 出现配置变更后的 `task.completed` runtime proof，才证明
完成事件已真正进入 Core。

## 隐私边界

默认队列只保存规范化元数据、项目显示名和哈希后的 session/task/turn 标识。原始 Hook
JSON、prompt、代码、完整回复、summary 和完整 cwd 不写入队列。`--fail-open` 保证本地
入队故障不阻塞 Agent；调试模式也不打印原始 Hook 输入。

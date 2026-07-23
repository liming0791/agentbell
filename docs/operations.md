# AgentBell M0.5 安装与运维

M0.5 是 `v0.2.0-rc.1` Technical Preview。它提供原生 Core、前台 Service、Codex
参考 Adapter 和 npm bootstrap；系统登录自启动、GUI 安装器、正式代码签名和真实飞书
绑定向导属于 M1。

## 安装 Core

发布包可从私有 GitHub Release 下载。使用 npm bootstrap 时，私有仓库需要只读 GitHub
token；token 只进入 HTTP `Authorization` header，不写入 URL、安装元数据或仓库。

在 npm Trusted Publisher 启用前，先从 GitHub Release 取得已验证的 tgz：

```powershell
gh release download v0.2.0-rc.1 --repo liming0791/agentbell --pattern "agentbell-cli-*.tgz"
$env:AGENTBELL_GITHUB_TOKEN = gh auth token
npm exec --package .\agentbell-cli-0.2.0-rc.1.tgz -- agentbell install-core --version 0.2.0-rc.1
Remove-Item Env:AGENTBELL_GITHUB_TOKEN
```

npm registry 发布启用后，也可以直接运行
`npx @agentbell/cli@0.2.0-rc.1 install-core --version 0.2.0-rc.1`。

在 macOS/Linux 中把第一行改成
`export AGENTBELL_GITHUB_TOKEN="$(gh auth token)"`，完成后执行
`unset AGENTBELL_GITHUB_TOKEN`。bootstrap 先下载 `checksums.txt`，校验 SHA-256 后才把
Core 移入版本目录；校验失败的文件不会执行。

M0.5 产物未签名。`install.json` 和 `release-manifest.json` 的
`signatureStatus` 必须为 `technical-preview`。

## 平台路径

| 平台 | 配置 | 状态与队列 | 前台日志 |
| --- | --- | --- | --- |
| Windows | `%APPDATA%\AgentBell\config.json` | `%LOCALAPPDATA%\AgentBell\state` | Service 的 stderr；日志目录预留在 `%LOCALAPPDATA%\AgentBell\logs` |
| macOS | `~/Library/Application Support/AgentBell/config.json` | `~/Library/Application Support/AgentBell/state` | Service 的 stderr；日志目录预留在 `~/Library/Logs/AgentBell` |
| Linux | `${XDG_CONFIG_HOME:-~/.config}/agentbell/config.json` | `${XDG_STATE_HOME:-~/.local/state}/agentbell` | Service 的 stderr；日志目录为状态目录下 `logs/` |

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

## 配置飞书通道

AgentBell 不保存或复制 `lark-cli` token。先按飞书官方 CLI 完成认证，再创建
`config.json`：

```json
{
  "defaultChannel": "team",
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

## 安装 Codex Adapter

先查看计划，再安装并验证：

```text
agentbell adapter detect codex --json
agentbell adapter plan codex --json
agentbell adapter install codex --dry-run
agentbell adapter install codex
agentbell adapter verify codex --json
```

Adapter 结构化合并 `CODEX_HOME/hooks.json` 的 `Stop` 和
`PermissionRequest`，命令使用 Core 绝对路径。安装前会备份原文件并写 owner receipt；
重复安装不会新增重复 Hook。

## 运行 Service

M0.5 仅提供前台服务：

```text
agentbell service run --foreground
```

Service 使用独占锁和心跳；第二实例会拒绝启动。队列发送失败按
`1s / 5s / 30s / 2m / 10m` 退避，五次失败后进入 `dead`。成功 history 保留 30 天；
dead 最长保留 90 天且最多 1000 条。

## 诊断与恢复

```text
agentbell doctor --json
agentbell queue list --state pending
agentbell queue list --state inflight
agentbell queue list --state dead
agentbell queue retry <event-id>
```

人工重试会重置自动尝试次数，并记录 `manualRetries` 和 `lastRetriedAt`。Service 重启时会
恢复过期 inflight 租约；损坏 JSON 被隔离为 `dead/*.corrupt`，不会阻塞其他事件。

## 卸载

先精确移除 Codex Hook：

```text
agentbell adapter uninstall codex --dry-run
agentbell adapter uninstall codex
```

确认 Service 已停止后，可以删除对应版本的 Core 安装目录。配置、queue、history 和 dead
默认保留，避免卸载造成诊断记录丢失；只有用户明确不需要恢复时才手动删除这些数据。

## 隐私边界

默认队列只保存规范化元数据、项目显示名和哈希后的 session/task/turn 标识。原始 Hook
JSON、prompt、代码、完整回复、summary 和完整 cwd 不写入队列。`--fail-open` 保证本地
入队故障不阻塞 Agent；调试模式也不打印原始 Hook 输入。

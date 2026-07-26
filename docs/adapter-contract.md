# AgentBell Source Adapter 协议

## 目标

Adapter 负责把一个 Agent 产品的公开扩展入口转成 AgentBell 的统一事件。Adapter 不负责飞书认证和消息发送。

## 生命周期

每个 Adapter 实现以下操作：

1. `detect`：检测产品、版本、Surface 和运行位置。
2. `capabilities`：动态判断当前版本实际支持的事件。
3. `plan`：输出将执行的安装、修改和权限请求。
4. `install`：安装插件或结构化合并 Hook。
5. `verify`：静态检查安装结构、命令和所有权信息。
6. `uninstall`：只删除 AgentBell 自己写入的内容。
7. `diagnose`：在 `verify` 之上检查 runtime proof，返回 Hook 是否在最后一次配置变更后
   真正到达 Core；必要时提示 Codex `/hooks` 信任或 Kimi 新会话加载。

## Manifest

适配器目录以机器可读清单驱动，示例：

```json
{
  "id": "qoder",
  "displayName": "Qoder",
  "phase1": true,
  "supportLevel": "pilot",
  "surfaces": ["cli", "ide", "jetbrains"],
  "platforms": ["windows", "macos", "linux"],
  "dialect": "claude-json-hooks",
  "events": ["Stop", "PostToolUseFailure"]
}
```

实际首期目录见 `adapters/catalog.json`。

## Hook Dialect

不要为每个产品复制一套几乎相同的模板。首期按协议族生成：

| Dialect | 产品 |
| --- | --- |
| `codex-json-hooks` | Codex CLI/Desktop |
| `claude-json-hooks` | Claude Code、Qoder，以及验证兼容后的产品 |
| `kimi-plugin-hooks` | Kimi Code |
| `opencode-plugin-events` | OpenCode |
| `vendor-plugin-pilot` | ZCode、WorkBuddy、TRAE 的验证期适配 |
| `assisted-mcp-skill` | 为将来明确批准的软触发集成预留；Kimi Work 当前不使用 |

Dialect 只描述事件协议；路径、变量名、插件根目录和配置优先级仍由产品 profile 定义。

没有公开确定性生命周期 Hook 的产品不得仅因为存在 Skill、MCP 或提示词入口就生成 Adapter。此类产品默认进入 Waiting，直到单独完成产品决策和风险评估。

## 统一事件映射

| 原始语义 | 统一事件 |
| --- | --- |
| Stop / session.idle / response ready | `task.completed` |
| StopFailure / session.error | `task.failed` |
| PermissionRequest / permission.asked | `approval.required` |
| Notification idle_prompt / agent_needs_input | `agent.waiting` |
| Interrupt | `session.interrupted` |
| SubagentStop | `subagent.completed` |

不能确认语义时映射到 `agent.info`，不得猜成任务完成。原始 Hook 输入只在规范化进程内
使用，默认不持久化。

映射表描述协议语义，不代表每个产品都能无条件发出该通知。若产品事件发生在路由决策
之前，且载荷不能证明最终需要用户操作，Adapter 必须抑制而不是误报。Codex 当前的
`PermissionRequest` 即属此类：只有未来明确出现 `approvals_reviewer=user` 才能发送
`approval.required`。

## 安装规则

- 默认用户级安装；项目级安装需要显式参数。
- 能通过官方插件安装时，不直接修改用户设置。
- JSON 使用结构化合并；无法可靠 round-trip 的 TOML 使用带内容哈希的标记区域，
  保留区域外原始字节，并拒绝有歧义的顶层内联配置。
- 每个 Hook 必须有可精确识别的所有权信息：稳定 ID、命令内容或标记区域加 owner receipt。
- 重复运行 `install` 不产生重复条目。
- 发现同名但内容不同的 Hook 时停止并报告冲突。
- 安装前备份；卸载根据稳定 ID 和内容哈希精确删除。
- GUI Surface 使用 AgentBell 二进制绝对路径。

## 原始事件入口

统一调用形式：

```text
agentbell emit \
  --adapter qoder \
  --surface ide \
  --runtime host \
  --stdin
```

Adapter 可以通过 stdin 传原始 JSON/TOML 事件。AgentBell 对原始内容设置大小上限，默认不持久化完整 transcript。

事件名称字段按以下优先级解析（取第一个非空值）：

1. `hook_event_name` — Claude Code、Codex、Qoder 等 JSON hooks 产品使用；
2. `event` — 部分产品的通用事件字段；
3. `type` — OpenCode 等插件事件系统使用（如 `{"type":"session.idle"}`）。

三个字段均为可选字符串，Core 按上述顺序 fallback。Adapter 实现无需关心具体字段名，只需透传原始 JSON。

## 验收门槛

一个 Adapter 从 Pilot 升为 Verified 前必须通过：

- 产品最低支持版本和最新版各一次；
- 产品实际支持的每个操作系统；
- 完成、失败、等待输入/授权至少三类事件；
- AgentBell 未运行、飞书离线和 `lark-cli` 失败时不阻塞 Agent；
- 重复事件去重；
- 已有用户 Hook 不丢失；
- 重复安装、升级、卸载可逆；
- GUI 启动环境没有 shell PATH 时仍能执行；
- 通知正文默认不泄露提示词、路径和代码。

当前 Codex、Claude Code 与 Kimi Code 实现只达到 Technical Preview/Pilot。fixture、
静态 `verify` 和 CI 证明实现可移植，但不替代宿主信任/加载后的 runtime proof，也不
替代产品最低/最新版在每个支持操作系统上的真实事件验收。

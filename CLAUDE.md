# AgentBell 项目规则

面向 Coding Agent CLI/IDE/Desktop 的跨平台通知适配层：归一化生命周期事件，经官方
`lark-cli` 发到飞书。Go 单文件 Core 是正式数据面；Node.js 只做 npm bootstrap 和 M0
迁移期原型。

## 常用命令

```bash
npm ci && npm run ci     # lint + 结构检查 + Node/Go 覆盖率门禁 + pack 预检
npm run go:check         # gofmt/vet/test + 覆盖率门禁
npm run perf:emit        # emit 性能门禁
npm run perf:m2          # bridge 性能 + relay 持久重试语义门禁
npm run doctor           # bootstrap 环境检查（只读）
```

Core 本地构建：`cd core && go build ./cmd/agentbell`。

## 结构

- `core/` Go Core（命令分发在 `core/internal/app/app.go`）
- `packages/cli` npm bootstrap（`setup --plan` 本地短路，其余命令代理 Core）
- `packages/hook-runtime` M0 原型，非正式数据面
- `adapters/catalog.json` Adapter 目录（权威源）
- `plugins/` 各 Agent 插件与 setup Skill
- `docs/` 架构、适配器协议、验收记录、运维

## 硬约定

- `adapters/catalog.json` 与 `core/internal/adapter/catalog.json` 必须内容一致
  （`check-structure.mjs` 强制）；改 catalog 两边同步改。
- 配置文件权威位置是平台目录（macOS `~/Library/Application Support/AgentBell/`），
  不是 `~/.agentbell`；env 覆盖变量见 `docs/operations.md`。
- 隐私默认 `metadata-only`：原始 Hook JSON、prompt、代码、summary、完整 cwd 不落盘；
  session/task/turn 标识只存 sha256 哈希。诊断、测试和绑定状态输出不得回显 chat id、
  token、绑定码、私钥或原始 Hook 内容。
- 所有 Hook 必须 fail-open，AgentBell 故障不得阻塞原 Agent。
- Adapter `verify` 只证明配置结构正确；运行态必须由 `diagnose` 的 runtime proof 确认。
  Codex 只接受 `task.completed` 的事件级 proof；新/变更/重排 Hook 需经 `/hooks` 信任
  并新建任务。Codex `PermissionRequest` 缺少明确 `approvals_reviewer=user` 时必须抑制，
  不能把 `auto_review` 误报成人工待审批。Kimi Code 需新会话加载。
- 后台服务必须经 `agentbell service install` 注册：macOS 使用 LaunchAgent、Windows
  使用当前用户登录计划任务、Linux 优先 systemd user 并回退 XDG Autostart。发送端使用
  配置中的 `larkCliPath` 绝对路径，不得依赖 GUI/登录服务继承 shell PATH；macOS plist
  还必须显式设置安装用户的 `HOME` 和固定 `PATH`，否则后台进程可能无法读取 Keychain。
  首次 M1→M2 upgrade 写入 active state 后必须由新 Core 执行 `service install`，把旧
  版本化服务定义迁到 stable bridge；若失败且旧安装没有 active state，补偿必须由旧
  Core 再执行 `service install` 恢复 legacy 定义。普通 rollback 只 restart stable bridge。
- 产品卸载使用 `agentbell uninstall` 统一预检并移除服务与七个 Adapter Hook；Core
  进程退出后由 npm bootstrap 删除其管理的精确版本目录，默认保留配置、队列、远程
  sidecar/peer 和诊断数据；远程私钥只有同时传入删除与确认参数才可删除。
- Adapter 全部保持 `pilot`，未完成 `docs/adapter-contract.md` 实机验收矩阵不得标
  `verified`。
- 未签名产物的 `signatureStatus` 必须为 `technical-preview`。
- 无公开生命周期 Hook 的产品（如 Kimi Work）不做轮询/UI 注入/Skill 软触发替代；
  解锁条件见 `TODO.md`。
- Go 覆盖率门禁：总行 ≥75%；`event`/`queue`/`adapter`/`setup` 以及已进入 M2
  数据面的 `binding`/`settings`/`relay`/`bridge`/`installstate`/`policy`/
  `hookaudit`/`pluginverify`/`remoteconfig`/`remote`/`secretstore` 各 ≥80%
  （`scripts/check-go.mjs`）。

## 深入文档

| 主题 | 文件 |
| --- | --- |
| 架构与技术选型 | `docs/architecture.md` |
| 开发路线（M0–M3） | `docs/development.md` |
| Adapter 协议与验收门槛 | `docs/adapter-contract.md` |
| 安装、路径、运维 | `docs/operations.md` |
| 兼容矩阵 | `docs/compatibility.md` |
| M0.5 / M1 / M1.5 验收 | `docs/m0.5-validation.md`、`docs/m1-setup-validation.md`、`docs/m1.5-validation.md` |
| M2 实施计划 | `docs/m2-execution-plan.md` |
| M2 验收台账 | `docs/m2-validation.md` |
| 厂商阻塞事项 | `TODO.md` |

# AgentBell TODO

此文件记录已经决定要跟踪、但当前不能安全实现的产品事项。常规开发路线见 `docs/development.md`。

## 等待厂商能力

### Codex 人工审批可判定 Hook

- [ ] 等待 Codex `PermissionRequest` 输入公开有效的当前回合审批人字段。
- 状态：Blocked by vendor capability；完成通知不受影响。
- 当前行为：不发送 Codex `approval.required`，避免将 `auto_review` 误报成人工待审批。
- 记录日期：2026-07-25。

解锁条件：

1. Hook 载荷提供当前请求的有效 `approvals_reviewer`，至少能区分 `user` 与
   `auto_review`；不能只读取全局默认值；
2. Codex CLI 与 Desktop 本地会话的字段语义一致并有官方文档；
3. 实机证明 `approvals_reviewer=user` 只在真正等待用户时出现。

参考：[OpenAI Codex #23465](https://github.com/openai/codex/issues/23465)。

### Codex 已有任务的 Hook 热加载

- [ ] 等待 Codex 提供受支持的 Hook 重载或用户确认式 trust request 接口。
- 状态：Blocked by vendor capability；首次安装后的既有任务无法承诺原地生效。
- 当前行为：CLI 仅在 `/hooks` 已列出新 Hook 时允许用户审核；没有公开的
  `reload-hooks` 命令。公开文档没有为 Desktop/IDE 提供 Hook 重载接口，AgentBell
  以分叉或新建任务作为可靠激活路径。
- 记录日期：2026-07-26。

解锁条件：

1. 已启动的 CLI/Desktop 本地任务能显式重新扫描 Hook 来源；
2. 用户能通过 Codex 自有 UI/命令确认信任，安装器不写私有 trust state；
3. 重载后能用同一任务的真实 `Stop` 事件产生新的 runtime proof。

参考：[Codex Hooks](https://learn.chatgpt.com/docs/hooks)、
[OpenAI Codex #21615](https://github.com/openai/codex/issues/21615)。

### Kimi Work 确定性通知适配器

- [ ] 等待并验证 Kimi Work 官方公开的生命周期 Hook。
- 状态：Waiting / Blocked by vendor capability。
- 范围：不进入 Phase 1 运行时适配器；不提供 Skill/MCP Assisted Adapter。
- 记录日期：2026-07-23。

解锁条件：

1. 官方提供能区分完成、失败、等待输入或授权的生命周期事件；
2. 官方文档公开第三方 Hook/插件的安装位置、事件输入结构和兼容版本；
3. Hook 可以 fail-open，不因 AgentBell 或网络故障阻塞原任务；
4. 安装、验证、升级和卸载可以稳定自动化；
5. 通过 `docs/adapter-contract.md` 定义的 Pilot 验收和安全检查。

不接受的替代方案：

- 轮询 transcript 或日志猜测任务状态；
- 注入或自动点击 Kimi Work UI；
- 依赖模型遵循自然语言提示；
- 用 Skill/MCP 主动调用伪装成“每次任务完成必达”。

参考：[Kimi Work Plugin Center](https://www.kimi.com/help/kimi-work/plugin-center)。

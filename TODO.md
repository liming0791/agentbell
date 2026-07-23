# AgentBell TODO

此文件记录已经决定要跟踪、但当前不能安全实现的产品事项。常规开发路线见 `docs/development.md`。

## 等待厂商能力

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

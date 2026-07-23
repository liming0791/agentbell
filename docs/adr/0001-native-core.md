# ADR 0001：M0.5 原生 Core

- 状态：Accepted
- 日期：2026-07-23

## 决策

AgentBell 正式 Core 使用 Go 1.26.5，代码位于 `core/`，初始模块路径为
`github.com/liming0791/agentbell/core`。发布构建默认使用 `CGO_ENABLED=0`。

Node.js workspace 保留为 npm bootstrap 和迁移期协议原型，不承担正式 Hook 的网络发送。

## 原因

- 单文件程序适合高频、短生命周期 Hook；
- GUI 启动环境不应依赖用户 shell 的 Node.js 或 PATH；
- 无 CGO 可以稳定交叉编译六个首期目标；
- Core 与 npm bootstrap 分离后，可以独立验证、升级和回滚。

## 后果

- M1 前必须冻结公开模块路径；当前 Go 包全部放在 `internal/`，不承诺第三方导入路径；
- Go 与 Node fixture 必须共同验证 NotificationEvent；
- Release 必须先产生并验证 Core，再发布 npm bootstrap。

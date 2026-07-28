# ADR 0003：M2 可回滚状态、稳定 Bridge 与远程 Relay

- 状态：Accepted
- 日期：2026-07-26

## 背景

M1 `config.json` 严格拒绝未知字段，queue reader 严格要求 `queueVersion=1`，Adapter Hook
和登录服务又直接引用版本化 Core 路径。直接扩展主配置、提升队列版本或原地替换 Core，
都会破坏 M2 要求的安全回滚。

远程运行面还需要跨主机身份、认证、receipt 和 hop，但 NotificationEvent v1 使用
`additionalProperties=false`；把网络字段塞入事件会同时破坏 Adapter 协议和旧 Core。

## 决策

1. 保持 M1 `config.json` 形状；模板、明确事件开关、免打扰、策略、remote 和 relay
   使用各自有版本的严格 sidecar。回滚前检查 `minCoreVersion`。
2. queueVersion 暂时保持 1；逐通道 delivery ledger、defer 和 disposition 使用旧 Core
   会忽略的可选字段。存在部分成功 ledger 时禁止静默回滚。
3. Codex、Claude Code、Kimi Code Hook 和三平台登录服务迁移到版本无关的原生 bridge。
   bridge 只支持 `hook-v1`、`service-v1` 白名单，并通过原子 active pointer 分发到
   Core。回滚到不能解析当前配置的 pre-M2 Core 时，Hook 分发到旧 active Core，
   `service-v1` 使用 active state 中 checksum 校验的当前 M2 service Core。
4. runtime proof 增加 bridge protocol、Core version 和 activation generation；
   `diagnose` 不接受其他 generation 的旧 proof。
5. NotificationEvent v1 保持不变；远程传输新增 RelayEnvelope v1。远端 Hook 只写 durable
   outbox，receiver 在 receipt 与本机 delivery queue 都持久提交后才 ACK。
6. Relay peer 使用 Ed25519 设备密钥、origin/scope 限制、时间戳、nonce replay cache 和
   独立 receipt ledger。跨主机幂等域为 team + origin + producer key。
7. WSL、SSH、Container 优先复用 host-pull stdio connector；HTTPS push 必须显式配置
   TLS。Vendor Cloud 只接公开且可验签的厂商生命周期回调。
8. 插件 bundle 使用 Sigstore keyless 签名，并固定 OIDC issuer、repository 和 workflow
   identity；`technical-preview` 不等于签名成功。

## 后果

- M2 新 Core 可以在缺少 sidecar/ledger 时保持 M1 行为，升级失败可原子恢复 previous。
- pre-M2 rollback 不需要改写或复制含凭据的 `config.json`，旧 Core 也不会因新字段
  导致后台服务持续退出。
- 首次把三个旧 Hook 迁移到 bridge 仍需按产品要求重新加载；之后升级/回滚不再修改
  Hook 命令。
- WSL 不需要开放 Host HTTP 端口；SSH/容器也不需要三套独立数据面。
- 最终飞书发送仍是 at-least-once；AgentBell 只承诺 relay 和逐通道投递边界的持久去重，
  不宣传端到端 exactly-once。
- 手工回滚、secret store 降级、非 loopback listener 和未签名预览插件都需要显式用户
  决策，不能静默降低安全性。

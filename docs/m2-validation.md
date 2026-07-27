# AgentBell M2 验收台账

## 状态

| 项目 | 结论 |
| --- | --- |
| 记录日期 | 2026-07-27 |
| 本地实现 | P0～P6 自动化与本地门禁已实现 |
| 发布状态 | 未发布，仍为 Technical Preview / Pilot |
| M2 退出结论 | **未通过**：真实 Release、三平台和跨主机实机证据尚未产生 |

本台账只记录可复现证据，不把 mock、交叉编译或自动 fixture 描述成实机通过。Adapter
等级继续由 `docs/adapter-contract.md` 的产品实机矩阵决定。

## 自动化证据索引

| 任务 | 当前证据 | 状态 |
| --- | --- | --- |
| M2-001～004 协议与迁移骨架 | `docs/adr/0003-m2-compatible-state-and-relay.md`、`core/testdata/migrations/`、严格 sidecar/queue tests、三平台 migration matrix | 本地通过；最终 Actions 待产生 |
| M2-101～104 设置与策略 | `core/internal/settings/`、`policy/`、`service/m2_test.go`、delivery ledger tests | 已实现 |
| M2-201～204 一次性绑定 | `core/internal/binding/`、`app/bind_test.go`、setup binding tests | 自动测试已实现；现有 bot 通道真实发送通过，一次性绑定仍待验 |
| M2-301～305 bridge/升级回滚 | `installstate/`、`bridge/`、`adapter/stable_bridge_test.go`、`tests/upgrade.test.mjs`、`scripts/release-lifecycle-smoke.mjs` | 真实旧 Release asset 到本地工作树候选通过；真实新 RC 待验 |
| M2-401～404 Hook/插件签名 | `adapter/hook_audit_test.go`、`pluginverify/`、`tests/plugin-bundles.test.mjs`、`tests/release-workflow.test.mjs` | 自动测试已实现；真实 tag keyless bundle 待验 |
| M2-501～506 Relay/Remote | `relay/`、`remote/`、`remoteconfig/`、`secretstore/`、`app/remote*_test.go`、`scripts/smoke-https-relay.mjs` | 自动测试、macOS Host→Linux container stdio 与隔离 Linux TLS/HTTPS E2E 通过；独立跨主机到飞书待验 |
| M2-601 doctor | `doctorSchemaVersion=1`、顶层 doctor golden、bridge doctor、connector runtime proof tests | 本地通过；输出脱敏 |
| M2-602 性能/压力 | stable bridge Hook p95、Relay/32 路 fan-out/queue benchmark、96 durable item stress gate | 本地通过 |
| M2-603 跨平台 CI | 六目标 Core+bridge 构建、Go race、Node/Go、三平台 migration 与 Ubuntu TLS/HTTPS smoke workflow | 本地构建/race/TLS smoke 通过；最终 commit 的 Actions run 待产生 |
| M2-604 实机矩阵 | 下表 | 未通过 |
| M2-605 Release | draft-before-npm、最终 Linux Core TLS smoke、上一 Release lifecycle smoke、checksum、插件 keyless、下载后复验 workflow | workflow 已接线且本地旧 Release asset/TLS smoke 通过；真实新 RC 未运行 |

2026-07-27 本地最终门禁：

- Node.js：87 项测试全部通过，生产模块行覆盖率 82.02%，门禁通过；
- Go：fmt/vet/fuzz/测试与覆盖率门禁通过，总覆盖率 79.6%；所有规定的 M2 数据面包
  均不低于 80%；
- `go test -race ./... -count=1` 全包通过；
- migration fixture、八份版本 manifest、一致性与 npm workspace pack 预检通过；
- Windows/macOS/Linux 的 amd64/arm64 Core 与 stable bridge 共 12 个二进制构建通过。

## 真实上一 Release 资产的本地生命周期 smoke

2026-07-27 从 GitHub Release `v0.2.0-rc.3` 下载真实
`agentbell-darwin-arm64`，其 SHA-256
`ea77e428213bd94b86dc768e71b03aee2106a73c9f943741a451d87d9e71d0b4`
与 Release asset digest 一致。使用该旧 Core 和当前未提交工作树构建的
`0.3.0-rc.1` macOS arm64 Core/bridge，在隔离数据根执行 lifecycle smoke，结果为：

- 从真实 M1 形状（旧 `install.json` 不含 `schemaVersion`、没有 `active.json`）迁移，
  upgrade 建立 generation `2` 并把 `0.2.0-rc.3` 纳入 `previous`，rollback 建立
  generation `3`；
- 三个稳定 Hook 文件在升级和回滚前后字节不变；
- stable bridge 在新 Core 和回滚后的真实旧 Core 上各成功入队一次；
- 升级和回滚后各由当前候选 Core 执行一次 `bridge doctor`，两次均为 healthy/active；
- 两次 service restart callback 均执行，产品卸载 dry-run 成功。

首次真实运行发现 lifecycle 脚本漏传 bridge 的
`--runtime host --stdin --fail-open` 参数；bridge 按白名单拒绝但 Hook fail-open 返回
成功，使旧 mock 测试形成假绿。修复后新增精确 argv 回归测试，并重新跑通上述真实旧
Release smoke。

后续完成审计时又发现 smoke 的旧版本 fixture 预先写入了 M2 `active.json` 和
schema v1 metadata，绕过了真实 M1→M2 首次迁移。新增真实 M1 metadata、多个旧版本
`--from` 消歧、失败恢复 legacy service、stable bridge checksum 和 rollback doctor
回归测试；修复后用同一真实 `v0.2.0-rc.3` 资产重跑，报告新增
`bridgeDoctors: 2` 并全部通过。

这只是“真实上一 Release asset -> 本地未提交工作树候选”的 macOS 隔离证据：
候选二进制的 commit ldflag 取当前 `HEAD`
`0765cd8cdcf01e761bbc4e6c2d1f639f09709747`，但工作树包含未提交改动，不能把该哈希解释为
候选源码的完整 provenance；restart callback 也不等于真实 LaunchAgent/计划任务/systemd
重启。新 RC 的 draft Release 下载复验、真实服务迁移与多平台证据仍未完成。

## macOS Host 到 Linux Container 的真实 stdio E2E

2026-07-27 在 macOS 26.4 arm64、Docker Engine 29.2.1 上，以固定镜像
`alpine@sha256:5b10f432ef3da1b8d4c7eb6c487f2f5a8f096bc91145e68878dd4a5019afde11`
运行 Linux arm64 容器。Host Core SHA-256 为
`0b9f0588716150f622cf2d10081955783eefdce7d407fdba7d569d534f08f83b`，Container Core
SHA-256 为 `c03b1ecabfe9feb2ac4a376e7a7533e8627023be31c8c116a6a8b3dff8383140`；两者均为
当前未提交工作树构建的 `dev/none` 二进制，不是 Release provenance。

真实进程链路为：

```text
Linux container remote emit
-> container durable outbox
-> macOS service container host-pull（docker exec -i，bounded stdio）
-> relay signature/scope/nonce/receipt
-> macOS durable queue
-> 测试 transport
```

验收结果：

- 容器私钥目录为 `0755` 时配对被 `unsafe secret storage` 正确拒绝；改为 `0700` 后，
  同一未消费绑定记录配对成功，私钥文件权限为 `0600`；
- Host 使用独立 connector registry 完成无 listener stdio 配对，peer 只获得
  `ingest` scope；
- 输入特意包含原始 session/turn、完整 cwd 和额外 prompt；容器持久数据不含这些明文，
  Host history 只含 metadata-only 事件和哈希标识；
- 首个事件产生 1 个 remote history、1 个 durable receipt 和 1 个 Host history；
  `doctor` 显示 queue/relay healthy、1 个 container connector 和 1 个 runtime proof；
- Host service 停止期间，同一个远端 Hook 连续注入只保留 1 个 pending item；重启后
  恢复为第 2 个 receipt/history。ACK 后再次注入同一事件，remote/Host history 均保持
  2，retrying 为 0；
- 实机重复注入最初暴露 `relay outbox body conflict`：同 producer key 的第二次
  normalization 生成了新 nonce/timestamp。新增失败回归测试后，remote producer 现会
  在生成新 envelope 前复用已有 durable delivery；relay 入口的 exact-body conflict
  负向门禁不变；
- 容器内实际执行产品级 `uninstall` 后，`remote.json`、outbox 与私钥按默认策略保留；
  携带双确认参数的 dry-run 只报告 `would-delete`，私钥仍存在；随后实际携带两个参数
  重跑卸载，报告 `deleted` 且私钥文件被删除。

本记录证明本机 macOS Host 与真实 Linux container 进程之间的 stdio connector/relay
链路，不证明独立物理 Linux 主机、HTTPS connector 或真实飞书到达。最终 transport 使用
`/usr/bin/true` 作为无网络测试 sink，因此 M2-604 仍未通过。

## Linux Container 的真实 TLS/HTTPS E2E

2026-07-27 使用当前未提交工作树的 `0.3.0-rc.1` Linux arm64 候选 Core，在隔离的
Docker bridge network 中分别运行 Relay 与 Remote service。测试使用临时 CA 证书，
客户端完成正常 x509 信任链校验并叠加 leaf SPKI SHA-256 pin；未启用
`InsecureSkipVerify`、明文 HTTP 或 SSH tunnel 例外。

真实进程链路为：

```text
Linux remote CLI/service
-> durable outbox
-> HTTPS + TLS certificate validation + SPKI pin
-> Linux relay listener
-> signature/scope/nonce/receipt
-> relay durable queue
```

验收结果：

- 一次性 Relay pairing code 经 HTTPS 消费后只登记 1 个 active peer；
- `remote test --wait 20s` 进入 outbox history 并取得 durable ACK；
- Relay 停止期间同一 Hook 连续注入两次，producer 只保留一个 delivery；Relay 重启后
  自动恢复，最终总计正好 2 个 history 和 2 个 committed receipt，而不是 3 个；
- 对完整隔离数据根扫描，原始 session/turn/summary 均未落盘；
- 运行中的 `doctor` connector proof 为 `https / healthy / running`；
- 测试完成后两个容器、临时 network、证书和私钥均清理。

该过程已固化为 `scripts/smoke-https-relay.mjs` 和 Ubuntu CI 的
`M2 TLS and HTTPS relay smoke` job。脚本只在 Linux 运行，并使用：

```bash
npm run smoke:https
```

脚本输出只有一行严格 proof；缺失任一 TLS、SPKI、pairing、ACK、去重、恢复、
metadata-only 或 runtime proof 字段都会失败。2026-07-27 又在隔离
`node:24-bookworm-slim` Linux arm64 容器中复用候选 Core 执行仓库正式脚本，取得：

```text
HTTPS_RELAY_SMOKE_PASS paired=1 deliveries=2 history=2 duplicate=1 recovery=1 metadata=1 connector=https state=healthy running=1 tls=verified spki=pinned
```

本记录证明同一台 macOS Docker Host 内两个真实 Linux 进程之间的 TLS/HTTPS 数据面，
包括断网、重启、ACK 与去重语义；它不证明独立物理主机、Windows/macOS HTTPS 客户端、
公网证书部署或真实飞书最终到达，因此仍不满足 M2-604。

## macOS 到真实飞书 bot 通道

2026-07-27 在 macOS 26.4 arm64 上，使用当前未提交工作树重建的
`artifacts/core/agentbell-darwin-arm64` 和既有的一个 bot 模式默认通道，实际执行
`agentbell test --json`。`lark-cli` 返回成功，AgentBell 输出 `ok: true`；本机队列在
测试前为 pending/inflight/dead 全部为 0。该命令设计为直接验证绑定结果，不经过 durable
queue。

首次实机发送同时发现成功 JSON 和文本输出会回显真实飞书 chat id。先新增 JSON、文本
两条失败回归测试，再移除该字段和文本括号中的目标标识；重建后第二次真实发送成功，
输出只保留 AgentBell channel id、`ok` 和 `sentAt`。这证明现有 bot 通道与真实飞书
transport 可达，也证明修复后的 CLI 输出不泄露 chat id；它不证明 M2 一次性绑定流程、
user 模式、多通道策略或后台 service 投递。

## 当前本机性能记录

2026-07-27，macOS / Apple M1 Pro：

| 场景 | 结果 | 判据 |
| --- | --- | --- |
| stable bridge `hook-v1` → Core → durable queue | p95 4.43 ms，max 4.51 ms，n=35 | p95 < 200 ms |
| RelayEnvelope decode + peer/scope/Ed25519 验签 | 96,340 ns/op，100 次 | 记录回归，不设跨机器绝对阈值 |
| 32 路策略 fan-out | 8,709 ns/op，100 次 | 记录回归，不设跨机器绝对阈值 |
| durable queue enqueue | 13,037,575 ns/op，100 次 | 记录回归，不设跨机器绝对阈值 |
| durable relay 故障恢复 | 96 items / 96 unique deliveries / 288 attempts / 24 crash recoveries | 精确语义门禁通过 |

复现命令：

```bash
npm run perf:m2
```

## 实机矩阵

| 场景 | macOS | Windows | Linux | WSL / SSH / Container |
| --- | --- | --- | --- | --- |
| 飞书 user/bot 一次性绑定 | 既有 bot 通道直接发送通过；一次性绑定与 user 模式待验 | 待验 | 待验 | Host 绑定后复用，待验 |
| 设置/模板/免打扰/多通道 | 待验 | 待验 | 待验 | Host policy，待验 |
| 安装、升级、自动回滚 | 待验 | 待验 | 待验 | shim 独立升级，待验 |
| Codex/Claude/Kimi stable Hook | 待验 | 待验 | 待验 | 按产品运行位置待验 |
| 登录服务重启/断网恢复 | 待验 | 待验 | 待验 | 待验 |
| WSL host-pull，无 listener | 不适用 | 待验 | 不适用 | 待验 |
| SSH strict host-key + tunnel | 可作 Host，待验 | 可作 Host，待验 | 可作 Host，待验 | 待验 |
| Container stdio/HTTPS | 本机 Linux arm64 container stdio host-pull 与隔离 TLS/HTTPS E2E 通过；真实飞书待验 | 可作 Host，待验 | 同 Host 双 container TLS/HTTPS E2E 通过；独立主机待验 | container stdio 与 TLS/HTTPS 本机通过；跨主机待验 |
| 统一卸载及默认保留 | 自动预检/双确认通过，实机待验 | 自动 fixture 通过，实机待验 | 自动 fixture 通过，实机待验 | credential/peer 默认保留与脱敏自动通过，实机待验 |

## 单次实机记录模板

每次记录必须包含：

- AgentBell 版本、commit、目标 OS/架构和产品版本；
- 使用的命令和已脱敏配置摘要；
- upgrade 前后 stable Hook 文件 SHA-256、active generation 和 runtime proof；
- queue/outbox/receipt 的计数与状态；
- 飞书到达结果、断网/重启/ACK 丢失后的恢复与去重结果；
- rollback/uninstall 结果及默认保留项；
- CI run 或 Release URL。

禁止记录 token、chat id、绑定码、私钥、公钥全文、endpoint/host、prompt、代码、完整
任务正文、完整 cwd 或原始 Hook JSON。

## M2 退出前仍需补齐

1. 对最终 commit 取得 GitHub Actions 全绿记录；
2. 创建新 RC，证明真实 draft Release smoke 在 npm publish 前成功；
3. 用真实上一 Release 完成安装 → upgrade → Hook 字节不变 → fixture 发送 →
   rollback → uninstall，并保存 Release/CI 链接；
4. 补齐 macOS、Windows+WSL、Linux、SSH 和 container 的端到端记录；
5. 真实 Codex/Claude/Kimi 任务生成配置变更后的 runtime proof；
6. 将本表中的“待验”替换为证据链接后，才可把实施计划状态改为完成。

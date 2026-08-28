# AgentBell M2 验收台账

## 状态

| 项目 | 结论 |
| --- | --- |
| 记录日期 | 2026-08-03 |
| 本地实现 | P0～P6 自动化与本地门禁已实现 |
| 发布状态 | `v0.3.0-rc.7` 已发布，仍为 Technical Preview / Pilot |
| M2 退出结论 | **未通过**：RC7 发布完成；三平台完整矩阵和独立跨主机实机证据尚未完成 |

本台账只记录可复现证据，不把 mock、交叉编译或自动 fixture 描述成实机通过。Adapter
等级继续由 `docs/adapter-contract.md` 的产品实机矩阵决定。

## 自动化证据索引

| 任务 | 当前证据 | 状态 |
| --- | --- | --- |
| M2-001～004 协议与迁移骨架 | `docs/adr/0003-m2-compatible-state-and-relay.md`、`core/testdata/migrations/`、严格 sidecar/queue tests、三平台 migration fixture | 本地与 Actions 通过 |
| M2-101～104 设置与策略 | `core/internal/settings/`、`policy/`、`service/m2_test.go`、delivery ledger tests | 已实现 |
| M2-201～204 一次性绑定 | `core/internal/binding/`、`app/bind_test.go`、setup binding tests | 自动测试已实现；现有 bot 通道真实发送通过，一次性绑定仍待验 |
| M2-301～305 bridge/升级回滚 | `installstate/`、`bridge/`、`adapter/stable_bridge_test.go`、`tests/upgrade.test.mjs`、`scripts/release-lifecycle-smoke.mjs` | 真实旧 Release → `v0.3.0-rc.4` Draft 生命周期与 macOS LaunchAgent 迁移/回滚/卸载通过 |
| M2-401～404 Hook/插件签名 | `adapter/hook_audit_test.go`、`pluginverify/`、`tests/plugin-bundles.test.mjs`、`tests/release-workflow.test.mjs` | 自动测试和 `v0.3.0-rc.4` 真实 tag 的五个 keyless 插件 bundle 验证通过 |
| M2-501～506 Relay/Remote | `relay/`、`remote/`、`remoteconfig/`、`secretstore/`、`app/remote*_test.go`、`scripts/smoke-https-relay.mjs` | 自动测试、macOS Host→Linux container stdio 与隔离 Linux TLS/HTTPS E2E 通过；独立跨主机到飞书待验 |
| M2-601 doctor | `doctorSchemaVersion=1`、顶层 doctor golden、bridge doctor、connector runtime proof tests | 本地通过；输出脱敏 |
| M2-602 性能/压力 | stable bridge Hook p95、Relay/32 路 fan-out/queue benchmark、96 durable item stress gate | 本地通过 |
| M2-603 跨平台 CI | 六目标 Core+bridge 构建、Go race、Node/Go、三平台 migration 与 Ubuntu TLS/HTTPS smoke workflow | 旧 13-job 基线由 Actions run 30352561499 证明；2026-07-30 分层 workflow 已完成本地验证，首次远程 run 待提交后补证 |
| M2-604 实机矩阵 | 下表 | 未通过 |
| M2-605 Release | draft-before-npm、最终 Linux Core TLS smoke、上一 Release lifecycle smoke、checksum、插件 keyless、下载后复验 workflow | RC7 沿用两阶段 workflow 发布，并增加 Bridge/Service 安装自愈；RC5 的 27 个 Release 资产、两个 npm 包、Trusted Publisher 和最终安装 smoke 已通过，历史 GitHub 账单拒绝启动的 job 使用下节记录的精确资产人工兜底 |

## 2026-08-03 RC7 Windows 安装自愈

RC6 Windows 实机出现 `agentbell test` 可直发飞书，但 Codex 任务结束没有通知。只读诊断
确认用户 Hook 仍指向 stable bridge，而 Bridge 文件与 `\AgentBell\AgentBell` 当前用户
计划任务均缺失；队列保持为空，因此失败发生在事件入队之前。RC6 bootstrap 的同版本
复用分支只校验 Core 安装元数据，不校验或恢复 Bridge，也不会重新对账 Service。

RC7 的 bootstrap 修复包含：

- 所有安装/升级幂等执行 `service install`，已存在的定义会更新并重启，缺失定义会重建；
- 同版本重装校验 Core、install metadata、active state 与 stable bridge checksum；Bridge
  缺失或损坏时，从同一不可变 Release 重新下载并验证 Core/Bridge checksum，原子恢复
  Bridge、提升 generation 并写入 `repair` 事务；
- Release 资产 checksum 与 active state 不一致时拒绝修复，Service 对账失败时恢复原 active
  state 和 Bridge 字节；
- Go app 测试统一隔离 `AGENTBELL_DATA_DIR`，避免测试读取真实用户安装；Windows runner
  命令测试改用显式 `cmd.exe`，并补 DPAPI round-trip/无效密文测试及 volume-root Bridge
  路径拒绝测试。

本机 `npm run ci` exit `0`：108 项 Node 测试中 107 通过、1 项 Windows 权限相关跳过，
Node 行覆盖率 76.47%；Go 总覆盖率 79.4%，Windows `installstate` 81.7%、`secretstore`
83.5%，其余规定数据面包均通过覆盖率门槛；两个 RC7 npm pack 预检通过。真实公开 RC7
安装、Codex 新任务 runtime proof 与手机最终到达仍需在 Release 发布后由 Windows 实机
端到端验收，不能由自动测试替代。

## 2026-07-30 CI runner 使用优化

现行工作树把 Draft、Ready、标准 GitHub merge commit 和 Markdown-only 改动分层；
完整矩阵从 13 个 job 收敛到 9 个。Ubuntu Node.js 24 由质量 job 覆盖，三平台
migration fixture 合并进现有质量/兼容 job，M2 stress 不再重复 `perf:emit`，六目标
Core/bridge 在单个 Ubuntu runner 内默认两路并行。`.github/workflows/release.yml`
未修改，stage/finalize 的发布门禁不受节流。

本地证据：

- `npm run ci` exit `0`：104 项 Node 测试通过，Node 行覆盖率 76.23%，Go 总覆盖率
  79.6%，规定的数据面包覆盖率门禁与两个 npm pack 预检通过；
- `npm run test:migrations` 在本机通过，工作流回归测试确认该入口仍接入 Linux
  quality 及 Windows/macOS compatibility job；emit p95 `5.59ms` 和 96-item durable
  relay stress gate 通过；
- 六目标 Core + bridge 共 12 个二进制构建通过；相同 version/commit/buildTime 下，
  默认两路并行与强制单路构建的文件集合和字节完全一致；
- CI/docs/release 三个 workflow 通过 YAML 解析，19 份 Markdown 的本地路径检查通过。

这些证据证明工作树实现与产物不变量，不等于新 workflow 已在 GitHub-hosted
Windows/macOS/Linux runner 上通过。首次 Ready PR 的完整远程 run 成功后，应把链接
补到本节；在此之前不得把旧 13/13 run 描述成新分层 workflow 的成功证据。

当前 Release 流程增加了两段式人工发布闸门和四个不可逆发布前门禁：Tag push 或手动
`stage` 只生成并复验 Draft，必须从同一 Tag 手动 `finalize` 才能进入 npm/公开发布；
上一 Release lifecycle 必须实际执行
产品卸载与 bootstrap Core 清理并验证默认保留项；两个最终 npm tgz 必须进入
checksums/release manifest；`stage` 和 `finalize` 都必须从 draft Release 下载并安装
smoke 这两个 tgz；已公开的同 tag Release 不允许 `--clobber` 或在失败补偿中退回 draft。
npm registry 不提供两个包的跨包原子事务，首包成功、次包失败时仍需以同一版本重跑
补齐，不能把该
补偿模型描述成原子发布；重跑只在已发布版本的 registry `dist.integrity` 与最终 tgz
完全一致时跳过。RC5 已完成 npm 首发、Trusted Publisher 配置和公开 prerelease；
标准 workflow 因 GitHub 账单状态被拒绝启动的部分由下节人工等价门禁补齐。

## `v0.3.0-rc.5` 发布与外部平台故障兜底

2026-07-28，PR #5 合并 commit
`98ce5a8033cec504352e174ba5ea1059beaf56ad` 后创建不可变 Tag
`v0.3.0-rc.5`。最终
[GitHub prerelease](https://github.com/liming0791/agentbell/releases/tag/v0.3.0-rc.5)
包含 27 个资产；npm 已公开：

- `@liming0791/agentbell-cli@0.3.0-rc.5`；
- `@liming0791/agentbell-hook-runtime@0.3.0-rc.5`。

[PR CI 30361271121](https://github.com/liming0791/agentbell/actions/runs/30361271121)
13/13 job 全绿。Tag 的
[Release run 30361896587](https://github.com/liming0791/agentbell/actions/runs/30361896587)
中 build job 完成全量 CI、六目标 Core/bridge、Linux TLS/HTTPS smoke、五个插件
keyless 签名、lifecycle/npm bootstrap smoke 和 artifact 上传；Stage Draft job 在启动前
被 GitHub 以账号账单状态拒绝。后续
[lifecycle run 30365393064](https://github.com/liming0791/agentbell/actions/runs/30365393064)
与
[finalize run 30371902184](https://github.com/liming0791/agentbell/actions/runs/30371902184)
也在启动前被同一外部条件拒绝，不能记作测试失败或成功。

人工兜底严格使用同一成功 build job 的精确 artifact：12 个确定性重建的 Core/bridge
SHA-256 与 CI 匹配，两个 npm tgz、`checksums.txt` 和 manifest 匹配，27/27 GitHub
asset digest 匹配，五个插件的 OIDC issuer/repository/workflow identity 通过。随后在
一次性 Linux ARM64 Node 24 容器中用真实 `v0.2.0-rc.3` 和最终 RC5 资产执行：

```text
install -> upgrade generation 2
-> 三个稳定 Hook 字节不变
-> fixture + bridge doctor
-> rollback generation 3
-> fixture + bridge doctor
-> actual uninstall
```

实际卸载移除受管 runtime、stable bridge、active state 和五个 Linux Adapter Hook，
两个非 Linux Adapter 明确 skipped；配置和状态按默认策略保留。两个 npm registry
integrity 与 Draft tgz 的 SHA-512 完全一致，并已为
`liming0791/agentbell`、`release.yml`、`npm-publish` 配置 GitHub OIDC Trusted
Publisher。最后从已发布 npm CLI 在隔离 macOS 数据根安装 Draft 的 darwin-arm64 Core，
版本、commit、build time、SHA-256 与 bridge doctor 均通过，才公开 GitHub prerelease。

仓库仍为 private。npm 包本身公开，但 bootstrap 下载 GitHub Core asset 时，外部用户
仍需 `AGENTBELL_GITHUB_TOKEN` 或 `GH_TOKEN` 具备仓库读取权限；这属于当前分发限制，
不应被描述成公开匿名安装。

## RC4 finalize 的 npm scope 阻断与 RC5 迁移

2026-07-28 从精确 Tag `v0.3.0-rc.4` 执行
[finalize run 30359369321](https://github.com/liming0791/agentbell/actions/runs/30359369321)。
Draft metadata、27 个资产 checksum、Core/Bridge、五个插件和两个 npm tgz 复验均通过；
首个 `npm publish` 对 `@agentbell/hook-runtime` 返回 `E404`，公开 GitHub Release
步骤按设计 skipped。随后确认 `@agentbell` scope 已属于无关第三方项目，本仓库从初始
提交起使用该名字但从未拥有或发布对应 npm 包。

发布开关已恢复为 `false`，RC4 保持 Draft，两个旧 scope 目标包继续为 E404。RC5
改用当前 npm 用户 scope：`@liming0791/agentbell-cli` 和
`@liming0791/agentbell-hook-runtime`；npm pack 资产名相应增加 `liming0791-` 前缀。
workflow、Draft 下载 smoke、manifest/checksum 测试、安装后模块路径和 finalize 的
integrity 复验必须全部使用新名字。旧公开 Release 的 npm bootstrap asset 名不改，
继续作为上一 Release lifecycle 输入。

## `v0.3.0-rc.2` 真实 Draft Release stage

2026-07-28 创建并推送 tag `v0.3.0-rc.2`，精确指向 commit
`ef210e7a8fd29a17d47687ea734443ac7131d7d7`。对应
[Release Actions run 30338745040](https://github.com/liming0791/agentbell/actions/runs/30338745040)
成功，真实 [Draft Release](https://github.com/liming0791/agentbell/releases/tag/untagged-a996a2cbde2c8c6ae5c7)
保持 `draft=true`、`prerelease=true`，包含 27 个最终资产。证据包括：

- 六目标 Core 和六目标 stable bridge 构建、Linux Core TLS/HTTPS smoke 通过；
- 五个插件完成 keyless 签名，并由最终 host Core 验证；
- 从真实上一 Release 下载旧 Core，完成安装 → 升级 → Hook 字节不变 → fixture
  发送 → rollback → uninstall；
- 创建 Draft 后重新从 Draft 下载 Core/bridge、五个插件 archive 和两个最终 npm
  tgz，bootstrap 安装、插件验证、checksum/release manifest 与 npm package smoke
  全部通过；
- `Re-verify and publish the draft release` job 明确为 skipped，stage 最后一步确认
  Release 继续保持 Draft；npm registry 对
  `@agentbell/cli@0.3.0-rc.2` 与
  `@agentbell/hook-runtime@0.3.0-rc.2` 均返回 `E404`，证明本次 stage 没有提前发布 npm。

首次 `v0.3.0-rc.1` stage 在 Draft 创建后暴露 GitHub API 语义差异：认证的
`releases/tags/{tag}` 对 Draft 返回 404，而认证的 release list 会包含 Draft。
CLI bootstrap 和 upgrade downloader 已改为先按 tag 查询、404 时安全分页查找精确
Draft tag，并增加认证 token 不出现在 URL、只进入 Authorization header 的回归测试。
因此没有强移失败标签，而是以新 tag `v0.3.0-rc.2` 重跑并通过完整 stage。

## 真实上一 Release → 最终 Draft lifecycle

2026-07-28 使用已公开的上一版
[Release `v0.2.0-rc.3`](https://github.com/liming0791/agentbell/releases/tag/v0.2.0-rc.3)
和保持未公开的最终
[Draft Release `v0.3.0-rc.2`](https://github.com/liming0791/agentbell/releases/tag/untagged-a996a2cbde2c8c6ae5c7)，
在 [Release Actions run 30342246644](https://github.com/liming0791/agentbell/actions/runs/30342246644)
完成独立 lifecycle 验证。验证 harness commit 为
`76d0396a5b048c81dd9883de565d9a4243505d1f`，run 中保留
`draft-release-lifecycle-evidence` artifact。结果：

- 从两个真实 Release 边界下载资产，完整校验最终 Draft 的
  `checksums.txt`、`release-manifest.json`、版本、tag commit 和
  `technical-preview` 状态；
- 解包上一 Release 的真实 npm CLI，以其 bootstrap 下载并安装
  `0.2.0-rc.3` Core；安装后的 Core 字节与上一 Release 资产完全一致；
- 升级到最终 Draft `0.3.0-rc.2` 后 active generation 为 2；三个稳定 Hook
  文件逐字节不变，最终 stable bridge 字节与 Draft 资产一致；
- upgrade 和 rollback 后各发送一次真实 Hook fixture，均成功进入持久队列；
  两次 bridge doctor 均健康；
- rollback 回 `0.2.0-rc.3` 后 active generation 为 3，Hook 仍逐字节不变，
  stable bridge 未被旧版本覆盖；
- 统一卸载真实执行：移除 active runtime、stable bridge、active state 和五个
  Linux 可用 Adapter Hook；两个非 Linux Adapter 明确跳过；配置和状态保留项均在；
- lifecycle 后再次确认目标仍为 `draft=true`、`prerelease=true`、27 个资产，
  `@agentbell/cli@0.3.0-rc.2` 和
  `@agentbell/hook-runtime@0.3.0-rc.2` 均由 npm registry 明确返回 E404；
  build、stage、finalize job 全部 skipped，没有发布或改写 Release。

## 真实 macOS 上一 Release → 最终 Draft → rollback → uninstall

2026-07-28 在 macOS 26.4 arm64 真机上，使用已公开的上一版
[Release `v0.2.0-rc.3`](https://github.com/liming0791/agentbell/releases/tag/v0.2.0-rc.3)
和保持未公开的最终
[Draft Release `v0.3.0-rc.4`](https://github.com/liming0791/agentbell/releases/tag/untagged-2a09efecec2a168519ab)
完成真实用户目录与 LaunchAgent 生命周期。最终 Draft commit 为
`aa3ace2a5fdca03efe584af712b8a99cfa87aab1`；
[PR CI 30352561499](https://github.com/liming0791/agentbell/actions/runs/30352561499)
的 13 个 job 全部通过，
[Release run 30352951065](https://github.com/liming0791/agentbell/actions/runs/30352951065)
完成 build、Draft staging 与资产 smoke；上一 Release lifecycle 和 finalize job 均
按触发条件 skipped。Linux 的自动上一 Release lifecycle 证据仍来自前述 RC2 run
30342246644，最终 RC4 的完整 lifecycle 由本节 macOS 真机验证覆盖。
两个 RC4 npm 包在 registry 均返回 `E404`。

执行前创建权限为 `0700/0600` 的受保护备份，保存原 Core、active state、LaunchAgent、
九个可能受影响的产品配置、上一 Release 与 Draft 资产及 SHA-256 清单。真实流程先用
上一 Release npm CLI 下载并安装 `0.2.0-rc.3`；安装后的 Core SHA-256
`ea77e428213bd94b86dc768e71b03aee2106a73c9f943741a451d87d9e71d0b4`
与 Release 资产一致。随后安装并验证七个 macOS Adapter：Codex、Claude Code、
Kimi Code、OpenCode、Qoder、QoderWork CN 和 TRAE CN。

首次使用 RC3 验证时，真机发现两个自动 fixture 没覆盖的兼容问题：

- rollback 原先让目标旧 Core 执行 `service restart`；旧 Core 没有该子命令。修复为
  当前 Core 负责重启 stable bridge；
- 修复后 active 切换成功，但旧 Core 严格拒绝当前 `config.json` 中的
  `larkCliPath`，LaunchAgent 因而无法消费队列。失败事件保持在 durable queue，
  恢复 RC3 后通过带审计字段的 `queue retry` 成功投递，最终 dead 归零。

第二个问题在 RC4 中改为：pre-M2 rollback 的 Hook 继续分发到旧 active Core，
`service-v1` 则使用 active state 中显式记录的 `serviceVersion` 和
`serviceChecksum` 选择当前 M2 Core；Bridge 和 `bridge doctor` 都校验该二进制。
这不修改或复制含凭据的 `config.json`。Node rollback、Go bridge/installstate/doctor
回归、完整本地 CI 与三平台 PR CI 均通过后才创建新的不可变 RC4 Draft。

最终 RC4 真机结果：

- 从 Draft 下载的 CLI、Core、Bridge、checksums 和 manifest 与 GitHub asset digest
  全部一致；Core SHA-256 为
  `d903ac72d93a707f26daca16747ae0b0ec31fb8eb8cd9048734f6ae02a123049`，
  stable bridge 为
  `90eac7d503d0c6997a4f763a2980f2e59e2f1302b019050b14cb542f0b53b745`；
- 升级到 RC4 后 active generation 为 8，LaunchAgent running；七个产品 Hook
  SHA-256 与升级前逐字节一致。真实 metadata-only Codex Desktop Stop 经 stable
  bridge → durable queue → LaunchAgent → 飞书投递，history 93→94，
  pending/inflight/dead 均为 0；
- rollback 到真实 `0.2.0-rc.3` 后 generation 为 9，active/service 分别为
  `0.2.0-rc.3`/`0.3.0-rc.4`。进程树证明 stable bridge 实际启动 RC4
  `service run --foreground`，七个 Hook 仍逐字节不变；第二条真实 fixture 使
  history 94→95，pending/inflight/dead 仍为 0；
- 统一卸载 dry-run 与实际执行均通过：LaunchAgent/plist、七个 AgentBell Hook、
  active state、stable bridge 和 active `0.2.0-rc.3` runtime 均被移除；
  OpenCode 独占插件文件删除，其他六个产品配置中无 AgentBell 标记且与各自安装前
  精确备份 SHA-256 匹配；
- `config.json` 与执行前备份 SHA-256 均为
  `94ecea773c13b042578e8af69c4589b35f1833be50c800133bf62250a1e883c8`；
  95 条 history、交易日志、诊断备份和非 active 版本缓存按默认保留策略保留，
  pending/inflight/dead 为 0，所有 Adapter receipt 已移除。

该记录关闭 macOS 上真实 Release 安装、Draft 升级、旧 Release 回滚、登录服务持续
发送、七 Adapter 精确卸载与默认保留项验证；不替代 Windows/Linux 实机与断网恢复。

2026-07-27 本地最终门禁：

- Node.js：95 项测试全部通过；六个 npm bootstrap 生产模块行覆盖率均不低于 80%；
- Go：fmt/vet/fuzz/测试与覆盖率门禁通过，总覆盖率 79.6%；所有规定的 M2 数据面包
  均不低于 80%；
- `go test -race ./... -count=1` 全包通过；
- migration fixture、八份版本 manifest、一致性与 npm workspace pack 预检通过；
- Windows/macOS/Linux 的 amd64/arm64 Core 与 stable bridge 共 12 个二进制构建通过。

2026-07-28 的 [Actions run 30332294570](https://github.com/liming0791/agentbell/actions/runs/30332294570)
在候选 `41bb573` 上由 Windows runner 发现 `remoteconfig` 原子 sidecar 覆盖仍直接使用
`os.Rename`，目标已存在时返回 `ACCESS_DENIED`。修复没有把失败改成成功或删除目标文件：
Windows 现使用 `MoveFileEx` 的 `REPLACE_EXISTING | WRITE_THROUGH`，其他平台继续使用
`os.Rename`。针对性测试、Windows amd64 交叉编译和完整本地 `npm run ci` 通过后，
候选 `964a09f` 的 [Actions run 30333419935](https://github.com/liming0791/agentbell/actions/runs/30333419935)
全部 13 个 job 通过；Draft PR #4 保持 CLEAN。该证据关闭 M2-603 的本轮回归，不等于
Windows 实机服务、WSL host-pull 或产品 Hook 验收。

## Linux Container 的真实 Release lifecycle uninstall

2026-07-27 使用 GitHub Release `v0.2.0-rc.3` 的真实 Linux amd64 Core，以及当前未提交
工作树构建的 `0.3.0-rc.1` Linux amd64 Core/bridge，在只读挂载工作区的
`node:24-bookworm-slim` 容器中执行完整 lifecycle。结果为 upgrade generation `2`、
rollback generation `3`，三个稳定 Hook 字节不变，两次 bridge fixture 和两次 doctor
均通过。随后不是 dry-run，而是实际执行统一产品卸载和 npm bootstrap Core 清理：

- 受管 runtime、stable bridge 和 `active.json` 均已删除；
- Codex、Claude Code、Kimi Code、OpenCode、Qoder 的隔离 Hook 均已删除；
- QoderWork 和 TRAE 在 Linux 上不适用，卸载报告分别给出明确 skipped/no-op；
- 配置与状态哨兵按默认保留策略保持原字节。

该证据验证了 Linux 隔离环境中的产品级实际卸载，不等于 Linux 实机 systemd user 服务
迁移；真实新 tag/draft Release 下载复验也仍未执行。

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
重启。该隔离 smoke 当时仍缺真实服务迁移；下节已补 macOS，真实新 RC 的 draft
Release 下载复验和 Windows/Linux 证据仍未完成。

## macOS 真实 LaunchAgent 的 M1→M2 迁移

2026-07-27 在 macOS 26.4 arm64 上，使用 commit `78c9312f124a` 构建
`0.3.0-rc.1` Core 与 stable bridge。迁移对象是正在运行的开发期 M1 形态：
LaunchAgent `com.agentbell.service` 直接指向工作树开发 Core，`active.json` 不存在，
队列为 history 78、pending/inflight/dead 均为 0。

迁移前将原 plist、当前 Core、`config.json` 与受管 `bin/` 复制到权限为 `0700` 的
`~/Library/Application Support/AgentBell/backups/m2-migration-20260727T025505Z/`；
备份 plist/Core 的 SHA-256 与迁移前对象一致。当前 Core 被按原字节登记为本地
`0.2.0-local.20260725` legacy previous，使自动补偿可恢复完全相同的二进制，而不是
替换为另一个 Release。

只读 preflight 扫描 78 个队列 item，确认没有部分成功 ledger，四个 M2 sidecar 均未
出现。首次实机执行前发现 bootstrap 的生产默认路径只调用
`service restart --json`：自动测试因注入 callback 而没有证明 plist 会迁移。按 TDD
先加入服务动作决策与默认生产路径集成测试，再修复为：

- 首次无 active state 的 upgrade 由新 Core 执行 `service install --json`；
- 没有旧 active state 的失败补偿由旧 Core 执行 `service install --json`，恢复 legacy；
- 已有 M2 active state 的正常升级、补偿和显式 rollback 只执行
  `service restart --json`。

真实切换提交 transaction 后，结果为 active `0.3.0-rc.1`、previous
`0.2.0-local.20260725`、generation 2；plist 固定为
`bin/bridge/v1/agentbell-bridge service-v1`，LaunchAgent 以新进程保持 running。
active 中 Core/bridge checksum 与实物一致，transaction 为 committed，队列计数没有
变化。随后向 stable bridge 注入一条 metadata-only Codex Desktop Stop fixture，
后台 LaunchAgent 完成真实飞书投递：history 78→79，pending/inflight/dead 仍为 0。

该记录证明 macOS 本机真实服务定义迁移和 stable bridge → durable queue →
LaunchAgent → 飞书链路；没有人为注入服务启动失败，因此自动补偿仍由自动故障测试
证明。Windows/Linux 服务迁移、真实 tag/draft Release 下载和产品 Hook 字节不变仍待验。

PR 首轮 Windows Node.js 22 job 还发现默认服务动作测试使用 POSIX `#!/bin/sh` 临时
可执行文件，Windows `spawn` 正确返回 `ENOENT`。测试改为给生产执行器注入跨平台 fake
runner，并直接断言 executable、argv 和 stdio；生产默认仍调用同一个执行器，不再用
Unix 测试夹具冒充跨平台证据。

后续 [Actions run 30233941208](https://github.com/liming0791/agentbell/actions/runs/30233941208)
中 Windows Node 测试已通过，Windows Go runner 又暴露出四类独立的 POSIX 假设：
OpenCode receipt 校验没有按 JSON 字面量处理反斜杠、scheduler fixture 使用 POSIX
平台路径、权限测试直接比较 `0600/0700`，以及文件/目录锁没有把 Windows 的
`ACCESS_DENIED`、sharing violation 和 lock violation 识别为竞争。修复后：

- OpenCode 只接受唯一且精确的 `const executable = <JSON string>;` 声明；
- scheduler、lark-cli 与命令执行测试全部使用 host-native 路径或 Go helper 进程；
- POSIX 继续严格检查 `0600/0700`；Windows 检查普通文件、目录和非符号链接，并沿用
  当前用户 AgentBell 状态根的 DACL；
- Relay 与 remoteconfig 使用带随机 owner token 的 `O_EXCL` 文件锁，Windows
  短暂共享冲突进入有界重试，旧 owner 不会删除 token 不匹配的 successor lock；
- Relay 并发去重/容量压力和 remoteconfig 并发事务各连续 30 轮通过，相关包 Windows
  amd64 测试二进制交叉编译通过；完整本地 `npm run ci` 通过。

修复后的 [Actions run 30234809465](https://github.com/liming0791/agentbell/actions/runs/30234809465)
已有 12 个 job 通过，只剩 Windows Go runner 暴露两处同源竞争：一次性绑定读取
inflight 记录时可能遇到 sharing violation，通道事务等待锁时可能遇到短暂
`ACCESS_DENIED`。最终修复没有放宽数据校验或覆盖率门禁：

- 通道配置锁把 Windows sharing/lock violation 与 `ACCESS_DENIED` 识别为竞争，16 个
  并发写事务必须串行进入临界区；
- 一次性绑定按记录使用 tokenized `O_EXCL` 锁，Claim、Commit、Release、Cancel 与
  过期恢复的完整读写/移动事务均持锁；
- 新增 stale owner 恢复、live owner 超时不偷锁、successor owner 不被旧 release
  删除、损坏记录报错和四类状态迁移等待 owner 的行为测试；
- 关键竞争测试连续 50 轮、binding 全包 race、Windows amd64 测试二进制交叉编译与
  完整本地 `npm run ci` 通过；`binding` 覆盖率为 80.1%，总覆盖率仍为 79.4%。

交叉编译和本地重复测试仍不等于 Windows 实机产品验收，因此 M2-604 的 Windows 栏
继续保持待验。[Actions run 30235694088](https://github.com/liming0791/agentbell/actions/runs/30235694088)
的 13 个 job 全绿，其中 Windows Node.js 22 主 job 完整通过 Node、Go 与 durable emit
p95 门禁，M2-603 因而通过；这仍不能替代 M2-604 的 Windows 实机产品验收。

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

## macOS 产品 Hook 的当前候选复验

2026-07-27～28 将当前 `0.3.0-rc.1` 受管候选部署到 stable bridge/版本目录后，在
macOS 26.4 arm64 上复验已安装产品：

- Kimi Code 使用安装后的新 CLI 会话产生 `task.completed`，`diagnose` 的
  `runtimeVerified` 为 true；
- QoderWork CN 0.9.12 与 TRAE CN 3.3.79 的自有 Hook 从工作树开发 Core 迁移到受管
  版本 Core，迁移前均生成带哈希的配置备份；两个真实 GUI 新任务都产生了配置变更后的
  `task.completed` proof，`runtimeVerified` 为 true；
- Codex 0.146 的 `/hooks` 逐项复核确认 AgentBell Stop Hook 精确指向 stable bridge；
  只信任该项后新建全新 CLI 任务，Stop 正常完成并写入 generation 2、Core
  0.3.0-rc.1 的 `task.completed` proof，`adapter diagnose codex` 已返回
  `runtimeVerified=true`；全程未选择 trust all，也未使用 Hook 信任绕过参数；
- 本机 Claude Code 2.0.19 会在 settings 中出现未知 `StopFailure` 时把整个 Hook 对象
  视为零匹配。候选实现现按官方版本阈值协商事件集与命令形态，并通过自动迁移、卸载、
  audit、race 和 Windows 交叉编译测试。真实用户 settings 已在备份后迁移为
  Stop/Notification 兼容集合；最小差分实测进一步确认 2.0.19 在
  `permissions.defaultMode=auto` 时仍把全部 settings Hook 视为零匹配，而移除该字段
  的隔离对照立即恢复 Stop matcher。AgentBell 不改变用户审批策略；随后另一个真实
  Claude Code 任务仍通过 stable bridge 写入了 generation 2、Core 0.3.0-rc.1 的
  `task.completed` proof，`adapter diagnose claude-code` 已返回
  `runtimeVerified=true`。因此兼容 Hook 的真实运行态已验证，但 2.0.19 的特定自动权限
  CLI 路径仍由 `diagnose` 在缺少 proof 时明确提示，不能把该宿主限制静默处理。

QoderWork/TRAE 继续使用各自独立的版本化 Core Hook，而 Codex/Claude/Kimi 使用 stable
bridge；不能把前三项的局部 macOS 证据解释为五个产品、三平台或 IDE Surface 矩阵完成。

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

## Windows stable bridge 无窗口修复（2026-08-01）

Windows 实机完成 setup 后，飞书用户/bot 群聊与手机测试消息链路已通；安装登录服务时
出现一个标题为 `%LOCALAPPDATA%\AgentBell\bin\bridge\v1\agentbell-bridge.exe` 的
常驻黑色控制台窗口。该进程实际在执行 `service-v1`，不退出属于后台服务预期行为，
但可见窗口会被用户误判为 setup 卡死或异常。

根因是 Windows 计划任务直接执行 stable bridge，而 RC5 bridge 是 Console subsystem
产物。Task Scheduler 的 `Hidden` 属性只控制任务是否显示在管理 UI，不控制进程窗口。
Release 构建现仅对 Windows `agentbell-bridge` 增加 Go linker
`-H=windowsgui`，且 Bridge 使用 `CREATE_NO_WINDOW` 拉起后台 Core，避免 Console
subsystem 的 Core 再创建替代窗口；用户直接执行的 Windows Core 仍保持 Console
subsystem，其他平台参数不变。
自动测试覆盖六目标的参数选择；本机按正式 Release 脚本构建 12 个产物，并读取 PE
Header 复验：Windows amd64/arm64 Core 均为 `WindowsCUI (3)`，bridge 均为
`WindowsGUI (2)`。随后在独立临时 managed root 中启动新版 bridge 和已安装 Core，
两者持续运行且 `MainWindowHandle` 均为 `0`；测试配置、队列和日志均隔离，未发送通知，
测试后进程与临时目录已清理。已安装 RC5 不会被工作树修改，仍需下一 Release 升级并
重新安装服务后完成真实登录/重启无窗口复验。

## Windows 运行中 Bridge 升级修复（2026-08-28）

Windows 实机从 RC7 升级 RC9 时复现：计划任务仍运行旧
`agentbell-bridge.exe service-v1`，原子替换虽已把旧文件改名为
`.agentbell-bridge.exe.<pid>.<nonce>.tmp.previous`，但 Windows 拒绝删除仍被进程占用的
旧映像，升级以 `EPERM: operation not permitted, unlink ...tmp.previous` 中断。任务
状态的独立核对同时证明问题不是服务未注册，而是升级器在停旧服务前替换了它正在运行
的稳定 EXE。

RC10 Draft 的自动
[stage](https://github.com/liming0791/agentbell/actions/runs/33157204919) 与 RC9→RC10
[lifecycle](https://github.com/liming0791/agentbell/actions/runs/33157658752) 均通过，但未据此
直接公开。Windows 最终 Draft 实机验收发现旧 active state 声明了 service bridge、文件
却已缺失时，旧 Core 在执行 `service uninstall` 前先做 bridge 校验并退出，因此 RC10
bootstrap 仍无法 quiesce。失败补偿删除目标版本时又先删除 `install.json`，随后因目标
Core EXE 被占用而留下只有 Core 的半安装目录；下一次尝试只检查 Core checksum，错误
复用了缺失元数据的目录。RC10 因此保持 Draft，npm 未发布。

RC11 bootstrap 的最终修复与门禁：

- Windows 下载、校验和 Core smoke 完成后，bootstrap 直接以 `schtasks.exe` 查询、停止
  并删除 `\AgentBell\AgentBell`，不再依赖旧 Core 或 stable bridge 健康；
- 停服后只清理稳定入口目录内名称严格匹配的历史 `.tmp.previous` 文件，再替换 Hook
  bridge 与无窗口 service bridge；短暂文件占用在重命名和删除阶段均有有界重试；
- 激活成功后由新 Core 执行 `service install --json` 并验证 Running；任一步失败时先
  停用可能已启动的新任务，再恢复旧 bridge / active state，并以旧 Core 重新安装服务；
- 缓存版本复用同时校验 Core 与严格 `install.json`。Core 精确匹配 Release、仅元数据因
  中断清理缺失时会重建；Core 缺失则清除精确版本目录后重新 staging，其他冲突拒绝；
- Windows 回归 fixture 覆盖任务存在/不存在/删除失败、运行时写入口即 EPERM、RC9 精确
  残留清理、非匹配文件保留、激活失败补偿，以及半安装和 active 元数据自愈；
- 同机使用 RC11 源码 bootstrap 和 RC10 Draft 原生产物，已从损坏 RC9 成功激活 RC10，
  再从缺失 `install.json` 的 active RC10 完成同版本修复；最终 generation 10、任务
  `Running`、`service status.running=true`、`bridge doctor.healthy=true`，Codex
  `hooks.json` SHA-256 前后相同。本次未修改 Hook 定义，不产生新的信任要求。

## 实机矩阵

| 场景 | macOS | Windows | Linux | WSL / SSH / Container |
| --- | --- | --- | --- | --- |
| 飞书 user/bot 一次性绑定 | 既有 bot 通道直接发送通过；一次性绑定与 user 模式待验 | 待验 | 待验 | Host 绑定后复用，待验 |
| 设置/模板/免打扰/多通道 | 待验 | 待验 | 待验 | Host policy，待验 |
| 安装、升级、自动回滚 | 真实上一 Release 安装、最终 Draft 升级和显式旧版回滚通过；自动失败补偿由故障测试覆盖 | 损坏 RC9→RC10 Draft 与半安装同版本修复通过；RC11 最终资产待发布后复验 | 待验 | shim 独立升级，待验 |
| Codex/Claude/Kimi stable Hook | Codex 0.146、Claude Code 2.0.19、Kimi Code 均由新任务/会话取得 generation 2 `task.completed` proof；Desktop/IDE Surface 矩阵仍待验 | 待验 | 待验 | 按产品运行位置待验 |
| 登录服务重启/断网恢复 | LaunchAgent 升级/回滚重启与两次后台飞书投递通过；断网待验 | 待验 | 待验 | 待验 |
| WSL host-pull，无 listener | 不适用 | 待验 | 不适用 | 待验 |
| SSH strict host-key + tunnel | 可作 Host，待验 | 可作 Host，待验 | 可作 Host，待验 | 待验 |
| Container stdio/HTTPS | 本机 Linux arm64 container stdio host-pull 与隔离 TLS/HTTPS E2E 通过；真实飞书待验 | 可作 Host，待验 | 同 Host 双 container TLS/HTTPS E2E 通过；独立主机待验 | container stdio 与 TLS/HTTPS 本机通过；跨主机待验 |
| 统一卸载及默认保留 | 七 Adapter、LaunchAgent、active runtime 实机卸载通过；配置、95 条 history、证据和非 active 缓存保留 | 自动 fixture 通过，实机待验 | 自动 fixture 通过，实机待验 | credential/peer 默认保留与脱敏自动通过，实机待验 |

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

已完成：

- 创建并发布 `v0.3.0-rc.5`；真实 Draft 资产复验、npm 字节一致性、Trusted Publisher
  和最终安装 smoke 在公开 GitHub prerelease 前成功；
- 用真实上一 Release 和 RC2 Draft 在自动 Linux 隔离环境完成 lifecycle，并用最终
  RC4 Draft 在 macOS 真机完成安装 → upgrade → Hook 字节不变 → fixture 发送 →
  rollback → uninstall；Release/CI 链接均已保存。

仍需：

1. 补齐 macOS 断网恢复，以及 Windows+WSL、Linux、SSH 和 container 的端到端记录；
2. 将本表中的“待验”替换为证据链接后，才可把实施计划状态改为完成。

# CI/CD 与发布

AgentBell M0.5 的“部署”是发布 Go Core、checksum、构建证明、GitHub Release 和 npm
bootstrap，不包含常驻 Web 服务。

## 本地门禁

开发环境使用 Node.js 24、npm 11 和 Go 1.26.5；发布的 npm bootstrap 支持 Node.js 20
或更高版本。

```bash
npm ci
npm run ci
npm run check:docs
npm run perf:emit
npm run perf:m2
npm run smoke:https       # Linux / Ubuntu CI
npm run build:core
npm run release:verify -- v0.3.0-rc.6
```

`perf:emit` 实际通过受管 `active.json` 和 stable `agentbell-bridge hook-v1`
启动当前 Core，门禁是 35 个样本的 p95 `<200ms`，不是绕过 bridge 直接调用 Core。
`perf:m2` 在此基础上另记录 RelayEnvelope 验签、32 路策略 fan-out 和 durable queue
写入基准，并执行 96 个 durable item 的断连、ACK 丢失、崩溃恢复、精确正文重试和
唯一投递语义门禁；纳秒基准用于发现回归，跨机器不设置不可靠的绝对阈值。

`npm run ci` 依次执行：

- ESLint；
- 仓库结构、JSON 和双份 Adapter catalog 一致性检查；
- Node.js 测试，以及 `packages/cli/src` 每个 npm bootstrap 生产模块 80% 行覆盖率门禁；
- Go fmt、vet、测试、75% 总覆盖率，以及 `event`、`queue`、`adapter`、`setup`、
  `binding`、`settings`、`relay`、`bridge`、`installstate`、`policy`、`hookaudit`、
  `pluginverify`、`remoteconfig`、`remote`、`secretstore` 的 80% 覆盖率门禁；
- 两个 npm workspace 的 `npm pack --dry-run`。

`npm run check:docs` 不需要安装依赖，检查仓库必备结构及 README、CLAUDE、TODO 和
`docs/` 中的本地 Markdown 路径；外部 URL 与页内 anchor 不在该轻量门禁范围内。

## 持续集成

CI 按改动状态分层，避免同一批代码在 Draft、Ready PR 和 merge commit 上重复跑完整
矩阵：

- Draft PR 以及 PR 转回 Draft 时只跑 Ubuntu 完整质量门禁，持续提供 lint、覆盖率、
  pack、Linux migration 和 durable emit 反馈；
- PR 标记 Ready、更新 Ready PR、直接 push 到 `main` 或手动触发时跑完整矩阵；
- 已通过 Ready PR 完整矩阵、commit message 以 `Merge pull request #` 开头的标准
  GitHub merge commit 在 `main` 只重跑质量门禁；squash/rebase merge 与直接 push
  仍跑完整矩阵；
- 仅修改 Markdown 时由 `.github/workflows/docs.yml` 执行仓库结构和本地文档链接检查，
  不安装 npm 依赖、不安装 Go，也不占用 Windows/macOS runner；
- 同一分支的新提交会取消仍在运行的旧 workflow。

主 CI workflow 的 job 数量由原来的 13 个调整为：Draft PR 1 个、Ready PR/直接
`main` push 9 个、标准 GitHub merge commit 1 个。代码改动同时包含 Markdown 时还会
并行执行 1 个轻量 docs job；Markdown-only 只执行 docs job。

完整矩阵包括：

- Ubuntu 完整质量门禁；
- Ubuntu Node.js 20/22（Node.js 24 已由完整质量门禁覆盖）；
- Windows 和 macOS Node.js 22；
- Linux migration 和 stable bridge Hook p95 `<200ms` 由质量 job 执行；
- Windows 和 macOS 在既有兼容 job 中执行 Go 测试、固定历史 migration fixture 和
  stable bridge Hook p95 `<200ms`，不再额外启动 migration runner；
- Linux `go test -race ./...`；
- Ubuntu 执行 M2 durable relay stress gate；
- Ubuntu 使用临时 CA、正常 x509 校验和 SPKI pin 执行真实 TLS/HTTPS
  pairing、ACK、断网恢复、去重、metadata-only 与 runtime proof smoke；
- Ubuntu 干净 runner 交叉构建六个 Core 和 stable bridge 目标；Windows stable bridge
  使用 `-H=windowsgui`，并以 `CREATE_NO_WINDOW` 拉起后台 Core；Windows Core 本身保持
  Console subsystem，避免后台登录任务弹出常驻控制台；同一 runner 默认两路
  并行，避免拆 job，同时缩短墙钟时间。

本地需要复现单路构建或调整 runner 内并发时，可设置
`AGENTBELL_BUILD_CONCURRENCY=1..6`；默认值为 `2`。无论并发顺序如何，固定
version/commit/buildTime 的 12 个产物必须逐字节一致。

仓库升级到支持 Rulesets/分支保护的计划后，建议把 Ready PR 的以下 job 设为必需检查：

- `Quality and package checks`
- `ubuntu-latest / Node.js 20`
- `ubuntu-latest / Node.js 22`
- `windows-latest / Node.js 22`
- `macos-latest / Node.js 22`
- `Go race detector`
- `M2 durable relay stress gate`
- `M2 TLS and HTTPS relay smoke`
- `Six-target Core and bridge build`

当前仓库已公开，并启用禁止分支删除和 non-fast-forward 的 active Ruleset；该 Ruleset
尚未把 CI job 设为必需检查，因此合并仍需由维护者确认 Ready PR 的完整矩阵全部通过。
未来启用必需检查时，应使用不会因 path filter 缺失的聚合门禁，不能直接要求
Markdown-only PR 不会创建的 job。Release 的 stage 和 finalize 门禁不使用上述节流
逻辑，发布证据与两段式不可逆边界保持不变。

## Release 流水线

`.github/workflows/release.yml` 使用同一 Tag 上的两个独立动作。推送 Tag 或手动选择
`stage` 时固定执行：

```text
tag/manifest 校验
-> Node/Go 全量门禁
-> 六目标 Core + bridge 可重复构建
-> 最终 Linux amd64 Core 的真实 TLS/HTTPS pairing/ACK/recovery smoke
-> Core 确定性 zip/tar.gz + bridge 原生文件
-> 两个最终 npm tgz
-> 覆盖 Core、bridge 和 npm tgz 的 checksums + Technical Preview manifest
-> 下载上一 Release，执行 upgrade/Hook 字节不变/fixture/rollback/实际 uninstall lifecycle smoke
-> 本地 HTTP bootstrap 下载/校验/执行/清理 smoke
-> GitHub artifact attestation（仓库能力允许时）
-> draft GitHub Release（已公开的同 tag Release 禁止覆盖）
-> 从 draft Release API 下载、校验、安装 Core/bridge，复验插件并安装 smoke 最终 npm tgz
-> 保持 Draft，不发布 npm，不公开 GitHub Release
```

维护者检查 Draft 和 stage Actions 证据后，必须从同一 Tag 手动选择 `finalize`：

```text
确认目标仍是 Draft 且 NPM_PUBLISH_ENABLED=true
-> 从 Draft 重新下载全部最终资产
-> 再次安装 Core/bridge、复验五个插件和两个 npm tgz
-> npm publish
-> 发布完整 GitHub Release
```

Core 与 bridge 版本的 `buildTime` 来自 tag commit 时间，不来自 runner 当前时间。
Core tar 使用固定 mtime/owner 和 `gzip -n`，zip 删除扩展元数据；bridge 当前以原生文件
进入同一 artifact 和 Release。Release manifest 记录 tag 对应 commit、每个产物的
SHA-256、大小和 `signatureStatus`。最终 Linux amd64 Core 在归档、draft Release 和
npm 发布前通过与 PR CI 相同的 `smoke:https`，不会用重新临时构建的 Core 替代待发布
二进制。当前 bootstrap 的 `install-core` 已用受管升级事务
安装 Core、stable bridge 和 `active.json`。Release workflow 会先下载上一正式 RC，
以 M1 真实无 `schemaVersion` 的 `install.json` 作为起点，验证旧版本被纳入
`previous`、升级前后三个稳定 Hook 字节不变、fixture 经 bridge 入队、升级和回滚后的
bridge doctor 均健康，以及实际执行产品卸载和 bootstrap Core 清理，并断言 active
Core、stable bridge、active pointer 和五个 Linux 适用的隔离 Adapter Hook 被删除；
QoderWork/TRAE 作为 Linux 不适用目标必须在统一卸载报告中明确标记 skipped/no-op，
配置和状态数据按默认策略保留。macOS 真实 LaunchAgent 的备份迁移和
stable bridge → 后台服务 → 飞书闭环已通过；Windows/Linux 的真实服务迁移仍由
[M2 实施计划](./m2-execution-plan.md) 的实机矩阵跟踪。
自动 Release lifecycle 固定在 Linux 隔离 runner；macOS LaunchAgent label 和 Windows
计划任务名称是用户全局资源，因此这两个平台只有在一次性测试用户上显式设置
`AGENTBELL_RELEASE_SMOKE_DISPOSABLE_USER=1` 才允许执行实际卸载，普通本机默认拒绝。

五个插件在同一 Release 中生成严格文件 manifest，并使用固定 GitHub
OIDC/repository/tag workflow identity 做 Cosign keyless 签名。workflow 在发布前由
最终 host Core 验证 staging；draft Release 上传后、npm 发布前，再下载每个 tarball、
解包并由已安装 Core 执行 `plugin verify`。任一签名身份、文件集或 hash 不匹配都会
阻止不可逆的 npm 发布和 Release 公开。

两个 npm tgz 在 build job 中只生成一次，并在 release metadata 中记录 SHA-256 和
大小。`stage` 和后续手动 `finalize` 都从 draft Release 重新下载这两个精确 tgz，
校验 checksum/manifest，安装到隔离前缀，并分别执行 CLI help 和 Hook runtime Stop
fixture；`finalize` 复验通过后才允许 `npm publish`。npm registry 不支持两个包的跨包
原子事务：若第一个 publish 成功后第二个
失败，相同 workflow 重跑只有在 registry `dist.integrity` 与最终 tgz 的 SHA-512
完全一致时才跳过已存在版本并继续发布缺失包；同版本不同字节会停止。已公开的 GitHub Release
禁止 `--clobber` 重跑，也不会因重跑失败被改回 draft；需要修复时发布新版本。

M0.5 没有代码签名凭据。workflow 只接受
`AGENTBELL_SIGNATURE_STATUS=technical-preview`；其他值会失败，避免把未签名产物标成
signed。

公开仓库已把 `AGENTBELL_ATTESTATIONS_ENABLED` 设为 `true`，Release workflow 会为
发布产物写入 GitHub Artifact Attestation；若未来迁移到不支持该能力的仓库，只有显式
设为 `false` 才会跳过该步骤，并继续保留 `checksums.txt`、`release-manifest.json` 和
Actions artifact 作为发布证据。

## npm Trusted Publishing

`finalize` job 使用 GitHub OIDC，不把长期 `NPM_TOKEN` 存入仓库。RC5 首发已完成以下
外部配置：

1. npm 账号 `liming0791` 拥有
   `@liming0791/agentbell-cli` 和 `@liming0791/agentbell-hook-runtime`；
2. GitHub Environment 名为 `npm-publish`；
3. 两个 npm 包的 Trusted Publisher 指向
   `liming0791/agentbell`、`release.yml`、`npm-publish`；
4. 两个包已用维护者 2FA 完成一次最小首发，随后立即切换 OIDC。

项目不得使用未验证所有权的品牌 scope。RC4 finalize 发现 `@agentbell` 已属于无关的
第三方 npm Organization，因此 RC5 起包名固定为
`@liming0791/agentbell-cli` 与 `@liming0791/agentbell-hook-runtime`。旧 Release 中
`agentbell-cli-*.tgz` 仅作为历史 bootstrap/lifecycle 输入；RC5 及以后最终 npm 资产名为
`liming0791-agentbell-cli-*.tgz` 和
`liming0791-agentbell-hook-runtime-*.tgz`。改 scope 必须创建新版本和新 Tag，不得改写
既有 Draft 或公开 Release。

预发布版本发布到 npm `next` dist-tag；正式版本使用 `latest`。RC5 是两个新包的首个
版本，registry 当前也把 `latest` 指向 RC5；Technical Preview 安装文档仍固定使用
`@next`，不得把 `latest` 当作 GA 承诺。相同版本已存在时 workflow 会跳过 npm publish；
只允许仍为 draft 的 GitHub Release 重跑和替换资产，公开 Release 必须保持不可变。
仓库变量 `NPM_PUBLISH_ENABLED` 当前为 `true`。变量未开启时，`stage` 仍会生成并验证
Draft，但 `finalize` 会在 npm 或 GitHub 公开发布前失败，Draft 保持不变。

## 创建 RC

```bash
npm run version:set -- 0.3.0-rc.6
npm run ci
npm run release:verify -- v0.3.0-rc.6
git add .
git commit -m "feat: deliver M2 release candidate"
git tag -a v0.3.0-rc.6 -m "AgentBell v0.3.0-rc.6"
git push origin main
git push origin v0.3.0-rc.6
```

Tag push 只完成 `stage`，成功后 Draft 仍不会公开。检查 stage run 与 Draft 资产后，
从该 Tag 手动执行：

```bash
gh workflow run release.yml \
  --ref v0.3.0-rc.6 \
  -f tag=v0.3.0-rc.6 \
  -f action=finalize
```

也可在 GitHub Actions 的 Release workflow 中选择同一个 Tag、`action=finalize`。
`finalize` 失败时 Draft 保持不变；npm 版本不可覆盖，首包已经发布时只允许相同 tgz
补齐第二个包。修复代码后发布新的 prerelease 或 patch 版本，不强制改写 Git tag。
所有下载复验和安装 smoke 都在 Release 仍为 Draft 时完成。已公开 Release 不允许覆盖
资产，也不会被失败重跑改回 Draft。

## `v0.3.0-rc.5` 发布记录

`v0.3.0-rc.5` 已于 2026-07-28 作为
[GitHub prerelease](https://github.com/liming0791/agentbell/releases/tag/v0.3.0-rc.5)
发布，包含 27 个资产；两个 npm 包也已发布到 `next`，registry 中的字节完整性与
Draft tgz 完全一致。PR 的
[跨平台 CI run 30361271121](https://github.com/liming0791/agentbell/actions/runs/30361271121)
13/13 job 全部通过，Tag 的 build job 也完成了全量门禁、六目标 Core/bridge、TLS smoke
和五个 keyless 插件签名。

同一次 Tag run 的 stage、独立 lifecycle 和 finalize job 被 GitHub 因账号账单状态拒绝
启动，并非测试失败：

- [Tag run 30361896587](https://github.com/liming0791/agentbell/actions/runs/30361896587)；
- [lifecycle run 30365393064](https://github.com/liming0791/agentbell/actions/runs/30365393064)；
- [finalize run 30371902184](https://github.com/liming0791/agentbell/actions/runs/30371902184)。

本次采用一次性人工兜底：只使用同一 Tag 成功 build job 上传的精确资产，12 个重建
Core/bridge 的 SHA-256、两个 npm tgz、checksums 和 manifest 均逐字节匹配；27/27
GitHub Draft asset digest 匹配；五个插件的 OIDC repository/workflow identity 复验
通过。随后在隔离 Linux ARM64 环境执行真实上一 Release 安装 → RC5 upgrade →
三个 Hook 字节不变 → 两次 fixture/doctor → rollback → 实际 uninstall，并从已发布
npm CLI 在隔离 macOS 数据根安装最终 Draft Core。人工兜底不得成为常规路径；只允许在
GitHub 拒绝启动 job、已有同 Tag 成功且不可变的 build 资产时使用，并必须保存失败 run
和等价门禁证据，不能把被拒绝的 run 记为绿色。

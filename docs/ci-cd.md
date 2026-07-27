# CI/CD 与发布

AgentBell M0.5 的“部署”是发布 Go Core、checksum、构建证明、GitHub Release 和 npm
bootstrap，不包含常驻 Web 服务。

## 本地门禁

开发环境使用 Node.js 24、npm 11 和 Go 1.26.5；发布的 npm bootstrap 支持 Node.js 20
或更高版本。

```bash
npm ci
npm run ci
npm run perf:emit
npm run perf:m2
npm run smoke:https       # Linux / Ubuntu CI
npm run build:core
npm run release:verify -- v0.3.0-rc.1
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

## 持续集成

`.github/workflows/ci.yml` 在 `main` push、Pull Request 和手动触发时运行：

- Ubuntu 完整质量门禁；
- Ubuntu Node.js 20/22/24；
- Windows 和 macOS Node.js 22；
- 三个平台各自执行 Go 测试和 stable bridge Hook p95 `<200ms` 门禁；
- 三个平台执行固定历史 migration fixture；
- Linux `go test -race ./...`；
- Ubuntu 执行 M2 durable relay stress gate；
- Ubuntu 使用临时 CA、正常 x509 校验和 SPKI pin 执行真实 TLS/HTTPS
  pairing、ACK、断网恢复、去重、metadata-only 与 runtime proof smoke；
- Ubuntu 干净 runner 交叉构建六个 Core 和 stable bridge 目标。

建议把以下 job 设为 `main` 分支必需检查：

- `Quality and package checks`
- `ubuntu-latest / Node.js 20`
- `ubuntu-latest / Node.js 22`
- `ubuntu-latest / Node.js 24`
- `windows-latest / Node.js 22`
- `macos-latest / Node.js 22`
- `Migration fixtures / ubuntu-latest`
- `Migration fixtures / windows-latest`
- `Migration fixtures / macos-latest`
- `Go race detector`
- `M2 durable relay stress gate`
- `M2 TLS and HTTPS relay smoke`
- `Six-target Core and bridge build`

## Release 流水线

`.github/workflows/release.yml` 的顺序固定为：

```text
tag/manifest 校验
-> Node/Go 全量门禁
-> 六目标 Core + bridge 可重复构建
-> 最终 Linux amd64 Core 的真实 TLS/HTTPS pairing/ACK/recovery smoke
-> Core 确定性 zip/tar.gz + bridge 原生文件
-> checksums + Technical Preview manifest
-> 下载上一 Release，执行 upgrade/Hook 字节不变/fixture/rollback/uninstall lifecycle smoke
-> 本地 HTTP bootstrap 下载/校验/执行/清理 smoke
-> GitHub artifact attestation（仓库能力允许时）
-> draft GitHub Release
-> 从 draft Release API 下载、校验、安装 Core/bridge 并复验插件
-> npm pack/publish
-> 上传 npm tgz
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
bridge doctor 均健康，以及产品卸载 dry-run；真实服务迁移和飞书发送闭环仍由
[M2 实施计划](./m2-execution-plan.md) 的实机矩阵跟踪。

五个插件在同一 Release 中生成严格文件 manifest，并使用固定 GitHub
OIDC/repository/tag workflow identity 做 Cosign keyless 签名。workflow 在发布前由
最终 host Core 验证 staging；draft Release 上传后、npm 发布前，再下载每个 tarball、
解包并由已安装 Core 执行 `plugin verify`。任一签名身份、文件集或 hash 不匹配都会
阻止不可逆的 npm 发布和 Release 公开。

M0.5 没有代码签名凭据。workflow 只接受
`AGENTBELL_SIGNATURE_STATUS=technical-preview`；其他值会失败，避免把未签名产物标成
signed。

个人账号下的 GitHub 私有仓库不支持 Artifact Attestation 持久化。仓库变量
`AGENTBELL_ATTESTATIONS_ENABLED=false` 时，workflow 明确跳过该可选步骤，并保留
`checksums.txt`、`release-manifest.json` 和 Actions artifact 作为发布证据；迁移到支持
该能力的仓库后，显式把变量设为 `true`。

## npm Trusted Publishing

发布 job 使用 GitHub OIDC，不把长期 `NPM_TOKEN` 存入仓库。首次发布前需要一次外部
配置：

1. npm 账号拥有 `@agentbell` scope 和两个包名；
2. GitHub Environment 名为 `npm-publish`；
3. 两个 npm 包的 Trusted Publisher 指向
   `liming0791/agentbell`、`release.yml`、`npm-publish`；
4. 若 npm 要求包先存在，维护者先完成一次最小首发，再立即切换 OIDC。

`v0.2.0-rc.3` 等预发布版本发布到 npm `next` dist-tag；正式版本使用 `latest`。相同版本
已存在时 workflow 会跳过 npm publish，因此 GitHub Release 上传可以安全重跑。
完成上述配置后，把仓库变量 `NPM_PUBLISH_ENABLED` 设为 `true`。变量未开启时，Release
仍会发布已验证的 Core 和两个 npm tgz，但不会向 npm registry 发起未经授权的 publish。

## 创建 RC

```bash
npm run version:set -- 0.3.0-rc.1
npm run ci
npm run release:verify -- v0.3.0-rc.1
git add .
git commit -m "feat: deliver M2 release candidate"
git tag -a v0.3.0-rc.1 -m "AgentBell v0.3.0-rc.1"
git push origin main
git push origin v0.3.0-rc.1
```

发布失败时保留 draft Release 供诊断，不对外显示为完整发布。npm 版本不可覆盖；修复后
发布新的 prerelease 或 patch 版本，不强制改写 Git tag。
完整 Release 发布后还会使用当前 job 的短期 GitHub token 走私有仓库 API 安装并执行
Core；该 smoke 失败时，workflow 会把 Release 自动退回 draft。

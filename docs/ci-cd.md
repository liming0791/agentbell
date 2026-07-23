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
npm run build:core
npm run release:verify -- v0.2.0-rc.3
```

`npm run ci` 依次执行：

- ESLint；
- 仓库结构、JSON 和双份 Adapter catalog 一致性检查；
- Node.js 测试与 npm bootstrap 80% 行覆盖率门禁；
- Go fmt、vet、测试、75% 总覆盖率和三个核心包 80% 覆盖率门禁；
- 两个 npm workspace 的 `npm pack --dry-run`。

## 持续集成

`.github/workflows/ci.yml` 在 `main` push、Pull Request 和手动触发时运行：

- Ubuntu 完整质量门禁；
- Ubuntu Node.js 20/22/24；
- Windows 和 macOS Node.js 22；
- 三个平台各自执行 Go 测试和持久 `emit` p95 `<200ms` 门禁；
- Linux `go test -race ./...`；
- Ubuntu 干净 runner 交叉构建六个 Core 目标。

建议把以下 job 设为 `main` 分支必需检查：

- `Quality and package checks`
- `ubuntu-latest / Node.js 20`
- `ubuntu-latest / Node.js 22`
- `ubuntu-latest / Node.js 24`
- `windows-latest / Node.js 22`
- `macos-latest / Node.js 22`
- `Go race detector`
- `Six-target Core build`

## Release 流水线

`.github/workflows/release.yml` 的顺序固定为：

```text
tag/manifest 校验
-> Node/Go 全量门禁
-> 六目标可重复构建
-> 确定性 zip/tar.gz
-> checksums + Technical Preview manifest
-> 本地 HTTP bootstrap 下载/校验/执行/清理 smoke
-> GitHub artifact attestation（仓库能力允许时）
-> draft GitHub Release
-> npm pack/publish
-> 上传 npm tgz
-> 发布完整 GitHub Release
-> 从私有 Release API 下载、校验、安装并执行 Core
```

Core 版本的 `buildTime` 来自 tag commit 时间，不来自 runner 当前时间。tar 使用固定
mtime/owner 和 `gzip -n`，zip 删除扩展元数据。Release manifest 记录 tag 对应 commit、
每个产物的 SHA-256、大小和 `signatureStatus`。

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
npm run version:set -- 0.2.0-rc.3
npm run ci
npm run release:verify -- v0.2.0-rc.3
git add .
git commit -m "feat: deliver M0.5 technical preview"
git tag -a v0.2.0-rc.3 -m "AgentBell v0.2.0-rc.3"
git push origin main
git push origin v0.2.0-rc.3
```

发布失败时保留 draft Release 供诊断，不对外显示为完整发布。npm 版本不可覆盖；修复后
发布新的 prerelease 或 patch 版本，不强制改写 Git tag。
完整 Release 发布后还会使用当前 job 的短期 GitHub token 走私有仓库 API 安装并执行
Core；该 smoke 失败时，workflow 会把 Release 自动退回 draft。

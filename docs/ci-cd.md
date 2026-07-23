# CI/CD 与发布

AgentBell 当前是 npm CLI/运行时包，不包含常驻 Web 服务。因此这里的“部署”是把两个
workspace 发布到 npm，并生成带有 `.tgz` 安装包的 GitHub Release。

## 本地质量门禁

开发环境使用 Node.js 24 和 npm 11；发布包本身兼容 Node.js 20 或更高版本。

```bash
npm ci
npm run ci
```

`npm run ci` 会依次执行 ESLint、仓库结构检查、带覆盖率报告的 Node.js 原生测试，以及
两个 workspace 的 `npm pack --dry-run` 打包检查。

## 持续集成

`.github/workflows/ci.yml` 在推送到 `main`、Pull Request 和手动触发时运行：

- Ubuntu 上执行完整质量门禁；
- Ubuntu 上验证 Node.js 20、22、24；
- Windows 和 macOS 上验证 Node.js 22。

建议在 GitHub 的 `main` 分支规则中要求以下检查通过后才能合并：

- `Quality and package checks`
- `ubuntu-latest / Node.js 20`
- `ubuntu-latest / Node.js 22`
- `ubuntu-latest / Node.js 24`
- `windows-latest / Node.js 22`
- `macos-latest / Node.js 22`

## 首次发布前的一次性配置

1. 在 GitHub 创建远程仓库并推送 `main`。
2. 在 npm 创建或确认 `@agentbell` scope，并确保发布者对
   `@agentbell/cli` 与 `@agentbell/hook-runtime` 有发布权限。
3. 在 GitHub 创建名为 `npm-publish` 的 Environment；可在这里增加人工审批。
4. 在两个 npm 包的 Trusted Publisher 设置中分别选择 GitHub Actions，并填写：
   - GitHub 组织或用户名；
   - 仓库名；
   - workflow 文件名 `release.yml`；
   - Environment 名 `npm-publish`；
   - 允许 `npm publish`。
5. 如果 npm 要求包必须先存在，首次发布用维护者账号完成一次最小版本发布，再立即切换
   到 Trusted Publisher，并撤销不再需要的自动化写令牌。

发布工作流使用 GitHub OIDC，不需要把长期 `NPM_TOKEN` 存进仓库。workflow 中的
`id-token: write` 只用于为单次发布换取短期凭据。

## 创建版本

在干净的 `main` 分支上同步所有 npm 与插件 manifest 版本：

```bash
npm run version:set -- 0.2.0
npm run ci
npm run release:verify -- v0.2.0
git add .
git commit -m "release: v0.2.0"
git tag -a v0.2.0 -m "AgentBell v0.2.0"
git push origin main
git push origin v0.2.0
```

推送 `vX.Y.Z` 标签后，`.github/workflows/release.yml` 会再次执行完整门禁、构建两个
package archive、发布尚未存在的 npm 版本，并创建或更新 GitHub Release。工作流也支持
用已有标签手动重跑，已发布的 npm 包会被跳过。

## 回滚原则

npm 版本不可覆盖。出现问题时应废弃有问题的版本、修复后发布新的补丁版本，并视情况
移动 `latest` dist-tag。Git 标签和已经发布的 Release 不应强制改写。

import { spawnSync } from "node:child_process";

function commandExists(command) {
  const locator = process.platform === "win32" ? "where.exe" : "which";
  const result = spawnSync(locator, [command], {
    encoding: "utf8",
    shell: false,
    windowsHide: true
  });

  return result.status === 0;
}

export function detectEnvironment() {
  return {
    node: {
      installed: true,
      version: process.version
    },
    npm: {
      installed: commandExists(process.platform === "win32" ? "npm.cmd" : "npm")
    },
    larkCli: {
      installed: commandExists("lark-cli")
    },
    agents: {
      codex: commandExists("codex"),
      claude: commandExists("claude"),
      kimi: commandExists("kimi")
    },
    platform: process.platform,
    architecture: process.arch
  };
}


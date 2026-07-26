import { readFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

// The platform directory is the canonical config location, matching the Go
// Core (core/internal/paths): %APPDATA%\AgentBell on Windows,
// ~/Library/Application Support/AgentBell on macOS,
// ${XDG_CONFIG_HOME:-~/.config}/agentbell on Linux.
function defaultConfigPath() {
  const home = os.homedir();

  switch (process.platform) {
    case "win32": {
      const appData = process.env.APPDATA ||
        path.join(home, "AppData", "Roaming");
      return path.join(appData, "AgentBell", "config.json");
    }
    case "darwin":
      return path.join(
        home, "Library", "Application Support", "AgentBell", "config.json"
      );
    default: {
      const configHome = process.env.XDG_CONFIG_HOME ||
        path.join(home, ".config");
      return path.join(configHome, "agentbell", "config.json");
    }
  }
}

export function resolveConfigPath() {
  return process.env.AGENTBELL_CONFIG
    ? path.resolve(process.env.AGENTBELL_CONFIG)
    : defaultConfigPath();
}

export async function loadConfig() {
  const configPath = resolveConfigPath();

  try {
    return {
      path: configPath,
      value: JSON.parse(await readFile(configPath, "utf8"))
    };
  } catch (error) {
    if (error && typeof error === "object" && error.code === "ENOENT") {
      return {
        path: configPath,
        value: null
      };
    }
    throw error;
  }
}


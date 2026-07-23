import { readFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";

export function resolveConfigPath() {
  return process.env.AGENTBELL_CONFIG
    ? path.resolve(process.env.AGENTBELL_CONFIG)
    : path.join(os.homedir(), ".agentbell", "config.json");
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


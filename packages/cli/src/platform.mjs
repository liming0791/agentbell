import os from "node:os";
import path from "node:path";

const targets = new Map([
  ["win32:x64", { os: "windows", arch: "amd64", extension: ".exe" }],
  ["win32:arm64", { os: "windows", arch: "arm64", extension: ".exe" }],
  ["darwin:x64", { os: "darwin", arch: "amd64", extension: "" }],
  ["darwin:arm64", { os: "darwin", arch: "arm64", extension: "" }],
  ["linux:x64", { os: "linux", arch: "amd64", extension: "" }],
  ["linux:arm64", { os: "linux", arch: "arm64", extension: "" }]
]);

export function resolveTarget(platform = process.platform, architecture = process.arch) {
  const target = targets.get(`${platform}:${architecture}`);
  if (!target) {
    throw new Error(
      `AgentBell Core does not support ${platform}/${architecture}.`
    );
  }
  const id = `${target.os}-${target.arch}`;
  return {
    ...target,
    id,
    fileName: `agentbell-${id}${target.extension}`,
    bridgeFileName: `agentbell-bridge-${id}${target.extension}`,
    ...(platform === "win32"
      ? { serviceBridgeFileName: `agentbell-service-${id}${target.extension}` }
      : {})
  };
}

export function resolveDataRoot({
  platform = process.platform,
  home = os.homedir(),
  env = process.env
} = {}) {
  if (env.AGENTBELL_DATA_DIR) {
    return path.resolve(env.AGENTBELL_DATA_DIR);
  }
  if (platform === "win32") {
    return path.join(
      env.LOCALAPPDATA || path.join(home, "AppData", "Local"),
      "AgentBell"
    );
  }
  if (platform === "darwin") {
    return path.join(home, "Library", "Application Support", "AgentBell");
  }
  return path.join(
    env.XDG_DATA_HOME || path.join(home, ".local", "share"),
    "agentbell"
  );
}

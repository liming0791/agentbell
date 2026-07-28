import { createHash } from "node:crypto";
import {
  chmod,
  lstat,
  mkdir,
  readFile,
  rename,
  rm,
  stat,
  writeFile
} from "node:fs/promises";
import path from "node:path";
import { spawn } from "node:child_process";
import { URL } from "node:url";

import { resolveDataRoot, resolveTarget } from "./platform.mjs";
import {
  activeStatePath,
  stableBridgePath
} from "./upgrade.mjs";
import { fetchGitHubReleaseMetadata } from "./github-release.mjs";

const defaultReleaseBase =
  "https://github.com/liming0791/agentbell/releases";
const githubAPIOrigin = "https://api.github.com";

function checksum(value) {
  return createHash("sha256").update(value).digest("hex");
}

function parseChecksums(value) {
  const result = new Map();
  for (const line of value.split(/\r?\n/)) {
    const match = /^([a-fA-F0-9]{64})\s+[* ]?(.+)$/.exec(line.trim());
    if (match) {
      result.set(match[2], match[1].toLowerCase());
    }
  }
  return result;
}

async function fetchRequired(fetchImpl, url, token, accept) {
  const headers = {
    "user-agent": "agentbell-bootstrap"
  };
  if (accept) {
    headers.accept = accept;
  }
  if (token) {
    headers.authorization = `Bearer ${token}`;
  }
  const response = await fetchImpl(url, {
    headers,
    redirect: "follow"
  });
  if (!response.ok) {
    throw new Error(`Download failed (${response.status}) for ${url}`);
  }
  return response;
}

function githubReleaseAPI(releaseBase) {
  try {
    const parsed = new URL(releaseBase);
    const match = /^\/([^/]+)\/([^/]+)\/releases\/?$/.exec(parsed.pathname);
    if (parsed.hostname !== "github.com" || !match) {
      return null;
    }
    const owner = encodeURIComponent(match[1]);
    const repository = encodeURIComponent(match[2]);
    return `${githubAPIOrigin}/repos/${owner}/${repository}`;
  } catch {
    return null;
  }
}

async function resolveReleaseAssets({
  fetchImpl,
  releaseBase,
  version,
  targetFileName,
  token
}) {
  const repositoryAPI = token ? githubReleaseAPI(releaseBase) : null;
  if (!repositoryAPI) {
    const releaseURL = `${releaseBase.replace(/\/$/, "")}/download/v${version}`;
    return {
      checksumsURL: `${releaseURL}/checksums.txt`,
      binaryURL: `${releaseURL}/${targetFileName}`
    };
  }

  const metadata = await fetchGitHubReleaseMetadata({
    fetchImpl,
    repositoryAPI,
    tagName: `v${version}`,
    token,
  });
  if (!Array.isArray(metadata.assets)) {
    throw new Error(`GitHub release v${version} returned no asset list.`);
  }

  const assetURL = (name) => {
    const asset = metadata.assets.find((candidate) => candidate?.name === name);
    if (!asset || typeof asset.url !== "string") {
      throw new Error(`GitHub release v${version} does not contain ${name}.`);
    }
    return asset.url;
  };

  return {
    checksumsURL: assetURL("checksums.txt"),
    binaryURL: assetURL(targetFileName)
  };
}

export function coreInstallPath({
  version,
  platform = process.platform,
  architecture = process.arch,
  dataRoot
}) {
  const target = resolveTarget(platform, architecture);
  const root = dataRoot || resolveDataRoot({ platform });
  return path.join(root, "bin", version, `agentbell${target.extension}`);
}

export async function installCore({
  version,
  releaseBase = process.env.AGENTBELL_RELEASE_BASE_URL || defaultReleaseBase,
  fetchImpl = globalThis.fetch,
  platform = process.platform,
  architecture = process.arch,
  dataRoot,
  token = process.env.AGENTBELL_GITHUB_TOKEN || process.env.GH_TOKEN
}) {
  if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error(`Invalid AgentBell Core version: ${version}`);
  }
  if (typeof fetchImpl !== "function") {
    throw new Error("This Node.js runtime does not provide fetch.");
  }

  const target = resolveTarget(platform, architecture);
  const installPath = coreInstallPath({
    version,
    platform,
    architecture,
    dataRoot
  });
  const installDirectory = path.dirname(installPath);
  const releaseAssets = await resolveReleaseAssets({
    fetchImpl,
    releaseBase,
    version,
    targetFileName: target.fileName,
    token
  });
  const checksumsResponse = await fetchRequired(
    fetchImpl,
    releaseAssets.checksumsURL,
    token,
    "application/octet-stream"
  );
  const checksums = parseChecksums(await checksumsResponse.text());
  const expected = checksums.get(target.fileName);
  if (!expected) {
    throw new Error(`checksums.txt does not contain ${target.fileName}.`);
  }

  try {
    const existing = await readFile(installPath);
    if (checksum(existing) === expected) {
      return {
        path: installPath,
        version,
        target: target.id,
        checksum: expected,
        reused: true
      };
    }
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }

  const binaryResponse = await fetchRequired(
    fetchImpl,
    releaseAssets.binaryURL,
    token,
    "application/octet-stream"
  );
  const binary = Buffer.from(await binaryResponse.arrayBuffer());
  const actual = checksum(binary);
  if (actual !== expected) {
    throw new Error(
      `SHA-256 mismatch for ${target.fileName}: expected ${expected}, got ${actual}.`
    );
  }

  await mkdir(installDirectory, { recursive: true, mode: 0o700 });
  const temporaryPath = `${installPath}.${process.pid}.${Date.now()}.tmp`;
  const backupPath = `${installPath}.${process.pid}.${Date.now()}.previous`;
  await writeFile(temporaryPath, binary, { mode: 0o700 });
  try {
    await chmod(temporaryPath, 0o700);
    try {
      await rename(temporaryPath, installPath);
    } catch (error) {
      if (!["EACCES", "EEXIST", "ENOTEMPTY", "EPERM"].includes(error?.code)) {
        throw error;
      }
      await rename(installPath, backupPath);
      try {
        await rename(temporaryPath, installPath);
      } catch (replacementError) {
        await rename(backupPath, installPath);
        throw replacementError;
      }
      await rm(backupPath, { force: true });
    }
  } finally {
    await rm(temporaryPath, { force: true });
  }

  const metadata = {
    version,
    target: target.id,
    fileName: target.fileName,
    checksum: expected,
    installedAt: new Date().toISOString(),
    signatureStatus: "technical-preview"
  };
  await writeFile(
    path.join(installDirectory, "install.json"),
    `${JSON.stringify(metadata, null, 2)}\n`,
    { mode: 0o600 }
  );

  return {
    path: installPath,
    version,
    target: target.id,
    checksum: expected,
    reused: false
  };
}

export async function uninstallCore({
  version,
  platform = process.platform,
  architecture = process.arch,
  dataRoot
}) {
  if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error(`Invalid AgentBell Core version: ${version}`);
  }
  const root = path.resolve(dataRoot || resolveDataRoot({ platform }));
  const installPath = coreInstallPath({
    version,
    platform,
    architecture,
    dataRoot: root
  });
  const installDirectory = path.dirname(installPath);
  const versionsRoot = path.join(root, "bin");
  if (path.dirname(installDirectory) !== versionsRoot) {
    throw new Error("Refusing to remove an AgentBell Core path outside the managed versions root.");
  }
  for (const [candidate, label] of [
    [root, "AgentBell data root"],
    [versionsRoot, "AgentBell versions root"],
    [installDirectory, "managed Core version directory"]
  ]) {
    try {
      if ((await lstat(candidate)).isSymbolicLink()) {
        throw new Error(`${label} must not be a symbolic link.`);
      }
    } catch (error) {
      if (error?.code !== "ENOENT") {
        throw error;
      }
    }
  }
  const activePath = activeStatePath(root);
  let removeActiveRuntime = false;
  try {
    const active = JSON.parse(await readFile(activePath, "utf8"));
    if (!active || active.schemaVersion !== 1 ||
        typeof active.activeVersion !== "string") {
      throw new Error("AgentBell active state is invalid.");
    }
    removeActiveRuntime = active.activeVersion === version;
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
  let removed = false;
  try {
    const info = await stat(installDirectory);
    removed = info.isDirectory();
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
  await rm(installDirectory, { recursive: true, force: true });
  if (removeActiveRuntime) {
    await rm(activePath, { force: true });
    await rm(stableBridgePath({ dataRoot: root, platform }), { force: true });
  }
  return {
    path: installPath,
    version,
    removed
  };
}

export function runCore(executable, args, {
  stdin = "inherit",
  stdout = "inherit",
  stderr = "inherit"
} = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {
      shell: false,
      windowsHide: true,
      stdio: [stdin, stdout, stderr]
    });
    child.once("error", reject);
    child.once("close", (code, signal) => {
      if (signal) {
        reject(new Error(`AgentBell Core terminated by ${signal}.`));
      } else {
        resolve(code ?? 1);
      }
    });
  });
}

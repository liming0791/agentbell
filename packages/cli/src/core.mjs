import { createHash } from "node:crypto";
import {
  chmod,
  mkdir,
  readFile,
  rename,
  rm,
  writeFile
} from "node:fs/promises";
import path from "node:path";
import { spawn } from "node:child_process";

import { resolveDataRoot, resolveTarget } from "./platform.mjs";

const defaultReleaseBase =
  "https://github.com/liming0791/agentbell/releases";

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

async function fetchRequired(fetchImpl, url, token) {
  const headers = {
    "user-agent": "agentbell-bootstrap"
  };
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
  const releaseURL = `${releaseBase.replace(/\/$/, "")}/download/v${version}`;
  const checksumsResponse = await fetchRequired(
    fetchImpl,
    `${releaseURL}/checksums.txt`,
    token
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
    `${releaseURL}/${target.fileName}`,
    token
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

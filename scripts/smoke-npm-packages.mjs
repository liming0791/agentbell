import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import {
  mkdtemp,
  readFile,
  rm
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function argument(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : undefined;
}

function parseChecksums(value) {
  const result = new Map();
  for (const line of value.split(/\r?\n/)) {
    if (line === "") {
      continue;
    }
    const match = /^([a-f0-9]{64}) {2}([^/\\]+)$/.exec(line);
    if (!match || result.has(match[2])) {
      throw new Error("Release checksums contain an invalid or duplicate entry.");
    }
    result.set(match[2], match[1]);
  }
  return result;
}

async function run(executable, args, { input } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {
      shell: false,
      windowsHide: true,
      stdio: ["pipe", "pipe", "pipe"]
    });
    const stdout = [];
    const stderr = [];
    child.stdout.on("data", (value) => stdout.push(value));
    child.stderr.on("data", (value) => stderr.push(value));
    child.once("error", reject);
    child.once("close", (code, signal) => {
      if (signal || code !== 0) {
        reject(new Error(
          `npm package smoke command failed (${signal || code}): ` +
          Buffer.concat(stderr).toString("utf8").slice(0, 4096)
        ));
        return;
      }
      resolve(Buffer.concat(stdout));
    });
    child.stdin.end(input);
  });
}

export async function verifyNpmArchiveMetadata({
  cliArchive,
  hookRuntimeArchive,
  checksumsPath,
  manifestPath,
  version
}) {
  const resolvedCLI = path.resolve(cliArchive);
  const resolvedHookRuntime = path.resolve(hookRuntimeArchive);
  if (resolvedCLI === resolvedHookRuntime) {
    throw new Error("CLI and Hook runtime archives must be distinct files.");
  }
  const expectedNames = new Map([
    [resolvedCLI, `liming0791-agentbell-cli-${version}.tgz`],
    [
      resolvedHookRuntime,
      `liming0791-agentbell-hook-runtime-${version}.tgz`
    ]
  ]);
  const checksums = parseChecksums(await readFile(checksumsPath, "utf8"));
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  if (manifest?.schemaVersion !== 1 ||
      manifest.version !== version ||
      manifest.signatureStatus !== "technical-preview" ||
      !Array.isArray(manifest.artifacts)) {
    throw new Error("Release manifest is incompatible with npm package smoke.");
  }

  for (const [archive, expectedName] of expectedNames) {
    if (path.basename(archive) !== expectedName) {
      throw new Error(`Unexpected npm archive name ${path.basename(archive)}.`);
    }
    const bytes = await readFile(archive);
    const digest = sha256(bytes);
    if (checksums.get(expectedName) !== digest) {
      throw new Error(`Release checksum mismatch for ${expectedName}.`);
    }
    const entries = manifest.artifacts.filter(
      (candidate) => candidate?.fileName === expectedName
    );
    if (entries.length !== 1 ||
        entries[0].sha256 !== digest ||
        entries[0].size !== bytes.length) {
      throw new Error(`Release manifest mismatch for ${expectedName}.`);
    }
  }
  return { archivesVerified: expectedNames.size };
}

export async function smokeNpmPackages({
  cliArchive,
  hookRuntimeArchive,
  checksumsPath,
  manifestPath,
  version
}) {
  const verified = await verifyNpmArchiveMetadata({
    cliArchive,
    hookRuntimeArchive,
    checksumsPath,
    manifestPath,
    version
  });
  const installRoot = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-npm-release-smoke-")
  );
  try {
    const npm = process.platform === "win32" ? "npm.cmd" : "npm";
    await run(npm, [
      "install",
      "--ignore-scripts",
      "--no-audit",
      "--no-fund",
      "--no-package-lock",
      "--prefix",
      installRoot,
      path.resolve(cliArchive),
      path.resolve(hookRuntimeArchive)
    ]);
    const modules = path.join(installRoot, "node_modules", "@liming0791");
    const cliRoot = path.join(modules, "agentbell-cli");
    const hookRoot = path.join(modules, "agentbell-hook-runtime");
    for (const packageRoot of [cliRoot, hookRoot]) {
      const manifest = JSON.parse(
        await readFile(path.join(packageRoot, "package.json"), "utf8")
      );
      if (manifest.version !== version) {
        throw new Error(
          `${manifest.name || "npm package"} installed version ${manifest.version}; ` +
          `expected ${version}.`
        );
      }
    }

    const help = await run(process.execPath, [
      path.join(cliRoot, "bin", "agentbell.mjs"),
      "help"
    ]);
    if (!help.toString("utf8").includes("Usage:")) {
      throw new Error("Packed AgentBell CLI did not print its help contract.");
    }
    const hookOutput = await run(
      process.execPath,
      [
        path.join(hookRoot, "bin", "agentbell-hook.mjs"),
        "--source",
        "codex",
        "--dry-run"
      ],
      {
        input: Buffer.from(JSON.stringify({
          hook_event_name: "Stop",
          cwd: path.join(installRoot, "project"),
          session_id: "release-smoke-session",
          turn_id: "release-smoke-turn"
        }))
      }
    );
    const hookResult = JSON.parse(hookOutput.toString("utf8"));
    if (hookResult?.notification?.source !== "codex" ||
        hookResult.notification.event !== "Stop" ||
        hookResult.notification.status !== "completed") {
      throw new Error("Packed Hook runtime did not normalize the fixture.");
    }
    return {
      ...verified,
      packagesInstalled: 2,
      cliSmoke: true,
      hookRuntimeSmoke: true
    };
  } finally {
    await rm(installRoot, { recursive: true, force: true });
  }
}

async function main() {
  const cliArchive = argument("--cli");
  const hookRuntimeArchive = argument("--hook-runtime");
  const checksumsPath = argument("--checksums");
  const manifestPath = argument("--manifest");
  const version = argument("--version");
  if (!cliArchive || !hookRuntimeArchive || !checksumsPath ||
      !manifestPath || !version) {
    throw new Error(
      "usage: smoke-npm-packages --cli <tgz> --hook-runtime <tgz> " +
      "--checksums <path> --manifest <path> --version <version>"
    );
  }
  const report = await smokeNpmPackages({
    cliArchive,
    hookRuntimeArchive,
    checksumsPath,
    manifestPath,
    version
  });
  process.stdout.write(`NPM_PACKAGE_SMOKE_PASS ${JSON.stringify(report)}\n`);
}

const invokedPath = process.argv[1] && path.resolve(process.argv[1]);
if (invokedPath === fileURLToPath(import.meta.url)) {
  await main();
}

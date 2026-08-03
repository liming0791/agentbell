import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmod,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  writeFile
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { resolveTarget } from "../packages/cli/src/platform.mjs";
import {
  rollback,
  resolveActiveCore,
  stableBridgePath,
  upgrade
} from "../packages/cli/src/upgrade.mjs";
import { uninstallCore } from "../packages/cli/src/core.mjs";
import { run as runBootstrap } from "../packages/cli/src/index.mjs";

const schemaVersion = 1;

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function assertSafeRoot(value) {
  const resolved = path.resolve(value);
  if (resolved === path.parse(resolved).root) {
    throw new Error("Release lifecycle data root cannot be a filesystem root.");
  }
  return resolved;
}

function executableName(target) {
  return `agentbell${target.extension}`;
}

async function runExecutable(executable, args, { input, env } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {
      env,
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
          `Release lifecycle executable failed (${signal || code}): ` +
          Buffer.concat(stderr).toString("utf8").slice(0, 4096)
        ));
        return;
      }
      resolve(Buffer.concat(stdout));
    });
    child.stdin.end(input);
  });
}

async function seedPreviousInstall({
  dataRoot,
  previousVersion,
  previousCore,
  target
}) {
  const directory = path.join(dataRoot, "bin", previousVersion);
  const corePath = path.join(directory, executableName(target));
  const checksum = sha256(previousCore);
  await mkdir(directory, { recursive: true, mode: 0o700 });
  await writeFile(corePath, previousCore, { mode: 0o700 });
  await chmod(corePath, 0o700);
  await writeFile(
    path.join(directory, "install.json"),
    `${JSON.stringify({
      version: previousVersion,
      target: target.id,
      fileName: target.fileName,
      checksum,
      installedAt: new Date(0).toISOString(),
      signatureStatus: "technical-preview"
    }, null, 2)}\n`,
    { mode: 0o600 }
  );
}

async function assertPreviousInstall({
  dataRoot,
  previousVersion,
  previousCore,
  target
}) {
  const directory = path.join(dataRoot, "bin", previousVersion);
  const corePath = path.join(directory, executableName(target));
  const installedCore = await readFile(corePath);
  if (!installedCore.equals(previousCore)) {
    throw new Error(
      "Preinstalled previous Core does not match the previous Release asset."
    );
  }
  const metadata = JSON.parse(
    await readFile(path.join(directory, "install.json"), "utf8")
  );
  if (
    metadata.version !== previousVersion ||
    metadata.target !== target.id ||
    metadata.fileName !== target.fileName ||
    metadata.checksum !== sha256(previousCore) ||
    metadata.signatureStatus !== "technical-preview"
  ) {
    throw new Error(
      "Preinstalled previous Core metadata does not match the previous Release."
    );
  }
}

async function seedHookFiles(dataRoot, bridgePath) {
  const directory = path.join(dataRoot, "release-smoke-hooks");
  await mkdir(directory, { recursive: true, mode: 0o700 });
  const files = [
    ["codex.json", { event: "Stop", command: bridgePath }],
    ["claude.json", { event: "Stop", command: bridgePath }],
    ["kimi.json", { event: "AgentStop", command: bridgePath }]
  ];
  const snapshots = new Map();
  for (const [name, value] of files) {
    const filePath = path.join(directory, name);
    const raw = Buffer.from(`${JSON.stringify(value, null, 2)}\n`);
    await writeFile(filePath, raw, { mode: 0o600 });
    snapshots.set(filePath, raw);
  }
  return snapshots;
}

async function assertHooksUnchanged(snapshots) {
  for (const [filePath, expected] of snapshots) {
    const actual = await readFile(filePath);
    if (!actual.equals(expected)) {
      throw new Error("Upgrade or rollback changed a stable Hook file.");
    }
  }
}

async function queueItemCount(stateDir) {
  let count = 0;
  for (const state of ["pending", "inflight", "history", "dead"]) {
    try {
      const entries = await readdir(path.join(stateDir, "queue", state));
      count += entries.filter((entry) => entry.endsWith(".json")).length;
    } catch (error) {
      if (error?.code !== "ENOENT") {
        throw error;
      }
    }
  }
  return count;
}

async function defaultSmokeCore({ path: corePath, version }) {
  const raw = await runExecutable(corePath, ["version", "--json"]);
  const value = JSON.parse(raw.toString("utf8"));
  if (value.version !== version) {
    throw new Error(
      `Core smoke returned ${value.version}; expected ${version}.`
    );
  }
}

export function releaseLifecycleHookArgs() {
  return [
    "hook-v1",
    "--adapter",
    "codex",
    "--surface",
    "cli",
    "--runtime",
    "host",
    "--stdin",
    "--fail-open"
  ];
}

async function defaultExerciseBridge({
  bridgePath,
  dataRoot,
  stateDir,
  configDir,
  homeDir,
  phase
}) {
  const before = await queueItemCount(stateDir);
  const input = Buffer.from(JSON.stringify({
    hook_event_name: "Stop",
    cwd: path.join(homeDir, "agentbell-release-smoke"),
    session_id: `release-smoke-${phase}`,
    turn_id: `release-smoke-${phase}`
  }));
  await runExecutable(
    bridgePath,
    releaseLifecycleHookArgs(),
    {
      input,
      env: {
        ...process.env,
        HOME: homeDir,
        AGENTBELL_DATA_DIR: dataRoot,
        AGENTBELL_CONFIG: path.join(configDir, "config.json"),
        AGENTBELL_STATE_DIR: stateDir
      }
    }
  );
  const after = await queueItemCount(stateDir);
  if (after <= before) {
    throw new Error(`Stable bridge did not enqueue after ${phase}.`);
  }
}

async function defaultInspectBridge({
  corePath,
  dataRoot,
  stateDir,
  configDir,
  homeDir,
  phase
}) {
  const raw = await runExecutable(corePath, ["bridge", "doctor", "--json"], {
    env: {
      ...process.env,
      HOME: homeDir,
      AGENTBELL_DATA_DIR: dataRoot,
      AGENTBELL_CONFIG: path.join(configDir, "config.json"),
      AGENTBELL_STATE_DIR: stateDir
    }
  });
  const report = JSON.parse(raw.toString("utf8"));
  if (report.healthy !== true || report.mode !== "active") {
    throw new Error(`Bridge doctor was unhealthy after ${phase}.`);
  }
}

function releaseSmokeEnvironment({
  dataRoot,
  stateDir,
  configDir,
  homeDir
}) {
  return {
    ...process.env,
    HOME: homeDir,
    AGENTBELL_DATA_DIR: dataRoot,
    AGENTBELL_CONFIG: path.join(configDir, "config.json"),
    AGENTBELL_STATE_DIR: stateDir,
    XDG_CONFIG_HOME: path.join(homeDir, ".config"),
    CODEX_HOME: path.join(homeDir, ".codex"),
    CLAUDE_CONFIG_DIR: path.join(homeDir, ".claude"),
    KIMI_CODE_HOME: path.join(homeDir, ".kimi-code"),
    OPENCODE_CONFIG_DIR: path.join(homeDir, ".config", "opencode"),
    QODER_CONFIG_DIR: path.join(homeDir, ".qoder"),
    QODERWORK_CONFIG_DIR: path.join(homeDir, ".qoderwork"),
    TRAE_CONFIG_DIR: path.join(homeDir, ".trae")
  };
}

export function assertActualUninstallIsolation({
  platform = process.platform,
  disposableUser = process.env.AGENTBELL_RELEASE_SMOKE_DISPOSABLE_USER === "1"
} = {}) {
  if (platform !== "linux" && !disposableUser) {
    throw new Error(
      "Actual release uninstall on macOS/Windows requires a disposable user " +
      "and AGENTBELL_RELEASE_SMOKE_DISPOSABLE_USER=1 because service labels " +
      "are user-global."
    );
  }
}

async function pathMissing(value) {
  try {
    await readFile(value);
    return false;
  } catch (error) {
    if (error?.code === "ENOENT") {
      return true;
    }
    throw error;
  }
}

async function defaultUninstallProduct({
  corePath,
  dataRoot,
  stateDir,
  configDir,
  homeDir,
  activeVersion,
  configSentinel,
  stateSentinel
}) {
  assertActualUninstallIsolation();
  const environment = releaseSmokeEnvironment({
    dataRoot,
    stateDir,
    configDir,
    homeDir
  });
  const allAdapterIDs = [
    "codex",
    "claude-code",
    "kimi-code",
    "opencode",
    "qoder",
    "qoder-work",
    "trae"
  ];
  const installAdapterIDs = process.platform === "linux"
    ? allAdapterIDs.filter(
        (adapterID) => !["qoder-work", "trae"].includes(adapterID)
      )
    : allAdapterIDs;
  const skippedAdapterIDs = allAdapterIDs.filter(
    (adapterID) => !installAdapterIDs.includes(adapterID)
  );
  for (const adapterID of installAdapterIDs) {
    await runExecutable(
      corePath,
      ["adapter", "install", adapterID, "--json"],
      { env: environment }
    );
  }
  let productReport;
  let cleanup;
  await runBootstrap(["uninstall", "--json"], {
    resolveActiveCore: () => resolveActiveCore({ dataRoot }),
    coreInstallPath: () => corePath,
    runCore: async (executable, args) => {
      if (path.resolve(executable) !== path.resolve(corePath)) {
        throw new Error(
          "Release lifecycle bootstrap did not select the current candidate Core after rollback."
        );
      }
      const output = await runExecutable(executable, args, {
        env: environment
      });
      productReport = JSON.parse(output.toString("utf8"));
      return 0;
    },
    uninstallCore: async ({ version }) => {
      if (version !== activeVersion) {
        throw new Error(
          "Release lifecycle bootstrap did not clean the rolled-back active runtime."
        );
      }
      cleanup = await uninstallCore({ version, dataRoot });
      return cleanup;
    }
  });
  if (productReport.dryRun !== false) {
    throw new Error("Release lifecycle product uninstall was not applied.");
  }
  const managedHooksRemoved = installAdapterIDs.every((adapterID) =>
    productReport.adapters?.some(
      (result) => result?.adapter === adapterID && result.changed === true
    )
  );
  const platformAdaptersSkipped = skippedAdapterIDs.every((adapterID) =>
    productReport.adapters?.some(
      (result) =>
        result?.adapter === adapterID &&
        result.changed === false &&
        result.message?.includes("skipped") &&
        result.message.includes(`not supported on ${process.platform}`)
    )
  );
  if (!cleanup) {
    throw new Error("Release lifecycle bootstrap did not clean the active Core.");
  }
  const activeRemoved = await pathMissing(
    path.join(dataRoot, "bin", "active.json")
  );
  const bridgeRemoved = await pathMissing(stableBridgePath({ dataRoot }));
  const runtimeRemoved = await pathMissing(cleanup.path);
  const configPreserved = (await readFile(configSentinel, "utf8")) ===
    "preserve config\n";
  const statePreserved = (await readFile(stateSentinel, "utf8")) ===
    "preserve state\n";
  if (!cleanup.removed || !runtimeRemoved || !activeRemoved || !bridgeRemoved ||
      !managedHooksRemoved || !platformAdaptersSkipped ||
      !configPreserved || !statePreserved) {
    throw new Error("Release lifecycle uninstall invariants were not satisfied.");
  }
  return {
    actual: true,
    managedRuntimeRemoved: runtimeRemoved,
    stableBridgeRemoved: bridgeRemoved,
    activeStateRemoved: activeRemoved,
    managedHooksRemoved: installAdapterIDs.length,
    platformAdaptersSkipped: skippedAdapterIDs.length,
    configPreserved,
    statePreserved
  };
}

export async function runReleaseLifecycleSmoke({
  dataRoot,
  previousVersion,
  currentVersion,
  previousCore,
  currentBundle,
  previousInstall = "seed",
  smokeCore = defaultSmokeCore,
  restartService,
  exerciseBridge = defaultExerciseBridge,
  inspectBridge = defaultInspectBridge,
  uninstallProduct = defaultUninstallProduct
}) {
  if (previousVersion === currentVersion) {
    throw new Error("Previous and current versions must differ.");
  }
  if (!Buffer.isBuffer(previousCore) || previousCore.length === 0) {
    throw new Error("Previous Core bytes are required.");
  }
  dataRoot = assertSafeRoot(dataRoot);
  const target = resolveTarget();
  const configDir = path.join(dataRoot, "config");
  const stateDir = path.join(dataRoot, "state");
  const homeDir = path.join(dataRoot, "home");
  await mkdir(configDir, { recursive: true, mode: 0o700 });
  await mkdir(stateDir, { recursive: true, mode: 0o700 });
  await mkdir(homeDir, { recursive: true, mode: 0o700 });
  const configSentinel = path.join(configDir, "release-smoke-preserved.txt");
  const stateSentinel = path.join(stateDir, "release-smoke-preserved.txt");
  await writeFile(configSentinel, "preserve config\n", { mode: 0o600 });
  await writeFile(stateSentinel, "preserve state\n", { mode: 0o600 });
  if (previousInstall === "seed") {
    await seedPreviousInstall({
      dataRoot,
      previousVersion,
      previousCore,
      target
    });
  } else if (previousInstall === "verify") {
    await assertPreviousInstall({
      dataRoot,
      previousVersion,
      previousCore,
      target
    });
  } else {
    throw new Error(`Unsupported previous install mode: ${previousInstall}`);
  }
  const bridgePath = stableBridgePath({ dataRoot });
  const hookSnapshots = await seedHookFiles(dataRoot, bridgePath);
  const restartCalls = [];
  const onRestart = restartService || (async ({ active }) => {
    restartCalls.push(active.activeVersion);
  });
  const trackRestart = async (request) => {
    if (restartService) {
      restartCalls.push(request.active.activeVersion);
    }
    await onRestart(request);
  };

  const upgraded = await upgrade({
    toVersion: currentVersion,
    dataRoot,
    configDir,
    stateDir,
    downloadBundle: async () => currentBundle,
    smokeCore,
    restartService: trackRestart
  });
  await assertHooksUnchanged(hookSnapshots);
  if (!(await readFile(bridgePath)).equals(currentBundle.bridge)) {
    throw new Error("Upgrade did not activate the final stable bridge.");
  }
  await exerciseBridge({
    phase: "upgraded",
    bridgePath,
    dataRoot,
    stateDir,
    configDir,
    homeDir
  });
  const currentCorePath = path.join(
    dataRoot,
    "bin",
    currentVersion,
    executableName(target)
  );
  await inspectBridge({
    phase: "upgraded",
    corePath: currentCorePath,
    dataRoot,
    stateDir,
    configDir,
    homeDir
  });

  const rolledBack = await rollback({
    toVersion: previousVersion,
    dataRoot,
    configDir,
    stateDir,
    smokeCore,
    restartService: trackRestart
  });
  await assertHooksUnchanged(hookSnapshots);
  if (!(await readFile(bridgePath)).equals(currentBundle.bridge)) {
    throw new Error("Rollback rewrote the stable bridge.");
  }
  await exerciseBridge({
    phase: "rolled-back",
    bridgePath,
    dataRoot,
    stateDir,
    configDir,
    homeDir
  });
  await inspectBridge({
    phase: "rolled-back",
    corePath: currentCorePath,
    dataRoot,
    stateDir,
    configDir,
    homeDir
  });
  const uninstall = await uninstallProduct({
    corePath: currentCorePath,
    dataRoot,
    stateDir,
    configDir,
    homeDir,
    activeVersion: previousVersion,
    configSentinel,
    stateSentinel
  });
  return {
    schemaVersion,
    previousVersion,
    currentVersion,
    upgradedGeneration: upgraded.generation,
    rolledBackGeneration: rolledBack.generation,
    hookFilesChecked: hookSnapshots.size,
    bridgeExercises: 2,
    bridgeDoctors: 2,
    serviceRestarts: restartCalls.length,
    uninstall
  };
}

function argument(name, fallback) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : fallback;
}

async function main() {
  const directory = path.resolve(argument("--directory", "artifacts/core"));
  const currentVersion = argument("--version");
  const previousVersion = argument("--previous-version");
  const previousCorePath = path.resolve(argument("--previous-core"));
  const previousInstall = process.argv.includes("--preinstalled-previous")
    ? "verify"
    : "seed";
  const requestedDataRoot = argument("--data-root");
  if (previousInstall === "verify" && !requestedDataRoot) {
    throw new Error(
      "--preinstalled-previous requires the explicit previous install --data-root."
    );
  }
  const target = resolveTarget();
  const core = await readFile(path.join(directory, target.fileName));
  const bridge = await readFile(path.join(directory, target.bridgeFileName));
  const serviceBridge = target.serviceBridgeFileName
    ? await readFile(path.join(directory, target.serviceBridgeFileName))
    : null;
  const manifest = JSON.parse(
    await readFile(path.join(directory, "release-manifest.json"), "utf8")
  );
  const dataRoot = requestedDataRoot
    ? assertSafeRoot(requestedDataRoot)
    : await mkdtemp(path.join(os.tmpdir(), "agentbell-m2-release-smoke-"));
  try {
    const report = await runReleaseLifecycleSmoke({
      dataRoot,
      previousVersion,
      currentVersion,
      previousCore: await readFile(previousCorePath),
      previousInstall,
      currentBundle: {
        core,
        bridge,
        ...(serviceBridge ? { serviceBridge } : {}),
        coreChecksum: sha256(core),
        bridgeChecksum: sha256(bridge),
        ...(serviceBridge
          ? { serviceBridgeChecksum: sha256(serviceBridge) }
          : {}),
        signatureStatus: manifest.signatureStatus,
        manifest
      }
    });
    process.stdout.write(`${JSON.stringify(report)}\n`);
  } finally {
    if (!requestedDataRoot) {
      await rm(dataRoot, { recursive: true, force: true });
    }
  }
}

const invokedPath = process.argv[1] && path.resolve(process.argv[1]);
if (invokedPath === fileURLToPath(import.meta.url)) {
  await main();
}

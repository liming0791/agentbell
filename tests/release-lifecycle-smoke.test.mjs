import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import {
  mkdir,
  mkdtemp,
  readFile,
  rm,
  writeFile
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  assertActualUninstallIsolation,
  releaseLifecycleHookArgs,
  runReleaseLifecycleSmoke
} from "../scripts/release-lifecycle-smoke.mjs";
import {
  activeStatePath,
  stableBridgePath
} from "../packages/cli/src/upgrade.mjs";
import { resolveTarget } from "../packages/cli/src/platform.mjs";

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

test("release lifecycle smoke uses the production hook-v1 argument contract", () => {
  assert.deepEqual(releaseLifecycleHookArgs(), [
    "hook-v1",
    "--adapter",
    "codex",
    "--surface",
    "cli",
    "--runtime",
    "host",
    "--stdin",
    "--fail-open"
  ]);
});

test("actual release uninstall requires isolation from user-global services", () => {
  assert.doesNotThrow(() => assertActualUninstallIsolation({
    platform: "linux",
    disposableUser: false
  }));
  for (const platform of ["darwin", "win32"]) {
    assert.throws(
      () => assertActualUninstallIsolation({
        platform,
        disposableUser: false
      }),
      /disposable user/i
    );
    assert.doesNotThrow(() => assertActualUninstallIsolation({
      platform,
      disposableUser: true
    }));
  }
});

test("release lifecycle smoke upgrades, preserves hooks and rolls back", async (context) => {
  const dataRoot = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-release-lifecycle-test-")
  );
  context.after(() => rm(dataRoot, { recursive: true, force: true }));
  const target = resolveTarget();
  const previousCore = Buffer.from("previous-core");
  const currentCore = Buffer.from("current-core");
  const currentBridge = Buffer.from("current-bridge");
  const currentServiceBridge = Buffer.from("current-service-bridge");
  const restarts = [];
  const phases = [];
  const doctorPhases = [];
  const uninstallCalls = [];

  const report = await runReleaseLifecycleSmoke({
    dataRoot,
    previousVersion: "0.2.0-rc.3",
    currentVersion: "0.3.0-rc.1",
    previousCore,
    currentBundle: {
      core: currentCore,
      bridge: currentBridge,
      ...(target.serviceBridgeFileName
        ? { serviceBridge: currentServiceBridge }
        : {}),
      coreChecksum: sha256(currentCore),
      bridgeChecksum: sha256(currentBridge),
      ...(target.serviceBridgeFileName
        ? { serviceBridgeChecksum: sha256(currentServiceBridge) }
        : {}),
      signatureStatus: "technical-preview",
      manifest: {
        schemaVersion: 1,
        version: "0.3.0-rc.1",
        signatureStatus: "technical-preview"
      }
    },
    smokeCore: async ({ version }) => {
      assert.ok(["0.2.0-rc.3", "0.3.0-rc.1"].includes(version));
    },
    restartService: async ({ active }) => {
      restarts.push(active.activeVersion);
    },
    exerciseBridge: async ({ phase }) => {
      phases.push(phase);
    },
    inspectBridge: async ({ phase }) => {
      doctorPhases.push(phase);
    },
    uninstallProduct: async ({ corePath, activeVersion }) => {
      uninstallCalls.push(corePath);
      assert.equal(activeVersion, "0.2.0-rc.3");
      return {
        actual: true,
        managedRuntimeRemoved: true,
        stableBridgeRemoved: true,
        activeStateRemoved: true,
        managedHooksRemoved: 5,
        platformAdaptersSkipped: 2,
        configPreserved: true,
        statePreserved: true
      };
    }
  });

  assert.deepEqual(restarts, ["0.3.0-rc.1", "0.2.0-rc.3"]);
  assert.deepEqual(phases, ["upgraded", "rolled-back"]);
  assert.deepEqual(doctorPhases, ["upgraded", "rolled-back"]);
  assert.equal(uninstallCalls.length, 1);
  assert.deepEqual(report, {
    schemaVersion: 1,
    previousVersion: "0.2.0-rc.3",
    currentVersion: "0.3.0-rc.1",
    upgradedGeneration: 2,
    rolledBackGeneration: 3,
    hookFilesChecked: 3,
    bridgeExercises: 2,
    bridgeDoctors: 2,
    serviceRestarts: 2,
    uninstall: {
      actual: true,
      managedRuntimeRemoved: true,
      stableBridgeRemoved: true,
      activeStateRemoved: true,
      managedHooksRemoved: 5,
      platformAdaptersSkipped: 2,
      configPreserved: true,
      statePreserved: true
    }
  });
  const active = JSON.parse(
    await readFile(activeStatePath(dataRoot), "utf8")
  );
  assert.equal(active.activeVersion, "0.2.0-rc.3");
  assert.deepEqual(
    await readFile(stableBridgePath({ dataRoot })),
    currentBridge
  );
  assert.equal(target.id, active.target);
});

test("release lifecycle smoke rejects a non-migration", async () => {
  await assert.rejects(
    runReleaseLifecycleSmoke({
      dataRoot: path.join(os.tmpdir(), "agentbell-release-same-version"),
      previousVersion: "0.3.0",
      currentVersion: "0.3.0",
      previousCore: Buffer.from("old"),
      currentBundle: {}
    }),
    /must differ/i
  );
});

test("release lifecycle smoke verifies a real previous bootstrap install", async (context) => {
  const dataRoot = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-release-preinstalled-test-")
  );
  context.after(() => rm(dataRoot, { recursive: true, force: true }));
  const target = resolveTarget();
  const previousVersion = "0.2.0-rc.3";
  const currentVersion = "0.3.0-rc.2";
  const previousCore = Buffer.from("previous-release-core");
  const currentCore = Buffer.from("current-draft-core");
  const currentBridge = Buffer.from("current-draft-bridge");
  const currentServiceBridge = Buffer.from("current-draft-service-bridge");
  const previousDirectory = path.join(dataRoot, "bin", previousVersion);
  const previousPath = path.join(
    previousDirectory,
    process.platform === "win32" ? "agentbell.exe" : "agentbell"
  );
  await mkdir(previousDirectory, { recursive: true });
  await writeFile(previousPath, previousCore);
  await writeFile(
    path.join(previousDirectory, "install.json"),
    `${JSON.stringify({
      version: previousVersion,
      target: target.id,
      fileName: target.fileName,
      checksum: sha256(previousCore),
      installedAt: new Date(0).toISOString(),
      signatureStatus: "technical-preview"
    })}\n`
  );

  await runReleaseLifecycleSmoke({
    dataRoot,
    previousVersion,
    currentVersion,
    previousCore,
    previousInstall: "verify",
    currentBundle: {
      core: currentCore,
      bridge: currentBridge,
      ...(target.serviceBridgeFileName
        ? { serviceBridge: currentServiceBridge }
        : {}),
      coreChecksum: sha256(currentCore),
      bridgeChecksum: sha256(currentBridge),
      ...(target.serviceBridgeFileName
        ? { serviceBridgeChecksum: sha256(currentServiceBridge) }
        : {}),
      signatureStatus: "technical-preview",
      manifest: {
        schemaVersion: 1,
        version: currentVersion,
        signatureStatus: "technical-preview"
      }
    },
    smokeCore: async () => {},
    restartService: async () => {},
    exerciseBridge: async () => {},
    inspectBridge: async () => {},
    uninstallProduct: async () => ({ actual: true })
  });
});

test("release lifecycle smoke rejects a mismatched previous Release install", async (context) => {
  const dataRoot = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-release-mismatch-test-")
  );
  context.after(() => rm(dataRoot, { recursive: true, force: true }));
  const target = resolveTarget();
  const previousVersion = "0.2.0-rc.3";
  const previousDirectory = path.join(dataRoot, "bin", previousVersion);
  await mkdir(previousDirectory, { recursive: true });
  await writeFile(
    path.join(
      previousDirectory,
      process.platform === "win32" ? "agentbell.exe" : "agentbell"
    ),
    "not-the-release-core"
  );
  await writeFile(
    path.join(previousDirectory, "install.json"),
    `${JSON.stringify({
      version: previousVersion,
      target: target.id,
      fileName: target.fileName,
      checksum: sha256(Buffer.from("not-the-release-core")),
      installedAt: new Date(0).toISOString(),
      signatureStatus: "technical-preview"
    })}\n`
  );

  await assert.rejects(
    runReleaseLifecycleSmoke({
      dataRoot,
      previousVersion,
      currentVersion: "0.3.0-rc.2",
      previousCore: Buffer.from("real-previous-release-core"),
      previousInstall: "verify",
      currentBundle: {}
    }),
    /does not match the previous Release asset/
  );
});

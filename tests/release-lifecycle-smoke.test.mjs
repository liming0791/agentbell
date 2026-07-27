import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
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

test("release lifecycle smoke upgrades, preserves hooks and rolls back", async (context) => {
  const dataRoot = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-release-lifecycle-test-")
  );
  context.after(() => rm(dataRoot, { recursive: true, force: true }));
  const target = resolveTarget();
  const previousCore = Buffer.from("previous-core");
  const currentCore = Buffer.from("current-core");
  const currentBridge = Buffer.from("current-bridge");
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
      coreChecksum: sha256(currentCore),
      bridgeChecksum: sha256(currentBridge),
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
    uninstallDryRun: async ({ corePath }) => {
      uninstallCalls.push(corePath);
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
    uninstallDryRun: true
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

import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { detectEnvironment } from "../packages/cli/src/detect.mjs";
import {
  booleanFlagEnabled,
  run
} from "../packages/cli/src/index.mjs";

const require = createRequire(import.meta.url);
const { version } = require("../packages/cli/package.json");

async function captureLogs(callback) {
  const originalLog = console.log;
  const logs = [];
  console.log = (...values) => logs.push(values.join(" "));

  try {
    await callback();
  } finally {
    console.log = originalLog;
  }

  return logs;
}

test("detects the current runtime", () => {
  const environment = detectEnvironment();

  assert.equal(environment.node.installed, true);
  assert.equal(environment.node.version, process.version);
  assert.equal(environment.platform, process.platform);
  assert.equal(environment.architecture, process.arch);
  assert.equal(typeof environment.agents.codex, "boolean");
});

test("prints help", async () => {
  const logs = await captureLogs(() => run(["help"]));

  assert.match(logs.join("\n"), /Usage:/);
});

test("prints doctor output and a setup plan", async () => {
  const doctorLogs = await captureLogs(() => run(["bootstrap-doctor"]));
  const planLogs = await captureLogs(() => run(["setup", "--plan"]));

  assert.equal(JSON.parse(doctorLogs[0]).node.installed, true);
  assert.ok(Array.isArray(JSON.parse(planLogs[0]).actions));
});

test("rejects unsupported commands", async () => {
  await assert.rejects(() => run(["unknown"]), /Unsupported command/);
});

test("parses boolean forwarding flags without unsafe uninstall ambiguity", () => {
  assert.equal(booleanFlagEnabled(["--dry-run"], "--dry-run"), true);
  assert.equal(booleanFlagEnabled(["--dry-run=true"], "--dry-run"), true);
  assert.equal(booleanFlagEnabled(["--dry-run=1"], "--dry-run"), true);
  assert.equal(booleanFlagEnabled(["--dry-run=false"], "--dry-run"), false);
  assert.equal(booleanFlagEnabled(["--json=true"], "--json"), true);
  assert.equal(booleanFlagEnabled([], "--dry-run"), false);
});

test("removes the managed Core only after successful product uninstall", async () => {
  const forwarded = [];
  const removed = [];
  const dependencies = {
    runCore: async (_executable, args) => {
      forwarded.push(args);
      return 0;
    },
    uninstallCore: async ({ version: selectedVersion }) => {
      removed.push(selectedVersion);
      return { removed: true };
    }
  };
  await run(["uninstall", "--json"], dependencies);
  assert.deepEqual(forwarded, [["uninstall", "--json"]]);
  assert.deepEqual(removed, [version]);

  forwarded.length = 0;
  removed.length = 0;
  await run(["uninstall", "--dry-run=true", "--json"], dependencies);
  assert.deepEqual(forwarded, [["uninstall", "--dry-run=true", "--json"]]);
  assert.deepEqual(removed, []);

  await assert.rejects(
    run(["uninstall"], {
      ...dependencies,
      runCore: async () => 9
    }),
    /exited with code 9/
  );
  assert.deepEqual(removed, []);
});

test("routes native commands to a checksum-installed Core", async (context) => {
  const temporaryRoot = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-cli-test-")
  );
  const previous = process.env.AGENTBELL_DATA_DIR;
  process.env.AGENTBELL_DATA_DIR = temporaryRoot;
  context.after(async () => {
    if (previous === undefined) {
      delete process.env.AGENTBELL_DATA_DIR;
    } else {
      process.env.AGENTBELL_DATA_DIR = previous;
    }
    await rm(temporaryRoot, { recursive: true, force: true });
  });

  const logs = await captureLogs(() => run(["core-path"]));
  assert.ok(logs[0].includes(version));
  const notInstalled = new RegExp(
    `Core ${version.replaceAll(".", "\\.")} is not installed`
  );
  await assert.rejects(run(["version"]), notInstalled);
  await assert.rejects(run(["setup"]), notInstalled);
  await assert.rejects(run(["test"]), notInstalled);
});

test("keeps setup --plan local without an installed Core", async (context) => {
  const temporaryRoot = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-cli-test-")
  );
  const previous = process.env.AGENTBELL_DATA_DIR;
  process.env.AGENTBELL_DATA_DIR = temporaryRoot;
  context.after(async () => {
    if (previous === undefined) {
      delete process.env.AGENTBELL_DATA_DIR;
    } else {
      process.env.AGENTBELL_DATA_DIR = previous;
    }
    await rm(temporaryRoot, { recursive: true, force: true });
  });

  const logs = await captureLogs(() => run(["setup", "--plan"]));

  assert.ok(Array.isArray(JSON.parse(logs[0]).actions));
});

import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import { detectEnvironment } from "../packages/cli/src/detect.mjs";
import { run } from "../packages/cli/src/index.mjs";

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
  assert.match(logs[0], /0\.2\.0-rc\.1/);
  await assert.rejects(
    run(["version"]),
    /Core 0\.2\.0-rc\.1 is not installed/
  );
});

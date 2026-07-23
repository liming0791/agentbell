import assert from "node:assert/strict";
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
  const doctorLogs = await captureLogs(() => run(["doctor"]));
  const planLogs = await captureLogs(() => run(["setup", "--plan"]));

  assert.equal(JSON.parse(doctorLogs[0]).node.installed, true);
  assert.ok(Array.isArray(JSON.parse(planLogs[0]).actions));
});

test("rejects unsupported commands", async () => {
  await assert.rejects(() => run(["unknown"]), /Unsupported command/);
});

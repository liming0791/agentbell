import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import {
  loadConfig,
  resolveConfigPath
} from "../packages/hook-runtime/src/config.mjs";
import { handleHook } from "../packages/hook-runtime/src/index.mjs";

test("resolves the platform config directory by default", (context) => {
  const previousConfigPath = process.env.AGENTBELL_CONFIG;

  context.after(() => {
    if (previousConfigPath === undefined) {
      delete process.env.AGENTBELL_CONFIG;
    } else {
      process.env.AGENTBELL_CONFIG = previousConfigPath;
    }
  });

  delete process.env.AGENTBELL_CONFIG;

  const home = os.homedir();
  let expected;
  if (process.platform === "win32") {
    const appData = process.env.APPDATA ||
      path.join(home, "AppData", "Roaming");
    expected = path.join(appData, "AgentBell", "config.json");
  } else if (process.platform === "darwin") {
    expected = path.join(
      home, "Library", "Application Support", "AgentBell", "config.json"
    );
  } else {
    const configHome = process.env.XDG_CONFIG_HOME ||
      path.join(home, ".config");
    expected = path.join(configHome, "agentbell", "config.json");
  }

  assert.equal(resolveConfigPath(), expected);
});

test("loads configuration from AGENTBELL_CONFIG", async (context) => {
  const temporaryDirectory = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-test-")
  );
  const configPath = path.join(temporaryDirectory, "config.json");
  const previousConfigPath = process.env.AGENTBELL_CONFIG;

  context.after(async () => {
    if (previousConfigPath === undefined) {
      delete process.env.AGENTBELL_CONFIG;
    } else {
      process.env.AGENTBELL_CONFIG = previousConfigPath;
    }
    await rm(temporaryDirectory, { recursive: true, force: true });
  });

  process.env.AGENTBELL_CONFIG = configPath;
  await writeFile(configPath, '{"defaultChannel":"test"}', "utf8");

  assert.equal(resolveConfigPath(), configPath);
  assert.deepEqual((await loadConfig()).value, {
    defaultChannel: "test"
  });
});

test("returns config-missing when no configuration exists", async (context) => {
  const temporaryDirectory = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-test-")
  );
  const configPath = path.join(temporaryDirectory, "missing.json");
  const previousConfigPath = process.env.AGENTBELL_CONFIG;

  context.after(async () => {
    if (previousConfigPath === undefined) {
      delete process.env.AGENTBELL_CONFIG;
    } else {
      process.env.AGENTBELL_CONFIG = previousConfigPath;
    }
    await rm(temporaryDirectory, { recursive: true, force: true });
  });

  process.env.AGENTBELL_CONFIG = configPath;
  const result = await handleHook({
    source: "codex",
    rawInput: '{"hook_event_name":"Stop"}',
    dryRun: true
  });

  assert.equal(result.sent, false);
  assert.equal(result.reason, "config-missing");
  assert.equal(result.configPath, configPath);
});

test("renders a dry-run notification with the selected channel", async (context) => {
  const temporaryDirectory = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-test-")
  );
  const configPath = path.join(temporaryDirectory, "config.json");
  const previousConfigPath = process.env.AGENTBELL_CONFIG;

  context.after(async () => {
    if (previousConfigPath === undefined) {
      delete process.env.AGENTBELL_CONFIG;
    } else {
      process.env.AGENTBELL_CONFIG = previousConfigPath;
    }
    await rm(temporaryDirectory, { recursive: true, force: true });
  });

  process.env.AGENTBELL_CONFIG = configPath;
  await writeFile(
    configPath,
    JSON.stringify({
      defaultChannel: "team",
      notifications: {
        events: ["Stop"],
        includeSummary: true
      },
      channels: [{
        id: "team",
        chatId: "oc_test",
        as: "bot"
      }]
    }),
    "utf8"
  );

  const result = await handleHook({
    source: "codex",
    rawInput: JSON.stringify({
      hook_event_name: "Stop",
      last_assistant_message: "Done"
    }),
    dryRun: true
  });

  assert.equal(result.sent, false);
  assert.equal(result.dryRun, true);
  assert.equal(result.channel, "team");
  assert.match(result.text, /摘要：Done/);
});

test("skips disabled events and missing channels", async (context) => {
  const temporaryDirectory = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-test-")
  );
  const configPath = path.join(temporaryDirectory, "config.json");
  const previousConfigPath = process.env.AGENTBELL_CONFIG;

  context.after(async () => {
    if (previousConfigPath === undefined) {
      delete process.env.AGENTBELL_CONFIG;
    } else {
      process.env.AGENTBELL_CONFIG = previousConfigPath;
    }
    await rm(temporaryDirectory, { recursive: true, force: true });
  });

  process.env.AGENTBELL_CONFIG = configPath;
  await writeFile(
    configPath,
    JSON.stringify({
      defaultChannel: "missing",
      notifications: {
        events: ["PermissionRequest"]
      },
      channels: []
    }),
    "utf8"
  );

  const disabled = await handleHook({
    source: "codex",
    rawInput: '{"hook_event_name":"Stop"}',
    dryRun: true
  });
  assert.equal(disabled.reason, "event-disabled");

  const missingChannel = await handleHook({
    source: "codex",
    rawInput: '{"hook_event_name":"PermissionRequest"}',
    dryRun: true
  });
  assert.equal(missingChannel.reason, "channel-missing");
});

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
  const help = logs.join("\n");

  assert.match(help, /Usage:/);
  for (const command of [
    "settings",
    "policy",
    "bind",
    "bridge",
    "hook",
    "plugin",
    "relay",
    "remote",
    "adapter",
    "uninstall"
  ]) {
    assert.match(help, new RegExp(`\\b${command}\\b`));
  }
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

test("routes upgrade, rollback and versions through the bootstrap transaction layer", async () => {
  const calls = [];
  const dependencies = {
    upgrade: async (request) => {
      calls.push(["upgrade", request]);
      return { activeVersion: request.toVersion, generation: 2 };
    },
    rollback: async (request) => {
      calls.push(["rollback", request]);
      return { activeVersion: request.toVersion, generation: 3 };
    },
    listVersions: async () => {
      calls.push(["versions"]);
      return { activeVersion: "0.3.0", installed: [] };
    }
  };
  const logs = await captureLogs(async () => {
    await run(
      [
        "upgrade",
        "--from", "0.2.0-rc.3",
        "--to", "0.3.0",
        "--channel", "next",
        "--dry-run",
        "--json"
      ],
      dependencies
    );
    await run(["rollback", "--to", "0.2.9", "--dry-run", "--json"], dependencies);
    await run(["versions", "--json"], dependencies);
  });
  assert.deepEqual(calls, [
    ["upgrade", {
      toVersion: "0.3.0",
      fromVersion: "0.2.0-rc.3",
      channel: "next",
      dryRun: true
    }],
    ["rollback", { toVersion: "0.2.9", dryRun: true }],
    ["versions"]
  ]);
  assert.equal(JSON.parse(logs[0]).activeVersion, "0.3.0");
  assert.equal(JSON.parse(logs[1]).activeVersion, "0.2.9");
  assert.equal(JSON.parse(logs[2]).activeVersion, "0.3.0");
});

test("install-core initializes the managed active Core and stable bridge", async () => {
  const calls = [];
  const logs = await captureLogs(() => run(
    ["install-core", "--version", "0.3.0"],
    {
      resolveActiveCore: async () => null,
      upgrade: async (request) => {
        calls.push(request);
        await request.restartService();
        return {
          activeVersion: request.toVersion,
          generation: 1,
          corePath: "/managed/bin/0.3.0/agentbell",
          bridgePath: "/managed/bin/bridge/v1/agentbell-bridge"
        };
      }
    }
  ));
  assert.equal(calls.length, 1);
  assert.equal(calls[0].toVersion, "0.3.0");
  assert.equal(calls[0].channel, "stable");
  assert.equal(calls[0].dryRun, false);
  assert.equal(typeof calls[0].restartService, "function");
  assert.equal(JSON.parse(logs[0]).generation, 1);

  await assert.rejects(
    run(
      ["install-core", "--version", "0.3.0"],
      {
        resolveActiveCore: async () => ({
          version: "0.2.9",
          path: "/managed/bin/0.2.9/agentbell"
        }),
        upgrade: async () => {
          throw new Error("must not run");
        }
      }
    ),
    /already active.*agentbell upgrade --to/i
  );
});

test("forwards native commands to the active Core when available", async () => {
  const calls = [];
  await run(["settings", "show", "--json"], {
    resolveActiveCore: async () => ({
      path: "/managed/bin/0.3.0/agentbell",
      version: "0.3.0"
    }),
    runCore: async (executable, args) => {
      calls.push({ executable, args });
      return 0;
    }
  });
  assert.deepEqual(calls, [{
    executable: "/managed/bin/0.3.0/agentbell",
    args: ["settings", "show", "--json"]
  }]);
});

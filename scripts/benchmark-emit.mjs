import { spawnSync } from "node:child_process";
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
import { performance } from "node:perf_hooks";

import { findGo, root } from "./go-tool.mjs";
import { resolveTarget } from "../packages/cli/src/platform.mjs";

const samples = 35;
const warmups = 5;
const limitMilliseconds = 200;
const coreVersion = JSON.parse(
  await readFile(path.join(root, "package.json"), "utf8")
).version;
const temporaryRoot = await mkdtemp(
  path.join(os.tmpdir(), "agentbell-emit-benchmark-")
);
const stateDir = path.join(temporaryRoot, "Agent Bell 性能 🚀", "state");
const dataRoot = path.join(temporaryRoot, "Agent Bell 性能 🚀", "data");
const target = resolveTarget();
const executable = path.join(
  dataRoot,
  "bin",
  coreVersion,
  process.platform === "win32" ? "agentbell.exe" : "agentbell"
);
const bridge = path.join(
  dataRoot,
  "bin",
  "bridge",
  "v1",
  process.platform === "win32"
    ? "agentbell-bridge.exe"
    : "agentbell-bridge"
);

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    ...options,
    encoding: "utf8",
    windowsHide: true
  });
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(
      `${path.basename(command)} failed (${result.status}): ${result.stderr || ""}`
    );
  }
  return result;
}

try {
  await mkdir(stateDir, { recursive: true });
  await mkdir(path.dirname(executable), { recursive: true });
  await mkdir(path.dirname(bridge), { recursive: true });
  const goExecutable = await findGo();
  run(
    goExecutable,
    [
      "build",
      "-trimpath",
      "-ldflags",
      `-X github.com/liming0791/agentbell/core/internal/version.Version=${coreVersion}`,
      "-o",
      executable,
      "./cmd/agentbell"
    ],
    {
      cwd: path.join(root, "core"),
      env: { ...process.env, CGO_ENABLED: "0" },
      stdio: ["ignore", "ignore", "pipe"]
    }
  );
  run(
    goExecutable,
    [
      "build",
      "-trimpath",
      ...(process.platform === "win32"
        ? ["-ldflags", "-H=windowsgui"]
        : []),
      "-o",
      bridge,
      "./cmd/agentbell-bridge"
    ],
    {
      cwd: path.join(root, "core"),
      env: { ...process.env, CGO_ENABLED: "0" },
      stdio: ["ignore", "ignore", "pipe"]
    }
  );
  const coreChecksum = createHash("sha256")
    .update(await readFile(executable))
    .digest("hex");
  const bridgeChecksum = createHash("sha256")
    .update(await readFile(bridge))
    .digest("hex");
  await writeFile(
    path.join(dataRoot, "bin", "active.json"),
    `${JSON.stringify({
      schemaVersion: 1,
      generation: 1,
      activeVersion: coreVersion,
      target: target.id,
      checksum: coreChecksum,
      bridgeChecksum,
      transactionId: "benchmark"
    }, null, 2)}\n`
  );

  const durations = [];
  for (let index = 0; index < samples + warmups; index += 1) {
    const input = JSON.stringify({
      hook_event_name: "Stop",
      cwd: path.join(temporaryRoot, "项目 🚀", "agentbell"),
      session_id: `benchmark-session-${index}`,
      turn_id: `benchmark-turn-${index}`
    });
    const started = performance.now();
    run(
      bridge,
      [
        "hook-v1",
        "--adapter",
        "codex",
        "--surface",
        "cli"
      ],
      {
        input,
        env: {
          ...process.env,
          AGENTBELL_STATE_DIR: stateDir
        },
        stdio: ["pipe", "ignore", "pipe"]
      }
    );
    const elapsed = performance.now() - started;
    if (index >= warmups) {
      durations.push(elapsed);
    }
  }

  durations.sort((left, right) => left - right);
  const p95 = durations[Math.ceil(durations.length * 0.95) - 1];
  const maximum = durations.at(-1);
  console.log(
    `agentbell bridge hook ${process.platform}/${process.arch}: ` +
    `p95=${p95.toFixed(2)}ms max=${maximum.toFixed(2)}ms n=${durations.length}`
  );
  if (p95 >= limitMilliseconds) {
    throw new Error(
      `bridge hook p95 ${p95.toFixed(2)}ms exceeds the ` +
      `${limitMilliseconds}ms gate.`
    );
  }
} finally {
  await rm(temporaryRoot, { recursive: true, force: true });
}

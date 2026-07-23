import { spawnSync } from "node:child_process";
import {
  mkdir,
  mkdtemp,
  rm
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { performance } from "node:perf_hooks";

import { findGo, root } from "./go-tool.mjs";

const samples = 35;
const warmups = 5;
const limitMilliseconds = 200;
const temporaryRoot = await mkdtemp(
  path.join(os.tmpdir(), "agentbell-emit-benchmark-")
);
const stateDir = path.join(temporaryRoot, "Agent Bell 性能 🚀", "state");
const executable = path.join(
  temporaryRoot,
  process.platform === "win32" ? "agentbell.exe" : "agentbell"
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
  const goExecutable = await findGo();
  run(
    goExecutable,
    ["build", "-trimpath", "-o", executable, "./cmd/agentbell"],
    {
      cwd: path.join(root, "core"),
      env: { ...process.env, CGO_ENABLED: "0" },
      stdio: ["ignore", "ignore", "pipe"]
    }
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
      executable,
      [
        "emit",
        "--adapter",
        "codex",
        "--surface",
        "cli",
        "--runtime",
        "host",
        "--stdin"
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
    `agentbell emit ${process.platform}/${process.arch}: ` +
    `p95=${p95.toFixed(2)}ms max=${maximum.toFixed(2)}ms n=${durations.length}`
  );
  if (p95 >= limitMilliseconds) {
    throw new Error(
      `emit p95 ${p95.toFixed(2)}ms exceeds the ${limitMilliseconds}ms gate.`
    );
  }
} finally {
  await rm(temporaryRoot, { recursive: true, force: true });
}

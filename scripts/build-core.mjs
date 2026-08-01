import { spawn } from "node:child_process";
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

import {
  releaseArtifacts,
  releaseTargets
} from "./build-targets.mjs";
import { findGo, root } from "./go-tool.mjs";

function argument(name, fallback) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : fallback;
}

export function buildConcurrency(environment = process.env) {
  const raw = environment.AGENTBELL_BUILD_CONCURRENCY;
  if (raw === undefined || raw === "") {
    return 2;
  }
  const parsed = Number(raw);
  if (!Number.isInteger(parsed) || parsed < 1 || parsed > 6) {
    throw new Error(
      "AGENTBELL_BUILD_CONCURRENCY must be an integer between 1 and 6."
    );
  }
  return parsed;
}

export async function runBounded(items, limit, worker) {
  if (!Number.isInteger(limit) || limit < 1) {
    throw new Error("Concurrency limit must be a positive integer.");
  }
  let nextIndex = 0;

  async function consume() {
    while (nextIndex < items.length) {
      const currentIndex = nextIndex;
      nextIndex += 1;
      await worker(items[currentIndex], currentIndex);
    }
  }

  const workerCount = Math.min(limit, items.length);
  await Promise.all(Array.from({ length: workerCount }, () => consume()));
}

function runBuild(goExecutable, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(
      goExecutable,
      [
        "build",
        "-trimpath",
        "-ldflags",
        options.ldflags,
        "-o",
        path.join(options.output, options.artifact.fileName),
        options.artifact.command
      ],
      {
        cwd: options.core,
        env: {
          ...process.env,
          CGO_ENABLED: "0",
          GOOS: options.target.goos,
          GOARCH: options.target.goarch
        },
        stdio: "inherit",
        windowsHide: true
      }
    );
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      if (code === 0) {
        resolve();
        return;
      }
      reject(
        new Error(
          `Failed to build ${options.artifact.command} for ${options.target.goos}/${options.target.goarch}` +
            (signal ? ` (signal ${signal}).` : ` (exit ${code}).`)
        )
      );
    });
  });
}

async function main() {
  const output = path.resolve(
    argument("--output", path.join(root, "artifacts", "core"))
  );
  const version = argument("--version", "dev");
  const commit = argument("--commit", "none");
  const buildTime = argument("--build-time", new Date().toISOString());
  const goExecutable = await findGo();
  const core = path.join(root, "core");
  const ldflags = [
    "-s",
    "-w",
    `-X github.com/liming0791/agentbell/core/internal/version.Version=${version}`,
    `-X github.com/liming0791/agentbell/core/internal/version.Commit=${commit}`,
    `-X github.com/liming0791/agentbell/core/internal/version.BuildTime=${buildTime}`
  ].join(" ");
  const builds = releaseTargets.flatMap((target) =>
    releaseArtifacts(target).map((artifact) => ({ artifact, target }))
  );

  await mkdir(output, { recursive: true });
  await runBounded(builds, buildConcurrency(), async ({ artifact, target }) => {
    await runBuild(goExecutable, {
      artifact,
      core,
      ldflags,
      output,
      target
    });
    console.log(`Built ${artifact.fileName}`);
  });
}

if (
  process.argv[1] &&
  pathToFileURL(path.resolve(process.argv[1])).href === import.meta.url
) {
  await main();
}

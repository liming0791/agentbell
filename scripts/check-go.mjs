import { spawnSync } from "node:child_process";
import { readFile, rm } from "node:fs/promises";
import path from "node:path";

import {
  executableNextToGo,
  findGo,
  root
} from "./go-tool.mjs";

const core = path.join(root, "core");
const goExecutable = await findGo();
const gofmt = executableNextToGo(goExecutable, "gofmt");
const coverageFile = path.join(core, "coverage.out");

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: core,
    encoding: "utf8",
    stdio: options.capture ? "pipe" : "inherit",
    windowsHide: true
  });
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    if (options.capture) {
      process.stderr.write(result.stdout || "");
      process.stderr.write(result.stderr || "");
    }
    throw new Error(`${path.basename(command)} ${args.join(" ")} failed.`);
  }
  return result.stdout || "";
}

const formatOutput = run(gofmt, ["-l", "."], { capture: true }).trim();
if (formatOutput) {
  throw new Error(`Go files are not formatted:\n${formatOutput}`);
}

run(goExecutable, ["vet", "./..."]);
run(goExecutable, ["test", "./...", "-coverprofile=coverage.out", "-covermode=atomic"]);
const coverageOutput = run(
  goExecutable,
  ["tool", "cover", "-func=coverage.out"],
  { capture: true }
);
process.stdout.write(coverageOutput);

const totalLine = coverageOutput
  .split(/\r?\n/)
  .find((line) => line.startsWith("total:"));
const match = totalLine && /([0-9.]+)%\s*$/.exec(totalLine);
if (!match) {
  throw new Error("Unable to read total Go coverage.");
}
const coverage = Number(match[1]);
if (coverage < 75) {
  throw new Error(`Go coverage ${coverage}% is below the 75% gate.`);
}

for (const packageName of ["event", "queue", "adapter", "setup"]) {
  const packageOutput = run(
    goExecutable,
    ["test", `./internal/${packageName}`, "-cover"],
    { capture: true }
  );
  process.stdout.write(packageOutput);
  const packageMatch = /coverage:\s+([0-9.]+)%/.exec(packageOutput);
  if (!packageMatch) {
    throw new Error(`Unable to read ${packageName} package coverage.`);
  }
  const packageCoverage = Number(packageMatch[1]);
  if (packageCoverage < 80) {
    throw new Error(
      `${packageName} package coverage ${packageCoverage}% is below the 80% gate.`
    );
  }
}

await readFile(coverageFile);
await rm(coverageFile, { force: true });

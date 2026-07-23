import { spawnSync } from "node:child_process";

const result = spawnSync(
  process.execPath,
  ["--test", "--experimental-test-coverage"],
  {
    cwd: process.cwd(),
    encoding: "utf8",
    windowsHide: true
  }
);

process.stdout.write(result.stdout || "");
process.stderr.write(result.stderr || "");

if (result.error) {
  throw result.error;
}
if (result.status !== 0) {
  throw new Error(`Node.js tests failed with exit code ${result.status}.`);
}

const coreLine = (result.stdout || "")
  .split(/\r?\n/)
  .find((line) => /\bcore\.mjs\s+\|/.test(line));
const columns = coreLine?.split("|");
const bootstrapCoverage = Number(columns?.[1]?.trim());
if (!Number.isFinite(bootstrapCoverage)) {
  throw new Error("Unable to read npm bootstrap coverage.");
}
if (bootstrapCoverage < 80) {
  throw new Error(
    `npm bootstrap line coverage ${bootstrapCoverage}% is below the 80% gate.`
  );
}

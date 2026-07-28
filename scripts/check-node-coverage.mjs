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

const reportLines = (result.stdout || "").split(/\r?\n/);
for (const fileName of [
  "core.mjs",
  "detect.mjs",
  "index.mjs",
  "plan.mjs",
  "platform.mjs",
  "upgrade.mjs"
]) {
  const escapedName = fileName.replace(".", "\\.");
  const reportLine = reportLines.find(
    (line) => new RegExp(`\\b${escapedName}\\s+\\|`).test(line)
  );
  const columns = reportLine?.split("|");
  const lineCoverage = Number(columns?.[1]?.trim());
  if (!Number.isFinite(lineCoverage)) {
    throw new Error(`Unable to read npm bootstrap coverage for ${fileName}.`);
  }
  if (lineCoverage < 80) {
    throw new Error(
      `${fileName} line coverage ${lineCoverage}% is below the 80% gate.`
    );
  }
}

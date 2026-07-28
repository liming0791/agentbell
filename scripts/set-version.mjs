import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const version = process.argv[2];
const semverPattern =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/;

if (!version || !semverPattern.test(version)) {
  throw new Error("A valid semantic version is required, for example: 0.2.0");
}

const manifestPaths = [
  "package.json",
  "packages/cli/package.json",
  "packages/hook-runtime/package.json",
  "plugins/codex/agentbell/.codex-plugin/plugin.json",
  "plugins/claude/agentbell/.claude-plugin/plugin.json",
  "plugins/kimi/agentbell/kimi.plugin.json",
  "plugins/opencode/agentbell/opencode.plugin.json",
  "plugins/qoder/agentbell/.qoder-plugin/plugin.json"
];

async function readJson(relativePath) {
  return JSON.parse(await readFile(path.join(root, relativePath), "utf8"));
}

async function writeJson(relativePath, value) {
  await writeFile(
    path.join(root, relativePath),
    `${JSON.stringify(value, null, 2)}\n`,
    "utf8"
  );
}

for (const relativePath of manifestPaths) {
  const manifest = await readJson(relativePath);
  manifest.version = version;
  await writeJson(relativePath, manifest);
}

const lockPath = "package-lock.json";

try {
  const packageLock = await readJson(lockPath);
  packageLock.version = version;

  for (const key of ["", "packages/cli", "packages/hook-runtime"]) {
    if (packageLock.packages?.[key]) {
      packageLock.packages[key].version = version;
    }
  }

  await writeJson(lockPath, packageLock);
} catch (error) {
  if (error && typeof error === "object" && error.code !== "ENOENT") {
    throw error;
  }
}

console.log(`Updated ${manifestPaths.length} manifests to ${version}.`);

import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const releaseTag = process.argv[2] ?? process.env.GITHUB_REF_NAME;

async function readJson(relativePath) {
  return JSON.parse(await readFile(path.join(root, relativePath), "utf8"));
}

if (!releaseTag) {
  throw new Error("Release tag is required, for example: v0.2.0");
}

const manifests = new Map([
  ["package.json", await readJson("package.json")],
  ["packages/cli/package.json", await readJson("packages/cli/package.json")],
  [
    "packages/hook-runtime/package.json",
    await readJson("packages/hook-runtime/package.json")
  ],
  [
    "plugins/codex/agentbell/.codex-plugin/plugin.json",
    await readJson("plugins/codex/agentbell/.codex-plugin/plugin.json")
  ],
  [
    "plugins/claude/agentbell/.claude-plugin/plugin.json",
    await readJson("plugins/claude/agentbell/.claude-plugin/plugin.json")
  ],
  [
    "plugins/kimi/agentbell/kimi.plugin.json",
    await readJson("plugins/kimi/agentbell/kimi.plugin.json")
  ],
  [
    "plugins/opencode/agentbell/opencode.plugin.json",
    await readJson("plugins/opencode/agentbell/opencode.plugin.json")
  ],
  [
    "plugins/qoder/agentbell/.qoder-plugin/plugin.json",
    await readJson("plugins/qoder/agentbell/.qoder-plugin/plugin.json")
  ]
]);

const rootPackage = manifests.get("package.json");
const expectedTag = `v${rootPackage.version}`;

if (releaseTag !== expectedTag) {
  throw new Error(
    `Release tag ${releaseTag} does not match root version ${rootPackage.version}.`
  );
}

for (const [relativePath, manifest] of manifests) {
  if (manifest.version !== rootPackage.version) {
    throw new Error(
      `${relativePath} has version ${manifest.version}; expected ${rootPackage.version}.`
    );
  }
}

for (const relativePath of [
  "packages/cli/package.json",
  "packages/hook-runtime/package.json"
]) {
  const manifest = manifests.get(relativePath);

  if (manifest.private === true) {
    throw new Error(`${relativePath} must be publishable.`);
  }

  if (manifest.publishConfig?.access !== "public") {
    throw new Error(`${relativePath} must publish with public access.`);
  }

  if (!Array.isArray(manifest.files) || manifest.files.length === 0) {
    throw new Error(`${relativePath} must declare an npm files allowlist.`);
  }
  if (manifest.repository?.url !== "git+https://github.com/liming0791/agentbell.git") {
    throw new Error(`${relativePath} must link to the release repository.`);
  }
}

console.log(
  `Release verification passed for ${releaseTag} (${manifests.size} manifests).`
);

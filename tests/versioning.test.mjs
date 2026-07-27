import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const pluginManifests = [
  "plugins/codex/agentbell/.codex-plugin/plugin.json",
  "plugins/claude/agentbell/.claude-plugin/plugin.json",
  "plugins/kimi/agentbell/kimi.plugin.json",
  "plugins/opencode/agentbell/opencode.plugin.json",
  "plugins/qoder/agentbell/.qoder-plugin/plugin.json"
];

test("all released plugin manifests participate in version tooling", async () => {
  const rootManifest = JSON.parse(
    await readFile(path.join(root, "package.json"), "utf8")
  );
  const setVersion = await readFile(
    path.join(root, "scripts", "set-version.mjs"),
    "utf8"
  );
  const verifyRelease = await readFile(
    path.join(root, "scripts", "verify-release.mjs"),
    "utf8"
  );
  for (const relativePath of pluginManifests) {
    const manifest = JSON.parse(
      await readFile(path.join(root, relativePath), "utf8")
    );
    assert.equal(manifest.version, rootManifest.version, relativePath);
    assert.ok(setVersion.includes(relativePath), `set-version misses ${relativePath}`);
    assert.ok(
      verifyRelease.includes(relativePath),
      `release verification misses ${relativePath}`
    );
  }
});

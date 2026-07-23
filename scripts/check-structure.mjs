import { access, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const requiredFiles = [
  "README.md",
  "TODO.md",
  ".editorconfig",
  ".gitattributes",
  ".github/dependabot.yml",
  ".github/workflows/ci.yml",
  ".github/workflows/release.yml",
  "package.json",
  "package-lock.json",
  "config.example.json",
  "adapters/catalog.json",
  "docs/ci-cd.md",
  "docs/adapter-contract.md",
  "docs/compatibility.md",
  "docs/decisions.md",
  "docs/m0.5-execution-plan.md",
  "docs/m0.5-validation.md",
  "docs/operations.md",
  "eslint.config.mjs",
  "schemas/notification-event.schema.json",
  "scripts/set-version.mjs",
  "scripts/build-core.mjs",
  "scripts/check-go.mjs",
  "scripts/check-node-coverage.mjs",
  "scripts/go-tool.mjs",
  "scripts/benchmark-emit.mjs",
  "scripts/release-metadata.mjs",
  "scripts/smoke-bootstrap.mjs",
  "scripts/verify-release.mjs",
  "tests/cli.test.mjs",
  "tests/bootstrap.test.mjs",
  "tests/protocol.test.mjs",
  "tests/config-and-hook.test.mjs",
  "tests/normalize.test.mjs",
  "tests/plan.test.mjs",
  "tests/render.test.mjs",
  "tests/transport.test.mjs",
  "packages/cli/package.json",
  "packages/cli/bin/agentbell.mjs",
  "packages/hook-runtime/package.json",
  "core/testdata/notification-event.golden.json",
  "packages/hook-runtime/bin/agentbell-hook.mjs",
  "plugins/codex/agentbell/.codex-plugin/plugin.json",
  "plugins/codex/agentbell/hooks/hooks.json",
  "plugins/claude/agentbell/.claude-plugin/plugin.json",
  "plugins/claude/agentbell/hooks/hooks.json",
  "plugins/kimi/agentbell/kimi.plugin.json"
];

for (const relativePath of requiredFiles) {
  await access(path.join(root, relativePath));
}

const jsonFiles = [
  "package.json",
  "package-lock.json",
  "config.example.json",
  "adapters/catalog.json",
  "schemas/notification-event.schema.json",
  "packages/cli/package.json",
  "packages/hook-runtime/package.json",
  "plugins/codex/agentbell/.codex-plugin/plugin.json",
  "plugins/codex/agentbell/hooks/hooks.json",
  "plugins/claude/agentbell/.claude-plugin/plugin.json",
  "plugins/claude/agentbell/hooks/hooks.json",
  "plugins/kimi/agentbell/kimi.plugin.json"
];

for (const relativePath of jsonFiles) {
  JSON.parse(await readFile(path.join(root, relativePath), "utf8"));
}

const catalog = JSON.parse(
  await readFile(path.join(root, "adapters/catalog.json"), "utf8")
);
const embeddedCatalog = JSON.parse(
  await readFile(path.join(root, "core/internal/adapter/catalog.json"), "utf8")
);
if (JSON.stringify(catalog) !== JSON.stringify(embeddedCatalog)) {
  throw new Error(
    "Go embedded adapter catalog differs from adapters/catalog.json."
  );
}
const supportLevels = new Set([
  "verified",
  "pilot",
  "assisted",
  "unsupported"
]);
const adapterIds = new Set();

for (const adapter of catalog.adapters) {
  if (adapterIds.has(adapter.id)) {
    throw new Error(`Duplicate adapter id: ${adapter.id}`);
  }
  adapterIds.add(adapter.id);

  if (!supportLevels.has(adapter.supportLevel)) {
    throw new Error(
      `Unknown support level for ${adapter.id}: ${adapter.supportLevel}`
    );
  }

  if (adapter.phase1 && adapter.supportLevel === "unsupported") {
    throw new Error(
      `Unsupported adapter cannot be included in Phase 1: ${adapter.id}`
    );
  }

  if (adapter.supportLevel === "unsupported") {
    if (adapter.dialect !== null || adapter.events.length !== 0) {
      throw new Error(
        `Unsupported adapter must not declare a dialect or events: ${adapter.id}`
      );
    }
  }
}

console.log(`AgentBell structure check passed (${requiredFiles.length} required files).`);

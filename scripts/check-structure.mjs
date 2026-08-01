import { access, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const requiredFiles = [
  "README.md",
  "CLAUDE.md",
  "AGENTS.md",
  "TODO.md",
  ".editorconfig",
  ".gitattributes",
  ".github/dependabot.yml",
  ".github/workflows/ci.yml",
  ".github/workflows/docs.yml",
  ".github/workflows/release.yml",
  "package.json",
  "package-lock.json",
  "config.example.json",
  "adapters/catalog.json",
  "docs/ci-cd.md",
  "docs/adapter-contract.md",
  "docs/architecture.md",
  "docs/development.md",
  "docs/adr/0001-native-core.md",
  "docs/adr/0002-filesystem-spool.md",
  "docs/adr/0003-m2-compatible-state-and-relay.md",
  "docs/compatibility.md",
  "docs/decisions.md",
  "docs/m0.5-execution-plan.md",
  "docs/m0.5-validation.md",
  "docs/m1-setup-validation.md",
  "docs/m1.5-validation.md",
  "docs/m2-execution-plan.md",
  "docs/m2-validation.md",
  "docs/operations.md",
  "eslint.config.mjs",
  "schemas/notification-event.schema.json",
  "schemas/relay-envelope.schema.json",
  "schemas/settings.schema.json",
  "scripts/set-version.mjs",
  "scripts/build-targets.mjs",
  "scripts/build-core.mjs",
  "scripts/plugin-bundles.mjs",
  "scripts/check-doc-links.mjs",
  "scripts/check-go.mjs",
  "scripts/check-migrations.mjs",
  "scripts/check-node-coverage.mjs",
  "scripts/go-tool.mjs",
  "scripts/benchmark-emit.mjs",
  "scripts/benchmark-m2.mjs",
  "scripts/release-metadata.mjs",
  "scripts/release-lifecycle-smoke.mjs",
  "scripts/smoke-npm-packages.mjs",
  "scripts/smoke-bootstrap.mjs",
  "scripts/https-relay-proof.mjs",
  "scripts/smoke-https-relay.mjs",
  "scripts/verify-release.mjs",
  "tests/cli.test.mjs",
  "tests/bootstrap.test.mjs",
  "tests/benchmark-m2.test.mjs",
  "tests/ci-workflow.test.mjs",
  "tests/https-relay-smoke.test.mjs",
  "tests/build-targets.test.mjs",
  "tests/plugin-bundles.test.mjs",
  "tests/release-workflow.test.mjs",
  "tests/release-lifecycle-smoke.test.mjs",
  "tests/release-metadata.test.mjs",
  "tests/npm-package-smoke.test.mjs",
  "tests/upgrade.test.mjs",
  "tests/protocol.test.mjs",
  "tests/config-and-hook.test.mjs",
  "tests/migration-structure.test.mjs",
  "tests/normalize.test.mjs",
  "tests/plan.test.mjs",
  "tests/render.test.mjs",
  "tests/transport.test.mjs",
  "tests/versioning.test.mjs",
  "packages/cli/package.json",
  "packages/cli/bin/agentbell.mjs",
  "packages/cli/src/upgrade.mjs",
  "packages/hook-runtime/package.json",
  "core/testdata/notification-event.golden.json",
  "core/testdata/relay-envelope.golden.json",
  "core/testdata/migrations/config-v1.json",
  "core/testdata/migrations/queue-v1.json",
  "core/testdata/migrations/receipts/codex-v1.json",
  "core/testdata/migrations/receipts/claude-v1.json",
  "core/testdata/migrations/receipts/kimi-v1.json",
  "core/testdata/migrations/receipts/opencode-v1.json",
  "core/testdata/migrations/receipts/qoder-v1.json",
  "core/internal/config/migration_fixture_test.go",
  "core/internal/queue/migration_fixture_test.go",
  "core/internal/adapter/migration_fixture_test.go",
  "core/cmd/agentbell-bridge/main.go",
  "core/internal/binding/store.go",
  "core/internal/binding/store_test.go",
  "core/internal/binding/discovery.go",
  "core/internal/binding/discovery_test.go",
  "core/internal/bridge/bridge.go",
  "core/internal/bridge/bridge_test.go",
  "core/internal/installstate/state.go",
  "core/internal/installstate/state_test.go",
  "core/internal/policy/policy.go",
  "core/internal/policy/policy_test.go",
  "core/internal/policy/quiet.go",
  "core/internal/policy/quiet_test.go",
  "core/internal/policy/template.go",
  "core/internal/policy/template_test.go",
  "core/internal/queue/ledger.go",
  "core/internal/queue/ledger_test.go",
  "core/internal/relay/durable_edge_test.go",
  "core/internal/relay/frame.go",
  "core/internal/relay/frame_test.go",
  "core/internal/relay/forwarder.go",
  "core/internal/relay/forwarder_test.go",
  "core/internal/relay/http.go",
  "core/internal/relay/http_test.go",
  "core/internal/relay/ingress.go",
  "core/internal/relay/ingress_test.go",
  "core/internal/relay/nonce.go",
  "core/internal/relay/nonce_test.go",
  "core/internal/relay/outbox.go",
  "core/internal/relay/outbox_test.go",
  "core/internal/relay/pairing.go",
  "core/internal/relay/pairing_http.go",
  "core/internal/relay/pairing_http_test.go",
  "core/internal/relay/pairing_test.go",
  "core/internal/relay/protocol.go",
  "core/internal/relay/protocol_test.go",
  "core/internal/relay/receipt.go",
  "core/internal/relay/receipt_test.go",
  "core/internal/relay/server.go",
  "core/internal/relay/server_test.go",
  "core/internal/relay/storage.go",
  "core/internal/relay/stress_test.go",
  "core/internal/relay/stdio.go",
  "core/internal/relay/stdio_test.go",
  "core/internal/remote/command.go",
  "core/internal/remote/command_test.go",
  "core/internal/remote/doc.go",
  "core/internal/remote/drain.go",
  "core/internal/remote/drain_test.go",
  "core/internal/remote/edge_test.go",
  "core/internal/remote/https.go",
  "core/internal/remote/https_test.go",
  "core/internal/remote/pairing_client.go",
  "core/internal/remote/pairing_client_test.go",
  "core/internal/remote/pair_protocol.go",
  "core/internal/remote/pair_protocol_test.go",
  "core/internal/remote/pairer.go",
  "core/internal/remote/pairer_test.go",
  "core/internal/remote/pull.go",
  "core/internal/remote/pull_test.go",
  "core/internal/remote/retry.go",
  "core/internal/remote/retry_test.go",
  "core/internal/remote/scheduler.go",
  "core/internal/remote/scheduler_test.go",
  "core/internal/remoteconfig/host_connectors.go",
  "core/internal/remoteconfig/host_connectors_test.go",
  "core/internal/remoteconfig/models.go",
  "core/internal/remoteconfig/peers.go",
  "core/internal/remoteconfig/peers_test.go",
  "core/internal/remoteconfig/remote_transactions.go",
  "core/internal/remoteconfig/store.go",
  "core/internal/remoteconfig/config_test.go",
  "core/internal/remoteconfig/validation.go",
  "core/internal/remoteconfig/validation_test.go",
  "core/internal/app/relay_connector.go",
  "core/internal/app/relay_connector_test.go",
  "core/internal/app/remote_scheduler.go",
  "core/internal/app/remote_scheduler_test.go",
  "core/internal/secretstore/command.go",
  "core/internal/secretstore/file.go",
  "core/internal/secretstore/protector_other.go",
  "core/internal/secretstore/protector_other_test.go",
  "core/internal/secretstore/protector_windows.go",
  "core/internal/secretstore/publish_other.go",
  "core/internal/secretstore/publish_windows.go",
  "core/internal/secretstore/store.go",
  "core/internal/secretstore/store_test.go",
  "core/internal/settings/save.go",
  "core/internal/settings/settings.go",
  "core/internal/settings/settings_test.go",
  "core/internal/adapter/stable_bridge.go",
  "core/internal/adapter/stable_bridge_test.go",
  "core/internal/adapter/codex.go",
  "core/internal/adapter/codex_test.go",
  "core/internal/adapter/claude.go",
  "core/internal/adapter/claude_test.go",
  "core/internal/adapter/kimi.go",
  "core/internal/adapter/kimi_test.go",
  "core/internal/adapter/opencode.go",
  "core/internal/adapter/opencode_test.go",
  "core/internal/adapter/qoder.go",
  "core/internal/adapter/qoder_test.go",
  "core/internal/adapter/qoderwork.go",
  "core/internal/adapter/qoderwork_test.go",
  "core/internal/adapter/shell_hooks.go",
  "core/internal/adapter/trae.go",
  "core/internal/adapter/trae_test.go",
  "core/internal/service/manager.go",
  "core/internal/service/manager_test.go",
  "core/internal/service/processor.go",
  "core/internal/service/m2_test.go",
  "core/internal/app/settings.go",
  "core/internal/app/settings_test.go",
  "core/internal/app/plugin.go",
  "core/internal/app/plugin_test.go",
  "core/internal/app/relay.go",
  "core/internal/app/relay_test.go",
  "core/internal/app/remote.go",
  "core/internal/app/remote_test.go",
  "core/internal/pluginverify/semver.go",
  "core/internal/pluginverify/semver_test.go",
  "core/internal/pluginverify/sigstore.go",
  "core/internal/pluginverify/sigstore_test.go",
  "core/internal/pluginverify/types.go",
  "core/internal/pluginverify/verify.go",
  "core/internal/pluginverify/verify_test.go",
  "packages/hook-runtime/bin/agentbell-hook.mjs",
  "plugins/codex/agentbell/.codex-plugin/plugin.json",
  "plugins/codex/agentbell/hooks/hooks.json",
  "plugins/codex/agentbell/scripts/hook.mjs",
  "plugins/codex/agentbell/skills/agentbell-setup/SKILL.md",
  "plugins/claude/agentbell/.claude-plugin/plugin.json",
  "plugins/claude/agentbell/hooks/hooks.json",
  "plugins/claude/agentbell/scripts/hook.mjs",
  "plugins/claude/agentbell/skills/agentbell-setup/SKILL.md",
  "plugins/kimi/agentbell/kimi.plugin.json",
  "plugins/kimi/agentbell/scripts/hook.mjs",
  "plugins/kimi/agentbell/skills/agentbell-setup/SKILL.md",
  "plugins/opencode/agentbell/opencode.plugin.json",
  "plugins/opencode/agentbell/scripts/hook.mjs",
  "plugins/opencode/agentbell/skills/agentbell-setup/SKILL.md",
  "plugins/qoder/agentbell/.qoder-plugin/plugin.json",
  "plugins/qoder/agentbell/hooks/hooks.json",
  "plugins/qoder/agentbell/scripts/hook.mjs",
  "plugins/qoder/agentbell/skills/agentbell-setup/SKILL.md"
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
  "schemas/relay-envelope.schema.json",
  "schemas/settings.schema.json",
  "packages/cli/package.json",
  "packages/hook-runtime/package.json",
  "plugins/codex/agentbell/.codex-plugin/plugin.json",
  "plugins/codex/agentbell/hooks/hooks.json",
  "plugins/claude/agentbell/.claude-plugin/plugin.json",
  "plugins/claude/agentbell/hooks/hooks.json",
  "plugins/kimi/agentbell/kimi.plugin.json",
  "plugins/opencode/agentbell/opencode.plugin.json",
  "plugins/qoder/agentbell/.qoder-plugin/plugin.json",
  "plugins/qoder/agentbell/hooks/hooks.json",
  "core/testdata/migrations/config-v1.json",
  "core/testdata/migrations/queue-v1.json",
  "core/testdata/migrations/receipts/codex-v1.json",
  "core/testdata/migrations/receipts/claude-v1.json",
  "core/testdata/migrations/receipts/kimi-v1.json",
  "core/testdata/migrations/receipts/opencode-v1.json",
  "core/testdata/migrations/receipts/qoder-v1.json"
];

for (const relativePath of jsonFiles) {
  JSON.parse(await readFile(path.join(root, relativePath), "utf8"));
}

const notificationSchema = JSON.parse(
  await readFile(
    path.join(root, "schemas/notification-event.schema.json"),
    "utf8"
  )
);
const settingsSchema = JSON.parse(
  await readFile(path.join(root, "schemas/settings.schema.json"), "utf8")
);
const relaySchema = JSON.parse(
  await readFile(path.join(root, "schemas/relay-envelope.schema.json"), "utf8")
);

function assertSameEnum(label, left, right) {
  if (JSON.stringify(left) !== JSON.stringify(right)) {
    throw new Error(`${label} differs from NotificationEvent.`);
  }
}

assertSameEnum(
  "settings events",
  settingsSchema.$defs.eventName.enum,
  notificationSchema.properties.event.enum
);
assertSameEnum(
  "settings sources",
  settingsSchema.$defs.policyMatch.properties.sources.items.enum,
  notificationSchema.properties.source.enum
);
assertSameEnum(
  "settings surfaces",
  settingsSchema.$defs.policyMatch.properties.surfaces.items.enum,
  notificationSchema.properties.surface.enum
);
assertSameEnum(
  "settings runtimes",
  settingsSchema.$defs.policyMatch.properties.runtimes.items.enum,
  notificationSchema.properties.runtime.enum
);
assertSameEnum(
  "settings priorities",
  settingsSchema.$defs.policyMatch.properties.priorities.items.enum,
  notificationSchema.properties.priority.enum
);
assertSameEnum(
  "relay runtimes",
  relaySchema.properties.origin.properties.runtime.enum,
  notificationSchema.properties.runtime.enum
);
if (relaySchema.properties.event.$ref !== "./notification-event.schema.json") {
  throw new Error("RelayEnvelope must reference NotificationEvent schema.");
}

const catalogSource = await readFile(
  path.join(root, "adapters/catalog.json"),
  "utf8"
);
const embeddedCatalogSource = await readFile(
  path.join(root, "core/internal/adapter/catalog.json"),
  "utf8"
);
const catalog = JSON.parse(catalogSource);
JSON.parse(embeddedCatalogSource);
if (catalogSource !== embeddedCatalogSource) {
  throw new Error(
    "Go embedded adapter catalog must be byte-identical to adapters/catalog.json."
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

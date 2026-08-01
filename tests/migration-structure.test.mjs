import assert from "node:assert/strict";
import { access, readFile, readdir } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  ".."
);
const migrationRoot = path.join(root, "core", "testdata", "migrations");
const expectedFixtures = [
  "config-v1.json",
  "queue-v1.json",
  "receipts/claude-v1.json",
  "receipts/codex-v1.json",
  "receipts/kimi-v1.json",
  "receipts/opencode-v1.json",
  "receipts/qoder-v1.json"
];

async function fixtureFiles(directory, prefix = "") {
  const entries = await readdir(directory, { withFileTypes: true });
  const result = [];
  for (const entry of entries) {
    const relative = path.posix.join(prefix, entry.name);
    if (entry.isDirectory()) {
      result.push(...await fixtureFiles(
        path.join(directory, entry.name),
        relative
      ));
    } else {
      result.push(relative);
    }
  }
  return result.sort();
}

test("migration fixture inventory and privacy contract are fixed", async () => {
  assert.deepEqual(await fixtureFiles(migrationRoot), expectedFixtures);

  const documents = new Map();
  for (const relativePath of expectedFixtures) {
    const raw = await readFile(path.join(migrationRoot, relativePath), "utf8");
    documents.set(relativePath, { raw, value: JSON.parse(raw) });
  }

  const config = documents.get("config-v1.json").value;
  assert.equal(config.notifications.includeSummary, false);
  assert.equal("privacyLevel" in config.notifications, false);

  const queue = documents.get("queue-v1.json").value;
  assert.equal(queue.queueVersion, 1);
  assert.equal(queue.event.privacyLevel, "metadata-only");
  assert.equal("cwd" in queue.event, false);
  assert.equal("summary" in queue.event, false);
  for (const identifier of ["sessionId", "taskId", "turnId"]) {
    assert.match(queue.event[identifier], /^[a-f0-9]{64}$/);
  }

  for (const [relativePath, document] of documents) {
    if (!relativePath.startsWith("receipts/")) {
      continue;
    }
    assert.equal(document.value.version, 1);
    const lower = document.raw.toLowerCase();
    for (const forbidden of [
      "\"cwd\"",
      "\"summary\"",
      "\"prompt\"",
      "\"token\"",
      "\"secret\"",
      "\"chatid\"",
      "/users/",
      "\\users\\"
    ]) {
      assert.equal(
        lower.includes(forbidden),
        false,
        `${relativePath} contains ${forbidden}`
      );
    }
    if (relativePath !== "receipts/opencode-v1.json") {
      assert.match(document.raw, /--fail-open/);
    }
  }
});

test("CI keeps migration fixtures on Linux, Windows and macOS without extra jobs", async () => {
  const packageDocument = JSON.parse(
    await readFile(path.join(root, "package.json"), "utf8")
  );
  assert.equal(
    packageDocument.scripts["test:migrations"],
    "node ./scripts/check-migrations.mjs"
  );
  await access(path.join(root, "scripts", "check-migrations.mjs"));
  const command = await readFile(
    path.join(root, "scripts", "check-migrations.mjs"),
    "utf8"
  );
  for (const packageName of ["config", "queue", "adapter"]) {
    assert.match(command, new RegExp(`\\./internal/${packageName}`));
  }

  const workflow = await readFile(
    path.join(root, ".github", "workflows", "ci.yml"),
    "utf8"
  );
  const qualityJob = workflow.match(
    /\n {2}quality:\n[\s\S]*?(?=\n {2}[a-z0-9-]+:\n|$)/
  );
  const compatibilityJob = workflow.match(
    /\n {2}compatibility:\n[\s\S]*?(?=\n {2}[a-z0-9-]+:\n|$)/
  );
  assert.ok(qualityJob, "quality job is missing");
  assert.ok(compatibilityJob, "compatibility job is missing");
  assert.match(qualityJob[0], /npm run test:migrations/);
  assert.match(compatibilityJob[0], /npm run test:migrations/);
  for (const operatingSystem of ["windows-latest", "macos-latest"]) {
    assert.match(
      compatibilityJob[0],
      new RegExp(`- os: ${operatingSystem}`)
    );
  }
  assert.doesNotMatch(workflow, /\n {2}migration:\n/u);
});

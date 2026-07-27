import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

test("release workflow keylessly signs and verifies final plugin bundles", async () => {
  const workflow = await readFile(
    path.join(root, ".github", "workflows", "release.yml"),
    "utf8"
  );
  for (const required of [
    "id-token: write",
    "uses: sigstore/cosign-installer@v4.1.2",
    "node scripts/plugin-bundles.mjs",
    "--signature-status sigstore-verified",
    "cosign sign-blob --yes",
    "--bundle \"$bundle_dir/plugin.sigstore.json\"",
    "\"$host_core\" plugin verify \"$bundle_dir\" --json",
    "name: Smoke-test final Linux Core over TLS and HTTPS",
    "AGENTBELL_HTTPS_SMOKE_BINARY:",
    "npm run smoke:https",
    "node scripts/release-lifecycle-smoke.mjs",
    "--previous-version \"${M2_PREVIOUS_TAG#v}\"",
    "name: agentbell-plugins",
    "gh release download \"$RELEASE_TAG\"",
    "\"$installed_core\" plugin verify \"$plugin_root\" --json"
  ]) {
    assert.ok(workflow.includes(required), `missing release step: ${required}`);
  }
  assert.match(
    workflow,
    /SIGNATURE_STATUS:\s*\$\{\{\s*vars\.AGENTBELL_SIGNATURE_STATUS\s*\|\|\s*'technical-preview'\s*\}\}/
  );
  assert.match(
    workflow,
    /if \[ "\$SIGNATURE_STATUS" != "technical-preview" \]; then/
  );
});

test("release smoke gates irreversible publication", async () => {
  const workflow = await readFile(
    path.join(root, ".github", "workflows", "release.yml"),
    "utf8"
  );
  const staged = workflow.indexOf(
    "name: Stage Core, bridge and signed plugins in a draft GitHub release"
  );
  const httpsSmoke = workflow.indexOf(
    "name: Smoke-test final Linux Core over TLS and HTTPS"
  );
  const smoke = workflow.indexOf(
    "name: Smoke-test the staged GitHub release"
  );
  const publishNpm = workflow.indexOf("name: Publish npm packages");
  const publishRelease = workflow.indexOf(
    "name: Publish the completed GitHub release"
  );
  assert.ok(staged >= 0, "release assets must first be staged as a draft");
  assert.ok(
    httpsSmoke >= 0 && httpsSmoke < staged,
    "final Linux Core TLS smoke must pass before draft staging"
  );
  assert.ok(smoke > staged, "staged artifacts must be smoke-tested");
  assert.ok(
    publishNpm > smoke,
    "npm publication must happen only after the staged Release smoke"
  );
  assert.ok(
    publishRelease > publishNpm,
    "the GitHub Release must remain draft until npm publication succeeds"
  );
});

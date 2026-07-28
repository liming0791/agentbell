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

test("release staging leaves a verified draft before manual finalization", async () => {
  const workflow = await readFile(
    path.join(root, ".github", "workflows", "release.yml"),
    "utf8"
  );
  const staged = workflow.indexOf(
    "name: Stage Core, bridge, signed plugins and npm archives in a draft GitHub release"
  );
  const packNpm = workflow.indexOf("name: Build final npm package archives");
  const includeNpm = workflow.indexOf("--include-directory artifacts/npm");
  const httpsSmoke = workflow.indexOf(
    "name: Smoke-test final Linux Core over TLS and HTTPS"
  );
  const smoke = workflow.indexOf(
    "name: Smoke-test the staged GitHub release"
  );
  const leaveDraft = workflow.indexOf(
    "name: Leave the verified release as a draft"
  );
  const requireDraft = workflow.indexOf(
    "name: Require an existing draft release"
  );
  const finalizeJob = workflow.indexOf("finalize-release:");
  const reverify = workflow.indexOf(
    "name: Re-verify the draft GitHub release"
  );
  const publishNpm = workflow.indexOf("name: Publish npm packages");
  const publishRelease = workflow.indexOf(
    "name: Publish the completed GitHub release"
  );
  assert.ok(staged >= 0, "release assets must first be staged as a draft");
  assert.ok(
    packNpm >= 0 && packNpm < staged,
    "final npm archives must be built before draft staging"
  );
  assert.ok(
    includeNpm > packNpm && includeNpm < staged,
    "npm archives must be covered by release metadata before staging"
  );
  assert.ok(
    httpsSmoke >= 0 && httpsSmoke < staged,
    "final Linux Core TLS smoke must pass before draft staging"
  );
  assert.ok(smoke > staged, "staged artifacts must be smoke-tested");
  assert.ok(
    leaveDraft > smoke && leaveDraft < finalizeJob,
    "the stage action must end with an unpublished verified draft"
  );
  assert.ok(
    finalizeJob > leaveDraft &&
      requireDraft > finalizeJob &&
      reverify > requireDraft,
    "manual finalization must require and re-verify the existing draft"
  );
  const stageJob = workflow.slice(
    workflow.indexOf("stage-draft:"),
    finalizeJob
  );
  assert.ok(
    !stageJob.includes("npm publish") &&
      !stageJob.includes("--draft=false") &&
      !stageJob.includes("environment: npm-publish"),
    "tag staging must not publish npm or the GitHub Release"
  );
  assert.ok(
    publishNpm > reverify,
    "npm publication must happen only after final draft re-verification"
  );
  assert.ok(
    publishRelease > publishNpm,
    "the GitHub Release must remain draft until npm publication succeeds"
  );
  for (const required of [
    "type: choice",
    "- stage",
    "- finalize",
    "if: github.event_name == 'push' || inputs.action == 'stage'",
    "if: github.event_name == 'workflow_dispatch' && inputs.action == 'finalize'",
    "environment: npm-publish",
    "NPM_PUBLISH_ENABLED must be true before finalizing a Release.",
    "name: Verify draft metadata against the selected tag",
    ".commit == $commit",
    "sha256sum --check checksums.txt",
    "name: agentbell-npm",
    "gh release download \"$RELEASE_TAG\"",
    "--pattern 'agentbell-*.tgz'",
    "node scripts/smoke-npm-packages.mjs",
    "--checksums",
    "--manifest",
    "dist.integrity",
    "Published npm archive does not match the final Release tgz."
  ]) {
    assert.ok(workflow.includes(required), `missing npm release gate: ${required}`);
  }
});

test("release reruns cannot mutate a public release or finalize without a draft", async () => {
  const workflow = await readFile(
    path.join(root, ".github", "workflows", "release.yml"),
    "utf8"
  );
  for (const required of [
    "existing_is_draft=\"$(gh release view \"$RELEASE_TAG\" --json isDraft --jq .isDraft)\"",
    "Refusing to replace assets on an already published Release.",
    "Finalize requires an existing draft Release.",
    "gh release edit \"$RELEASE_TAG\" --draft=false"
  ]) {
    assert.ok(
      workflow.includes(required),
      `missing published Release rerun guard: ${required}`
    );
  }
  assert.ok(
    !workflow.includes("--draft=true"),
    "a failed rerun must never re-draft an already published Release"
  );
});

test("draft lifecycle CI uses two real Release boundaries without publishing", async () => {
  const workflow = await readFile(
    path.join(root, ".github", "workflows", "release.yml"),
    "utf8"
  );
  for (const required of [
    "draft-lifecycle:",
    "- lifecycle",
    "inputs.action == 'lifecycle'",
    "gh release view \"$PREVIOUS_TAG\"",
    "gh release download \"$PREVIOUS_TAG\"",
    "gh release download \"$CURRENT_TAG\"",
    "Install the real previous Release through its npm bootstrap",
    "install-core --version \"$previous_version\"",
    "--preinstalled-previous",
    "Upgrade, preserve Hook bytes, send fixture, rollback and uninstall",
    "Prove the target is still Draft and npm is still unpublished",
    "draft-release-lifecycle-evidence.json"
  ]) {
    assert.ok(workflow.includes(required), `missing lifecycle proof: ${required}`);
  }
  const lifecycleJob = workflow.slice(
    workflow.indexOf("draft-lifecycle:"),
    workflow.indexOf("finalize-release:")
  );
  assert.ok(
    !lifecycleJob.includes("npm publish") &&
      !lifecycleJob.includes("--draft=false") &&
      !lifecycleJob.includes("contents: write"),
    "the lifecycle proof must not publish or mutate either Release"
  );
});

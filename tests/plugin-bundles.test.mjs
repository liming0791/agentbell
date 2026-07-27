import assert from "node:assert/strict";
import {
  cp,
  mkdir,
  mkdtemp,
  readFile,
  symlink,
  writeFile
} from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  buildPluginBundles,
  releasePlugins
} from "../scripts/plugin-bundles.mjs";

const repositoryRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  ".."
);
const version = JSON.parse(
  await readFile(path.join(repositoryRoot, "package.json"), "utf8")
).version;

test("stages five deterministic signed plugin manifests", async () => {
  // The production builder deliberately confines output to the repository.
  // Use a repository-local temporary root for this invariant.
  const localTemporary = await mkdtemp(path.join(repositoryRoot, ".plugin-test-"));
  const first = path.join(localTemporary, "first");
  const second = path.join(localTemporary, "second");
  try {
    const firstResults = await buildPluginBundles({
      rootDirectory: repositoryRoot,
      outputDirectory: first,
      version
    });
    const secondResults = await buildPluginBundles({
      rootDirectory: repositoryRoot,
      outputDirectory: second,
      version
    });
    assert.equal(firstResults.length, 5);
    assert.deepEqual(
      firstResults.map((value) => value.pluginId),
      releasePlugins.map((value) => value.pluginId)
    );
    for (let index = 0; index < firstResults.length; index += 1) {
      const firstManifest = await readFile(
        firstResults[index].manifestPath,
        "utf8"
      );
      const secondManifest = await readFile(
        secondResults[index].manifestPath,
        "utf8"
      );
      assert.equal(firstManifest, secondManifest);
      const parsed = JSON.parse(firstManifest);
      assert.equal(parsed.pluginVersion, version);
      assert.equal(parsed.minCoreVersion, version);
      assert.equal(parsed.maxCoreVersion, version);
      assert.equal(parsed.signatureStatus, "sigstore-verified");
      assert.ok(parsed.files.length > 0);
      assert.deepEqual(
        parsed.files.map((file) => file.path),
        [...parsed.files.map((file) => file.path)].sort((a, b) =>
          a.localeCompare(b, "en")
        )
      );
      assert.ok(
        parsed.files.every((file) =>
          /^sha256:[a-f0-9]{64}$/.test(file.sha256) &&
          Number.isSafeInteger(file.size) &&
          file.size >= 0
        )
      );
    }
  } finally {
    await import("node:fs/promises").then(({ rm }) =>
      rm(localTemporary, { recursive: true, force: true })
    );
  }
});

test("refuses preview downgrade and source version mismatch", async () => {
  const localTemporary = await mkdtemp(path.join(repositoryRoot, ".plugin-test-"));
  try {
    await assert.rejects(
      buildPluginBundles({
        rootDirectory: repositoryRoot,
        outputDirectory: path.join(localTemporary, "preview"),
        version,
        signatureStatus: "technical-preview"
      }),
      /must use sigstore-verified/
    );

    const fixtureRoot = path.join(localTemporary, "fixture");
    await mkdir(path.join(fixtureRoot, "plugin"), { recursive: true });
    await writeFile(
      path.join(fixtureRoot, "plugin", "plugin.json"),
      `${JSON.stringify({ name: "agentbell", version: "9.9.9" })}\n`
    );
    await assert.rejects(
      buildPluginBundles({
        rootDirectory: fixtureRoot,
        outputDirectory: path.join(fixtureRoot, "output"),
        version,
        plugins: [{
          name: "fixture",
          pluginId: "agentbell.fixture",
          source: "plugin",
          productManifest: "plugin.json",
          files: ["plugin.json"]
        }]
      }),
      /has version 9.9.9/
    );
    await writeFile(
      path.join(fixtureRoot, "plugin", "plugin.json"),
      `${JSON.stringify({ name: "agentbell", version })}\n`
    );
    await writeFile(path.join(fixtureRoot, "plugin", "unexpected.txt"), "secret\n");
    await assert.rejects(
      buildPluginBundles({
        rootDirectory: fixtureRoot,
        outputDirectory: path.join(fixtureRoot, "extra-output"),
        version,
        plugins: [{
          name: "fixture",
          pluginId: "agentbell.fixture",
          source: "plugin",
          productManifest: "plugin.json",
          files: ["plugin.json"]
        }]
      }),
      /file set differs from the release allowlist/
    );
  } finally {
    await import("node:fs/promises").then(({ rm }) =>
      rm(localTemporary, { recursive: true, force: true })
    );
  }
});

test("refuses symlinks and reserved controls in plugin payloads", async (t) => {
  if (process.platform === "win32") {
    t.skip("symlink creation is not reliably available to unprivileged Windows CI");
    return;
  }
  const localTemporary = await mkdtemp(path.join(repositoryRoot, ".plugin-test-"));
  try {
    const fixtureRoot = path.join(localTemporary, "fixture");
    const source = path.join(fixtureRoot, "plugin");
    await mkdir(source, { recursive: true });
    await writeFile(
      path.join(source, "plugin.json"),
      `${JSON.stringify({ name: "agentbell", version })}\n`
    );
    await symlink("plugin.json", path.join(source, "alias.json"));
    await assert.rejects(
      buildPluginBundles({
        rootDirectory: fixtureRoot,
        outputDirectory: path.join(fixtureRoot, "output"),
        version,
        plugins: [{
          name: "fixture",
          pluginId: "agentbell.fixture",
          source: "plugin",
          productManifest: "plugin.json",
          files: ["plugin.json"]
        }]
      }),
      /contains a symlink/
    );

    const copied = path.join(fixtureRoot, "plugin-reserved");
    await cp(source, copied, { recursive: true, dereference: true });
    await writeFile(path.join(copied, "plugin-manifest.json"), "{}\n");
    await assert.rejects(
      buildPluginBundles({
        rootDirectory: fixtureRoot,
        outputDirectory: path.join(fixtureRoot, "reserved-output"),
        version,
        plugins: [{
          name: "fixture",
          pluginId: "agentbell.fixture",
          source: "plugin-reserved",
          productManifest: "plugin.json",
          files: ["plugin.json"]
        }]
      }),
      /reserved control file/
    );
  } finally {
    await import("node:fs/promises").then(({ rm }) =>
      rm(localTemporary, { recursive: true, force: true })
    );
  }
});

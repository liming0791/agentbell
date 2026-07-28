import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { verifyNpmArchiveMetadata } from "../scripts/smoke-npm-packages.mjs";

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

test("npm package smoke verifies both final archives against release metadata", async (context) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "agentbell-npm-smoke-"));
  context.after(() => rm(root, { recursive: true, force: true }));
  const cli = path.join(root, "liming0791-agentbell-cli-0.3.0-rc.1.tgz");
  const hook = path.join(
    root,
    "liming0791-agentbell-hook-runtime-0.3.0-rc.1.tgz"
  );
  const cliBytes = Buffer.from("cli");
  const hookBytes = Buffer.from("hook");
  await writeFile(cli, cliBytes);
  await writeFile(hook, hookBytes);
  const checksumsPath = path.join(root, "checksums.txt");
  await writeFile(
    checksumsPath,
    `${sha256(cliBytes)}  ${path.basename(cli)}\n` +
      `${sha256(hookBytes)}  ${path.basename(hook)}\n`
  );
  const manifestPath = path.join(root, "release-manifest.json");
  await writeFile(
    manifestPath,
    `${JSON.stringify({
      schemaVersion: 1,
      version: "0.3.0-rc.1",
      signatureStatus: "technical-preview",
      artifacts: [
        {
          fileName: path.basename(cli),
          sha256: sha256(cliBytes),
          size: cliBytes.length
        },
        {
          fileName: path.basename(hook),
          sha256: sha256(hookBytes),
          size: hookBytes.length
        }
      ]
    })}\n`
  );

  const result = await verifyNpmArchiveMetadata({
    cliArchive: cli,
    hookRuntimeArchive: hook,
    checksumsPath,
    manifestPath,
    version: "0.3.0-rc.1"
  });
  assert.equal(result.archivesVerified, 2);

  await writeFile(cli, "tampered");
  await assert.rejects(
    verifyNpmArchiveMetadata({
      cliArchive: cli,
      hookRuntimeArchive: hook,
      checksumsPath,
      manifestPath,
      version: "0.3.0-rc.1"
    }),
    /checksum/i
  );
});

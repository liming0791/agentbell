import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { spawn } from "node:child_process";
import os from "node:os";
import path from "node:path";
import test from "node:test";

function run(executable, args) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {
      shell: false,
      stdio: ["ignore", "pipe", "pipe"]
    });
    const stderr = [];
    child.stderr.on("data", (value) => stderr.push(value));
    child.once("error", reject);
    child.once("close", (code) => {
      if (code !== 0) {
        reject(new Error(Buffer.concat(stderr).toString("utf8")));
        return;
      }
      resolve();
    });
  });
}

test("release metadata covers final npm package archives", async (context) => {
  const root = await mkdtemp(path.join(os.tmpdir(), "agentbell-release-metadata-"));
  context.after(() => rm(root, { recursive: true, force: true }));
  const core = path.join(root, "core");
  const npm = path.join(root, "npm");
  await mkdir(core);
  await mkdir(npm);
  await writeFile(path.join(core, "agentbell-linux-amd64"), "core");
  await writeFile(
    path.join(npm, "agentbell-cli-0.3.0-rc.1.tgz"),
    "cli archive"
  );
  await writeFile(
    path.join(npm, "agentbell-hook-runtime-0.3.0-rc.1.tgz"),
    "runtime archive"
  );

  await run(process.execPath, [
    "scripts/release-metadata.mjs",
    "--directory",
    core,
    "--include-directory",
    npm,
    "--version",
    "0.3.0-rc.1",
    "--commit",
    "abc123",
    "--signature-status",
    "technical-preview"
  ]);

  const checksums = await readFile(path.join(core, "checksums.txt"), "utf8");
  assert.match(checksums, /agentbell-cli-0\.3\.0-rc\.1\.tgz/);
  assert.match(checksums, /agentbell-hook-runtime-0\.3\.0-rc\.1\.tgz/);
  const manifest = JSON.parse(
    await readFile(path.join(core, "release-manifest.json"), "utf8")
  );
  assert.deepEqual(
    manifest.artifacts.map((artifact) => artifact.fileName).sort(),
    [
      "agentbell-cli-0.3.0-rc.1.tgz",
      "agentbell-hook-runtime-0.3.0-rc.1.tgz",
      "agentbell-linux-amd64"
    ]
  );
});

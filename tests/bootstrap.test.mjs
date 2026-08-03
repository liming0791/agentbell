import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import {
  mkdir,
  mkdtemp,
  readFile,
  rm,
  writeFile
} from "node:fs/promises";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import {
  coreInstallPath,
  installCore,
  runCore,
  uninstallCore
} from "../packages/cli/src/core.mjs";
import {
  resolveDataRoot,
  resolveTarget
} from "../packages/cli/src/platform.mjs";
import {
  activeStatePath,
  stableBridgePath
} from "../packages/cli/src/upgrade.mjs";

const WebResponse = globalThis.Response;

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

async function withReleaseServer(binary, checksumValue, callback) {
  const server = http.createServer((request, response) => {
    if (request.url.endsWith("/checksums.txt")) {
      response.writeHead(200, { "content-type": "text/plain" });
      response.end(`${checksumValue}  agentbell-windows-amd64.exe\n`);
      return;
    }
    if (request.url.endsWith("/agentbell-windows-amd64.exe")) {
      response.writeHead(200, { "content-type": "application/octet-stream" });
      response.end(binary);
      return;
    }
    response.writeHead(404);
    response.end();
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  try {
    await callback(`http://127.0.0.1:${address.port}`);
  } finally {
    await new Promise((resolve, reject) => {
      server.close((error) => error ? reject(error) : resolve());
    });
  }
}

test("resolves all supported release targets", () => {
  const windows = resolveTarget("win32", "x64");
  assert.equal(windows.fileName, "agentbell-windows-amd64.exe");
  assert.equal(
    windows.bridgeFileName,
    "agentbell-bridge-windows-amd64.exe"
  );
  assert.equal(
    windows.serviceBridgeFileName,
    "agentbell-service-windows-amd64.exe"
  );
  const darwin = resolveTarget("darwin", "arm64");
  assert.equal(darwin.fileName, "agentbell-darwin-arm64");
  assert.equal(
    darwin.bridgeFileName,
    "agentbell-bridge-darwin-arm64"
  );
  const linux = resolveTarget("linux", "x64");
  assert.equal(linux.fileName, "agentbell-linux-amd64");
  assert.equal(linux.bridgeFileName, "agentbell-bridge-linux-amd64");
  assert.throws(() => resolveTarget("aix", "ppc64"), /does not support/);
});

test("uses platform data roots and explicit overrides", () => {
  assert.match(
    resolveDataRoot({
      platform: "linux",
      home: "/home/test",
      env: {}
    }),
    /\.local[\\/]share[\\/]agentbell$/
  );
  assert.equal(
    resolveDataRoot({
      platform: "linux",
      home: "/home/test",
      env: { AGENTBELL_DATA_DIR: path.resolve("custom-data") }
    }),
    path.resolve("custom-data")
  );
});

test("downloads, verifies and reuses native Core", async (context) => {
  const binary = Buffer.from("fake-windows-binary");
  const expected = sha256(binary);
  const temporaryRoot = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-bootstrap-test-")
  );
  context.after(async () => {
    await rm(temporaryRoot, { recursive: true, force: true });
  });

  await withReleaseServer(binary, expected, async (releaseBase) => {
    const first = await installCore({
      version: "0.2.0",
      releaseBase,
      platform: "win32",
      architecture: "x64",
      dataRoot: temporaryRoot
    });
    assert.equal(first.reused, false);
    assert.deepEqual(await readFile(first.path), binary);
    assert.equal(
      first.path,
      coreInstallPath({
        version: "0.2.0",
        platform: "win32",
        architecture: "x64",
        dataRoot: temporaryRoot
      })
    );

    const second = await installCore({
      version: "0.2.0",
      releaseBase,
      platform: "win32",
      architecture: "x64",
      dataRoot: temporaryRoot
    });
    assert.equal(second.reused, true);
  });
});

test("rejects a binary with the wrong SHA-256", async (context) => {
  const temporaryRoot = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-bootstrap-test-")
  );
  context.after(async () => {
    await rm(temporaryRoot, { recursive: true, force: true });
  });
  await withReleaseServer(Buffer.from("tampered"), "0".repeat(64), async (releaseBase) => {
    await assert.rejects(
      installCore({
        version: "0.2.0",
        releaseBase,
        platform: "win32",
        architecture: "x64",
        dataRoot: temporaryRoot
      }),
      /SHA-256 mismatch/
    );
  });
});

test("reports offline and HTTP failures without writing a binary", async () => {
  const temporaryRoot = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-bootstrap-test-")
  );
  try {
    await assert.rejects(
      installCore({
        version: "0.2.0",
        dataRoot: temporaryRoot,
        fetchImpl: async () => {
          throw new Error("offline");
        }
      }),
      /offline/
    );
    await assert.rejects(
      installCore({
        version: "0.2.0",
        dataRoot: temporaryRoot,
        fetchImpl: async () => ({ ok: false, status: 404 })
      }),
      /Download failed \(404\)/
    );
    await assert.rejects(readFile(coreInstallPath({
      version: "0.2.0",
      dataRoot: temporaryRoot
    })), /ENOENT/);
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
});

test("uses an explicit token only as an Authorization header", async () => {
  const binary = Buffer.from("authenticated-binary");
  const expected = sha256(binary);
  const requests = [];
  const checksumsAssetURL =
    "https://api.github.com/repos/liming0791/agentbell/releases/assets/1";
  const binaryAssetURL =
    "https://api.github.com/repos/liming0791/agentbell/releases/assets/2";
  const temporaryRoot = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-bootstrap-test-")
  );
  try {
    const result = await installCore({
      version: "0.2.0",
      dataRoot: temporaryRoot,
      token: "private-token",
      fetchImpl: async (url, options) => {
        requests.push({ url, options });
        if (url.endsWith("/releases/tags/v0.2.0")) {
          return {
            ok: true,
            json: async () => ({
              assets: [
                { name: "checksums.txt", url: checksumsAssetURL },
                {
                  name: "agentbell-windows-amd64.exe",
                  url: binaryAssetURL
                }
              ]
            })
          };
        }
        if (url === checksumsAssetURL) {
          return {
            ok: true,
            text: async () => `${expected}  agentbell-windows-amd64.exe\n`
          };
        }
        return {
          ok: true,
          arrayBuffer: async () => binary
        };
      },
      platform: "win32",
      architecture: "x64"
    });
    assert.equal(result.reused, false);
    assert.equal(requests.length, 3);
    assert.ok(requests.every(
      ({ url, options }) =>
        !url.includes("private-token") &&
        options.headers.authorization === "Bearer private-token"
    ));
    assert.equal(
      requests[0].options.headers.accept,
      "application/vnd.github+json"
    );
    assert.ok(requests.slice(1).every(
      ({ options }) => options.headers.accept === "application/octet-stream"
    ));
    const metadata = JSON.parse(
      await readFile(path.join(path.dirname(result.path), "install.json"), "utf8")
    );
    assert.equal(metadata.signatureStatus, "technical-preview");
    assert.equal(metadata.checksum, expected);
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
});

test("authenticated installs can resolve a draft release by tag", async () => {
  const binary = Buffer.from("draft-release-binary");
  const expected = sha256(binary);
  const checksumsAssetURL =
    "https://api.github.com/repos/liming0791/agentbell/releases/assets/11";
  const binaryAssetURL =
    "https://api.github.com/repos/liming0791/agentbell/releases/assets/12";
  const requests = [];
  const temporaryRoot = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-bootstrap-test-")
  );
  try {
    const result = await installCore({
      version: "0.3.0-rc.2",
      dataRoot: temporaryRoot,
      token: "draft-token",
      fetchImpl: async (url, options) => {
        requests.push({ url, options });
        if (url.endsWith("/releases/tags/v0.3.0-rc.2")) {
          return new WebResponse("missing", { status: 404 });
        }
        if (url.endsWith("/releases?per_page=100")) {
          return WebResponse.json([
            {
              tag_name: "v0.3.0-rc.2",
              draft: true,
              assets: [
                { name: "checksums.txt", url: checksumsAssetURL },
                {
                  name: "agentbell-linux-amd64",
                  url: binaryAssetURL
                }
              ]
            }
          ]);
        }
        if (url === checksumsAssetURL) {
          return new WebResponse(
            `${expected}  agentbell-linux-amd64\n`
          );
        }
        if (url === binaryAssetURL) {
          return new WebResponse(binary);
        }
        return new WebResponse("unexpected", { status: 500 });
      },
      platform: "linux",
      architecture: "x64"
    });

    assert.equal(result.reused, false);
    assert.equal(requests.length, 4);
    assert.match(requests[0].url, /releases\/tags\/v0\.3\.0-rc\.2$/);
    assert.match(requests[1].url, /releases\?per_page=100$/);
    assert.ok(requests.every(
      ({ options }) =>
        options.headers.authorization === "Bearer draft-token"
    ));
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
});

test("rejects invalid versions and incomplete checksum manifests", async () => {
  await assert.rejects(
    installCore({ version: "../bad" }),
    /Invalid AgentBell Core version/
  );
  await assert.rejects(
    installCore({
      version: "0.2.0",
      fetchImpl: null
    }),
    /does not provide fetch/
  );
  await assert.rejects(
    installCore({
      version: "0.2.0",
      fetchImpl: async () => ({
        ok: true,
        text: async () => ""
      })
    }),
    /does not contain/
  );
});

test("removes only the requested managed Core version", async (context) => {
  const temporaryRoot = await mkdtemp(
    path.join(os.tmpdir(), "agentbell-uninstall-test-")
  );
  context.after(async () => {
    await rm(temporaryRoot, { recursive: true, force: true });
  });
  const installPath = coreInstallPath({
    version: "0.2.0",
    dataRoot: temporaryRoot
  });
  const retainedPath = path.join(temporaryRoot, "retained.txt");
  const activePath = activeStatePath(temporaryRoot);
  const bridgePath = stableBridgePath({ dataRoot: temporaryRoot });
  await mkdir(path.dirname(installPath), { recursive: true });
  await mkdir(path.dirname(bridgePath), { recursive: true });
  await writeFile(installPath, "fake-core");
  await writeFile(bridgePath, "fake-bridge");
  await writeFile(activePath, JSON.stringify({
    schemaVersion: 1,
    generation: 4,
    activeVersion: "0.2.0",
    target: resolveTarget(process.platform, process.arch).id,
    checksum: sha256("fake-core"),
    bridgeChecksum: sha256("fake-bridge"),
    transactionId: "uninstall-test"
  }));
  await writeFile(retainedPath, "keep");

  const result = await uninstallCore({
    version: "0.2.0",
    dataRoot: temporaryRoot
  });
  assert.equal(result.removed, true);
  await assert.rejects(readFile(installPath), /ENOENT/);
  await assert.rejects(readFile(activePath), /ENOENT/);
  await assert.rejects(readFile(bridgePath), /ENOENT/);
  assert.equal(await readFile(retainedPath, "utf8"), "keep");

  const repeated = await uninstallCore({
    version: "0.2.0",
    dataRoot: temporaryRoot
  });
  assert.equal(repeated.removed, false);
  await assert.rejects(
    uninstallCore({ version: "../bad", dataRoot: temporaryRoot }),
    /Invalid AgentBell Core version/
  );
});

test("runs Core without a shell and reports spawn failures", async () => {
  assert.equal(await runCore(
    process.execPath,
    ["-e", "process.exit(0)"],
    { stdin: "ignore", stdout: "ignore", stderr: "ignore" }
  ), 0);
  assert.equal(await runCore(
    process.execPath,
    ["-e", "process.exit(7)"],
    { stdin: "ignore", stdout: "ignore", stderr: "ignore" }
  ), 7);
  await assert.rejects(
    runCore(
      "agentbell-bootstrap-command-does-not-exist",
      [],
      { stdin: "ignore", stdout: "ignore", stderr: "ignore" }
    ),
    /ENOENT/
  );
});

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import {
  lstat,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  symlink,
  utimes,
  writeFile
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";

import {
  activeStatePath,
  listVersions,
  resolveActiveCore,
  rollback,
  stableBridgePath,
  upgrade
} from "../packages/cli/src/upgrade.mjs";
import { resolveTarget } from "../packages/cli/src/platform.mjs";

const WebResponse = globalThis.Response;
const WebURL = globalThis.URL;

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function releaseBundle(version) {
  const core = Buffer.from(`core-${version}`);
  const bridge = Buffer.from(`bridge-${version}`);
  return {
    core,
    bridge,
    coreChecksum: sha256(core),
    bridgeChecksum: sha256(bridge),
    signatureStatus: "technical-preview",
    manifest: {
      schemaVersion: 1,
      version,
      signatureStatus: "technical-preview"
    }
  };
}

function compatibleSettings(minCoreVersion = "0.3.0") {
  return {
    version: 1,
    minCoreVersion,
    events: {},
    defaultTemplate: "standard",
    templates: [],
    quietHours: {},
    policies: []
  };
}

function compatibleRemote(minCoreVersion = "0.3.0") {
  return {
    version: 1,
    minCoreVersion,
    teamId: "team-main",
    originId: "origin-main",
    runtime: "ssh",
    outbox: {
      path: { platform: "linux", value: "/tmp/agentbell-outbox" },
      maxBytes: 1048576
    },
    connector: {
      type: "https",
      https: { endpoint: "https://relay.example.test/v1/events" }
    },
    privateKeyRef: {
      store: "secret-service",
      id: "agentbell/key"
    }
  };
}

function compatibleRelay(minCoreVersion = "0.3.0") {
  return {
    version: 1,
    minCoreVersion,
    listener: { enabled: false },
    peers: []
  };
}

function compatibleHostConnectors(minCoreVersion = "0.3.0") {
  return {
    version: 1,
    minCoreVersion,
    connectors: [
      {
        id: "wsl-ubuntu",
        teamId: "team-main",
        originId: "origin-wsl",
        runtime: "wsl",
        connector: {
          type: "wsl",
          wsl: {
            distribution: "Ubuntu",
            hostExecutable: {
              platform: "windows",
              value: "C:\\Windows\\System32\\wsl.exe"
            },
            remoteExecutable: {
              platform: "linux",
              value: "/usr/local/bin/agentbell"
            }
          }
        }
      }
    ]
  };
}

async function writeJSON(filePath, value) {
  await mkdir(path.dirname(filePath), { recursive: true });
  await writeFile(filePath, `${JSON.stringify(value, null, 2)}\n`);
}

async function writeLegacyInstall(
  dataRoot,
  version,
  { platform = "linux", architecture = "x64" } = {}
) {
  const target = resolveTarget(platform, architecture);
  const core = Buffer.from(`legacy-core-${version}`);
  const directory = path.join(dataRoot, "bin", version);
  const corePath = path.join(
    directory,
    platform === "win32" ? "agentbell.exe" : "agentbell"
  );
  await mkdir(directory, { recursive: true, mode: 0o700 });
  await writeFile(corePath, core, { mode: 0o700 });
  await writeJSON(path.join(directory, "install.json"), {
    version,
    target: target.id,
    fileName: target.fileName,
    checksum: sha256(core),
    installedAt: new Date(0).toISOString(),
    signatureStatus: "technical-preview"
  });
  return { core, corePath, target };
}

async function journalValues(dataRoot) {
  const directory = path.join(dataRoot, "bin", "transactions");
  const names = await readdir(directory);
  return Promise.all(names
    .filter((name) => name.endsWith(".json"))
    .map(async (name) => JSON.parse(await readFile(
      path.join(directory, name),
      "utf8"
    ))));
}

function releaseResponses(version, target, bundle = releaseBundle(version)) {
  const checksums = [
    `${bundle.coreChecksum}  ${target.fileName}`,
    `${bundle.bridgeChecksum}  ${target.bridgeFileName}`
  ].join("\n");
  const manifest = {
    schemaVersion: 1,
    version,
    signatureStatus: "technical-preview",
    artifacts: [
      { fileName: target.fileName, sha256: bundle.coreChecksum },
      { fileName: target.bridgeFileName, sha256: bundle.bridgeChecksum }
    ]
  };
  return new Map([
    ["checksums.txt", new WebResponse(checksums)],
    ["release-manifest.json", WebResponse.json(manifest)],
    [target.fileName, new WebResponse(bundle.core)],
    [target.bridgeFileName, new WebResponse(bundle.bridge)]
  ]);
}

async function temporaryDataRoot(context) {
  const root = await mkdtemp(path.join(os.tmpdir(), "agentbell-upgrade-test-"));
  context.after(async () => {
    await rm(root, { recursive: true, force: true });
  });
  return root;
}

test("upgrade atomically installs Core, stable bridge and active state", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const smokeCalls = [];
  const result = await upgrade({
    toVersion: "0.3.0",
    dataRoot,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    smokeCore: async (request) => {
      smokeCalls.push(request);
    },
    restartService: async () => {}
  });

  assert.equal(result.activeVersion, "0.3.0");
  assert.equal(result.previousVersion, "");
  assert.equal(result.generation, 1);
  assert.equal(result.rolledBack, false);
  assert.equal(smokeCalls.length, 1);
  assert.deepEqual(
    await readFile(path.join(dataRoot, "bin", "0.3.0", "agentbell")),
    Buffer.from("core-0.3.0")
  );
  assert.deepEqual(
    await readFile(stableBridgePath({
      dataRoot,
      platform: "linux",
      architecture: "x64"
    })),
    Buffer.from("bridge-0.3.0")
  );

  const active = JSON.parse(await readFile(activeStatePath(dataRoot), "utf8"));
  assert.deepEqual(active, {
    schemaVersion: 1,
    generation: 1,
    activeVersion: "0.3.0",
    target: "linux-amd64",
    checksum: sha256(Buffer.from("core-0.3.0")),
    bridgeChecksum: sha256(Buffer.from("bridge-0.3.0")),
    transactionId: result.transactionId
  });
  const journal = JSON.parse(await readFile(
    path.join(dataRoot, "bin", "transactions", `${result.transactionId}.json`),
    "utf8"
  ));
  assert.equal(journal.status, "committed");
  assert.equal(journal.operation, "upgrade");
});

test("upgrade adopts one real M1 install and preserves rollback", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const legacy = await writeLegacyInstall(dataRoot, "0.2.0-rc.3");
  const dependencies = {
    dataRoot,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    smokeCore: async () => {},
    restartService: async () => {}
  };

  const dryRun = await upgrade({
    ...dependencies,
    toVersion: "0.3.0-rc.1",
    dryRun: true
  });
  assert.equal(dryRun.fromVersion, "0.2.0-rc.3");
  assert.equal(dryRun.preflight.outcome, "passed");

  const upgraded = await upgrade({
    ...dependencies,
    toVersion: "0.3.0-rc.1"
  });
  assert.equal(upgraded.previousVersion, "0.2.0-rc.3");
  assert.equal(upgraded.generation, 2);
  const bridge = await readFile(stableBridgePath(dependencies));
  const activeAfterUpgrade = JSON.parse(
    await readFile(activeStatePath(dataRoot), "utf8")
  );
  assert.equal(activeAfterUpgrade.previousVersion, "0.2.0-rc.3");
  assert.equal(activeAfterUpgrade.bridgeChecksum, sha256(bridge));

  const versions = await listVersions(dependencies);
  assert.deepEqual(
    versions.installed.map(({ version, invalid = false }) => ({
      version,
      invalid
    })),
    [
      { version: "0.2.0-rc.3", invalid: false },
      { version: "0.3.0-rc.1", invalid: false }
    ]
  );

  const rolledBack = await rollback(dependencies);
  assert.equal(rolledBack.activeVersion, "0.2.0-rc.3");
  assert.equal(rolledBack.previousVersion, "0.3.0-rc.1");
  assert.equal(rolledBack.generation, 3);
  assert.deepEqual(await readFile(stableBridgePath(dependencies)), bridge);
  const activeAfterRollback = JSON.parse(
    await readFile(activeStatePath(dataRoot), "utf8")
  );
  assert.equal(activeAfterRollback.checksum, sha256(legacy.core));
  assert.equal(activeAfterRollback.bridgeChecksum, sha256(bridge));
});

test("legacy migration requires --from when more than one version exists", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  await writeLegacyInstall(dataRoot, "0.2.0-rc.2");
  await writeLegacyInstall(dataRoot, "0.2.0-rc.3");
  const dependencies = {
    dataRoot,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    smokeCore: async () => {},
    restartService: async () => {}
  };

  await assert.rejects(
    upgrade({
      ...dependencies,
      toVersion: "0.3.0-rc.1",
      dryRun: true
    }),
    /Multiple legacy AgentBell versions.*--from/
  );
  const plan = await upgrade({
    ...dependencies,
    fromVersion: "0.2.0-rc.3",
    toVersion: "0.3.0-rc.1",
    dryRun: true
  });
  assert.equal(plan.fromVersion, "0.2.0-rc.3");
  await assert.rejects(
    upgrade({
      ...dependencies,
      fromVersion: "0.3.0-rc.1",
      toVersion: "0.3.0-rc.1",
      dryRun: true
    }),
    /--from must differ from --to/
  );
});

test("failed legacy service migration restores legacy mode and Core", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const legacy = await writeLegacyInstall(dataRoot, "0.2.0-rc.3");
  const restarts = [];
  await assert.rejects(
    upgrade({
      toVersion: "0.3.0-rc.1",
      dataRoot,
      platform: "linux",
      architecture: "x64",
      downloadBundle: async ({ version }) => releaseBundle(version),
      smokeCore: async () => {},
      restartService: async (request) => {
        restarts.push(request);
        if (!request.compensation) {
          throw new Error("new service failed after legacy migration");
        }
      }
    }),
    /new service failed after legacy migration/
  );

  assert.equal(restarts.length, 2);
  assert.equal(restarts[0].active.activeVersion, "0.3.0-rc.1");
  assert.equal(restarts[1].corePath, legacy.corePath);
  assert.equal(restarts[1].active, null);
  await assert.rejects(readFile(activeStatePath(dataRoot)), /ENOENT/);
  await assert.rejects(
    lstat(path.join(dataRoot, "bin", "0.3.0-rc.1")),
    /ENOENT/
  );
  assert.deepEqual(await readFile(legacy.corePath), legacy.core);
});

test("upgrade preserves previous and automatically rolls back service failure", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const dependencies = {
    dataRoot,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    smokeCore: async () => {},
    restartService: async () => {}
  };
  await upgrade({ ...dependencies, toVersion: "0.3.0" });
  const oldBridge = await readFile(stableBridgePath(dependencies));

  await assert.rejects(
    upgrade({
      ...dependencies,
      toVersion: "0.3.1",
      restartService: async () => {
        throw new Error("service did not become healthy");
      }
    }),
    /service did not become healthy/
  );

  const active = JSON.parse(await readFile(activeStatePath(dataRoot), "utf8"));
  assert.equal(active.activeVersion, "0.3.0");
  assert.equal(active.generation, 3);
  assert.deepEqual(await readFile(stableBridgePath(dependencies)), oldBridge);
  const transactions = await readdir(path.join(dataRoot, "bin", "transactions"));
  const journals = await Promise.all(
    transactions
      .filter((name) => name.endsWith(".json"))
      .map(async (name) => JSON.parse(await readFile(
        path.join(dataRoot, "bin", "transactions", name),
        "utf8"
      )))
  );
  assert.ok(journals.some(
    (journal) =>
      journal.toVersion === "0.3.1" &&
      journal.status === "compensation-failed" &&
      journal.compensation.status === "failed" &&
      journal.error.includes("service did not become healthy")
  ));
});

test("smoke failure does not switch active state", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const base = {
    dataRoot,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    restartService: async () => {}
  };
  await upgrade({ ...base, toVersion: "0.3.0", smokeCore: async () => {} });
  const before = await readFile(activeStatePath(dataRoot));
  await assert.rejects(
    upgrade({
      ...base,
      toVersion: "0.3.1",
      smokeCore: async () => {
        throw new Error("smoke failed");
      }
    }),
    /smoke failed/
  );
  assert.deepEqual(await readFile(activeStatePath(dataRoot)), before);
  await assert.rejects(
    lstat(path.join(dataRoot, "bin", "0.3.1")),
    /ENOENT/
  );
});

test("active pointer failure restores the stable bridge and staged version", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const base = {
    dataRoot,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    smokeCore: async () => {},
    restartService: async () => {}
  };
  await upgrade({ ...base, toVersion: "0.3.0" });
  const beforeActive = await readFile(activeStatePath(dataRoot));
  const beforeBridge = await readFile(stableBridgePath(base));
  await assert.rejects(
    upgrade({
      ...base,
      toVersion: "0.3.1",
      writeActive: async () => {
        throw new Error("active pointer fault");
      }
    }),
    /active pointer fault/
  );
  assert.deepEqual(await readFile(activeStatePath(dataRoot)), beforeActive);
  assert.deepEqual(await readFile(stableBridgePath(base)), beforeBridge);
  await assert.rejects(
    lstat(path.join(dataRoot, "bin", "0.3.1")),
    /ENOENT/
  );
});

test("rollback selects an installed version and increments generation", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const dependencies = {
    dataRoot,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    smokeCore: async () => {},
    restartService: async () => {}
  };
  await upgrade({ ...dependencies, toVersion: "0.3.0" });
  await upgrade({ ...dependencies, toVersion: "0.3.1" });
  const result = await rollback({ ...dependencies, toVersion: "0.3.0" });
  assert.equal(result.activeVersion, "0.3.0");
  assert.equal(result.previousVersion, "0.3.1");
  assert.equal(result.generation, 3);

  const versions = await listVersions(dependencies);
  assert.equal(versions.activeVersion, "0.3.0");
  assert.equal(versions.previousVersion, "0.3.1");
  assert.deepEqual(
    versions.installed.map(({ version }) => version),
    ["0.3.0", "0.3.1"]
  );
  await assert.rejects(
    rollback({ ...dependencies, toVersion: "9.9.9" }),
    /is not installed/
  );
});

test("upgrade dry-run is read-only and rejects unsafe roots or bundles", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  let downloads = 0;
  const result = await upgrade({
    toVersion: "0.3.0",
    dataRoot,
    platform: "linux",
    architecture: "x64",
    dryRun: true,
    downloadBundle: async () => {
      downloads++;
      return releaseBundle("0.3.0");
    }
  });
  assert.equal(result.dryRun, true);
  assert.equal(downloads, 0);
  await assert.rejects(lstat(path.join(dataRoot, "bin")), /ENOENT/);

  await assert.rejects(
    upgrade({
      toVersion: "../bad",
      dataRoot,
      downloadBundle: async () => releaseBundle("0.3.0")
    }),
    /Invalid AgentBell version/
  );
  await assert.rejects(
    upgrade({
      toVersion: "0.3.0",
      dataRoot,
      platform: "linux",
      architecture: "x64",
      downloadBundle: async () => ({
        ...releaseBundle("0.3.0"),
        coreChecksum: "0".repeat(64)
      }),
      smokeCore: async () => {}
    }),
    /Core SHA-256 mismatch/
  );

  const target = path.join(dataRoot, "real");
  const linked = path.join(dataRoot, "linked");
  await mkdir(target);
  await symlink(target, linked, "dir");
  await assert.rejects(
    upgrade({
      toVersion: "0.3.0",
      dataRoot: linked,
      platform: "linux",
      architecture: "x64",
      downloadBundle: async () => releaseBundle("0.3.0")
    }),
    /data root must not be a symbolic link/
  );
});

test("default downloader verifies direct release assets and authorization", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const version = "0.3.2";
  const target = resolveTarget("linux", "x64");
  const responses = releaseResponses(version, target);
  const requests = [];

  const result = await upgrade({
    toVersion: version,
    dataRoot,
    platform: "linux",
    architecture: "x64",
    releaseBase: "https://downloads.example.test/agentbell",
    token: "read-only-token",
    fetchImpl: async (url, options) => {
      requests.push({ url, options });
      const name = new WebURL(url).pathname.split("/").at(-1);
      const response = responses.get(name);
      return response
        ? response.clone()
        : new WebResponse("missing", { status: 404 });
    },
    smokeCore: async () => {},
    restartService: async () => {}
  });

  assert.equal(result.activeVersion, version);
  assert.deepEqual(
    requests.map(({ url }) => new WebURL(url).pathname),
    [
      `/agentbell/download/v${version}/checksums.txt`,
      `/agentbell/download/v${version}/release-manifest.json`,
      `/agentbell/download/v${version}/${target.fileName}`,
      `/agentbell/download/v${version}/${target.bridgeFileName}`
    ]
  );
  assert.ok(requests.every(
    ({ options }) => options.headers.authorization === "Bearer read-only-token"
  ));
});

test("default downloader supports private GitHub release asset endpoints", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const version = "0.3.3";
  const target = resolveTarget("linux", "x64");
  const responses = releaseResponses(version, target);
  const assets = [
    "checksums.txt",
    "release-manifest.json",
    target.fileName,
    target.bridgeFileName
  ].map((name) => ({
    name,
    url: `https://api.github.test/assets/${encodeURIComponent(name)}`
  }));
  const requests = [];

  await upgrade({
    toVersion: version,
    dataRoot,
    platform: "linux",
    architecture: "x64",
    releaseBase: "https://github.com/liming0791/agentbell/releases",
    token: "github-token",
    fetchImpl: async (url, options) => {
      requests.push({ url, options });
      if (url.endsWith(`/releases/tags/v${version}`)) {
        return WebResponse.json({ assets });
      }
      const name = decodeURIComponent(new WebURL(url).pathname.split("/").at(-1));
      const response = responses.get(name);
      return response
        ? response.clone()
        : new WebResponse("missing", { status: 404 });
    },
    smokeCore: async () => {},
    restartService: async () => {}
  });

  assert.match(requests[0].url, /api\.github\.com\/repos\/liming0791\/agentbell/);
  assert.equal(
    requests[0].options.headers.accept,
    "application/vnd.github+json"
  );
  assert.ok(requests.slice(1).every(
    ({ options }) => options.headers.accept === "application/octet-stream"
  ));
});

test("default downloader rejects missing, inconsistent and failed assets", async (context) => {
  const target = resolveTarget("linux", "x64");
  const base = {
    dataRoot: await temporaryDataRoot(context),
    platform: "linux",
    architecture: "x64",
    releaseBase: "https://downloads.example.test/releases",
    smokeCore: async () => {},
    restartService: async () => {}
  };

  await assert.rejects(
    upgrade({
      ...base,
      toVersion: "0.3.4",
      fetchImpl: async () => new WebResponse("unavailable", { status: 503 })
    }),
    /Download failed \(503\)/
  );

  const version = "0.3.5";
  const inconsistent = releaseResponses(version, target);
  inconsistent.set(
    "release-manifest.json",
    WebResponse.json({
      schemaVersion: 1,
      version,
      signatureStatus: "technical-preview",
      artifacts: []
    })
  );
  await assert.rejects(
    upgrade({
      ...base,
      toVersion: version,
      fetchImpl: async (url) => {
        const name = new WebURL(url).pathname.split("/").at(-1);
        return inconsistent.get(name).clone();
      }
    }),
    /does not consistently describe/
  );

  const githubVersion = "0.3.6";
  await assert.rejects(
    upgrade({
      ...base,
      toVersion: githubVersion,
      releaseBase: "https://github.com/liming0791/agentbell/releases",
      token: "token",
      fetchImpl: async () => WebResponse.json({ assets: [] })
    }),
    /does not contain checksums\.txt/
  );
});

test("upgrade reuses an active install and active resolution validates target", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  let downloads = 0;
  const dependencies = {
    dataRoot,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => {
      downloads++;
      return releaseBundle(version);
    },
    smokeCore: async () => {},
    restartService: async () => {}
  };
  const first = await upgrade({ ...dependencies, toVersion: "0.3.7" });
  const reused = await upgrade({ ...dependencies, toVersion: "0.3.7" });
  assert.equal(downloads, 1);
  assert.equal(reused.reused, true);
  assert.equal(reused.transactionId, first.transactionId);

  const active = await resolveActiveCore(dependencies);
  assert.equal(active.version, "0.3.7");
  assert.equal(active.checksum, sha256(Buffer.from("core-0.3.7")));
  await assert.rejects(
    resolveActiveCore({
      dataRoot,
      platform: "darwin",
      architecture: "x64"
    }),
    /does not match/
  );
});

test("rollback dry-run is read-only and service failure restores active state", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const dependencies = {
    dataRoot,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    smokeCore: async () => {},
    restartService: async () => {}
  };
  await upgrade({ ...dependencies, toVersion: "0.3.8" });
  await upgrade({ ...dependencies, toVersion: "0.3.9" });

  const plan = await rollback({ ...dependencies, dryRun: true });
  assert.deepEqual(
    {
      dryRun: plan.dryRun,
      fromVersion: plan.fromVersion,
      toVersion: plan.toVersion
    },
    { dryRun: true, fromVersion: "0.3.9", toVersion: "0.3.8" }
  );
  const before = JSON.parse(await readFile(activeStatePath(dataRoot), "utf8"));
  await assert.rejects(
    rollback({
      ...dependencies,
      restartService: async () => {
        throw new Error("restart rejected");
      }
    }),
    /restart rejected/
  );
  const restored = JSON.parse(await readFile(activeStatePath(dataRoot), "utf8"));
  assert.equal(restored.activeVersion, before.activeVersion);
  assert.equal(restored.generation, before.generation + 2);
  await assert.rejects(
    rollback({ ...dependencies, toVersion: restored.activeVersion }),
    /already active/
  );
});

test("version listing reports empty and invalid managed installs", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const empty = await listVersions({
    dataRoot,
    platform: "linux",
    architecture: "x64"
  });
  assert.deepEqual(empty.installed, []);

  const invalidRoot = path.join(dataRoot, "bin", "0.3.10");
  await mkdir(invalidRoot, { recursive: true });
  await writeFile(path.join(invalidRoot, "agentbell"), "tampered");
  const result = await listVersions({
    dataRoot,
    platform: "linux",
    architecture: "x64"
  });
  assert.equal(result.installed[0].version, "0.3.10");
  assert.equal(result.installed[0].invalid, true);
  assert.match(result.installed[0].error, /ENOENT|invalid/i);
});

test("upgrade recovers a stale transaction lock", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const lockPath = path.join(dataRoot, "bin", "upgrade.lock");
  await mkdir(lockPath, { recursive: true });
  const stale = new Date(Date.now() - 120_000);
  await utimes(lockPath, stale, stale);

  const result = await upgrade({
    toVersion: "0.3.11",
    dataRoot,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    smokeCore: async () => {},
    restartService: async () => {}
  });
  assert.equal(result.activeVersion, "0.3.11");
  await assert.rejects(lstat(lockPath), /ENOENT/);
});

test("rollback preflight enforces sidecar semver and journals rejection", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const configDir = path.join(dataRoot, "config");
  const stateDir = path.join(dataRoot, "state");
  const dependencies = {
    dataRoot,
    configDir,
    stateDir,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    smokeCore: async () => {},
    restartService: async () => {}
  };
  await upgrade({ ...dependencies, toVersion: "0.3.0" });
  await upgrade({ ...dependencies, toVersion: "0.4.0" });
  await writeJSON(
    path.join(configDir, "settings.json"),
    compatibleSettings("v0.4.0")
  );

  await assert.rejects(
    rollback({ ...dependencies, toVersion: "0.3.0" }),
    /settings\.json requires AgentBell Core v?0\.4\.0.*0\.3\.0/i
  );
  assert.equal(
    JSON.parse(await readFile(activeStatePath(dataRoot), "utf8")).activeVersion,
    "0.4.0"
  );
  const journals = await journalValues(dataRoot);
  const rejected = journals.find(
    (journal) => journal.operation === "rollback" &&
      journal.toVersion === "0.3.0"
  );
  assert.equal(rejected.status, "preflight-rejected");
  assert.equal(rejected.preflight.outcome, "rejected");
  assert.equal(rejected.preflight.sidecars[0].name, "settings.json");
  assert.equal(rejected.preflight.sidecars[0].minCoreVersion, "0.4.0");
  assert.equal("path" in rejected.preflight.sidecars[0], false);
});

test("rollback preflight rejects unknown or damaged sidecars", async (context) => {
  for (const [name, body, pattern] of [
    [
      "unknown",
      JSON.stringify({ ...compatibleSettings(), execute: "env" }),
      /unknown field execute/i
    ],
    ["damaged", `{"version":1,"minCoreVersion":`, /parse settings\.json/i],
    [
      "invalid semver",
      JSON.stringify(compatibleSettings("latest")),
      /semantic version/i
    ]
  ]) {
    await context.test(name, async () => {
      const dataRoot = await temporaryDataRoot(context);
      const configDir = path.join(dataRoot, "config");
      const dependencies = {
        dataRoot,
        configDir,
        stateDir: path.join(dataRoot, "state"),
        platform: "linux",
        architecture: "x64",
        downloadBundle: async ({ version }) => releaseBundle(version),
        smokeCore: async () => {},
        restartService: async () => {}
      };
      await upgrade({ ...dependencies, toVersion: "0.3.0" });
      await upgrade({ ...dependencies, toVersion: "0.4.0" });
      await mkdir(configDir, { recursive: true });
      await writeFile(path.join(configDir, "settings.json"), body);
      await assert.rejects(
        rollback({ ...dependencies, toVersion: "0.3.0" }),
        pattern
      );
    });
  }
});

test("host connector rollback preflight rejects remote-owned state", async (context) => {
  for (const [name, mutate, pattern] of [
    [
      "outbox",
      (entry) => {
        entry.outbox = { path: "/tmp/outbox" };
      },
      /unknown field outbox/i
    ],
    [
      "push connector",
      (entry) => {
        entry.runtime = "https";
        entry.connector = {
          type: "https",
          https: { endpoint: "https://relay.example.test/v1/events" }
        };
      },
      /unknown field https|supported host runtime/i
    ]
  ]) {
    await context.test(name, async () => {
      const dataRoot = await temporaryDataRoot(context);
      const configDir = path.join(dataRoot, "config");
      const dependencies = {
        dataRoot,
        configDir,
        stateDir: path.join(dataRoot, "state"),
        platform: "linux",
        architecture: "x64",
        downloadBundle: async ({ version }) => releaseBundle(version),
        smokeCore: async () => {},
        restartService: async () => {}
      };
      await upgrade({ ...dependencies, toVersion: "0.3.0" });
      await upgrade({ ...dependencies, toVersion: "0.4.0" });
      const registry = compatibleHostConnectors();
      mutate(registry.connectors[0]);
      await writeJSON(
        path.join(configDir, "host-connectors.json"),
        registry
      );
      await assert.rejects(
        rollback({ ...dependencies, toVersion: "0.3.0" }),
        pattern
      );
    });
  }
});

test("rollback refuses a partially successful delivery ledger", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const stateDir = path.join(dataRoot, "state");
  const dependencies = {
    dataRoot,
    configDir: path.join(dataRoot, "config"),
    stateDir,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    smokeCore: async () => {},
    restartService: async () => {}
  };
  await upgrade({ ...dependencies, toVersion: "0.3.0" });
  await upgrade({ ...dependencies, toVersion: "0.4.0" });
  await writeJSON(
    path.join(stateDir, "queue", "pending", "partial.json"),
    {
      queueVersion: 1,
      id: "opaque-event-id",
      ledger: [
        {
          channelId: "channel-a",
          templateId: "standard",
          state: "succeeded",
          attempts: 1
        },
        {
          channelId: "channel-b",
          templateId: "standard",
          state: "pending",
          attempts: 1
        }
      ]
    }
  );

  await assert.rejects(
    rollback({ ...dependencies, toVersion: "0.3.0" }),
    /partially successful delivery ledger/i
  );
  const journals = await journalValues(dataRoot);
  const rejected = journals.find(
    (journal) => journal.status === "preflight-rejected"
  );
  assert.equal(rejected.preflight.ledger.hasPartialSuccess, true);
  assert.equal(rejected.preflight.ledger.partialItems, 1);
  assert.equal(JSON.stringify(rejected).includes("opaque-event-id"), false);
});

test("rollback dry-run performs read-only M1-compatible preflight", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const dependencies = {
    dataRoot,
    configDir: path.join(dataRoot, "config"),
    stateDir: path.join(dataRoot, "state"),
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    smokeCore: async () => {},
    restartService: async () => {}
  };
  await upgrade({ ...dependencies, toVersion: "0.3.0" });
  await upgrade({ ...dependencies, toVersion: "0.4.0" });
  const beforeActive = await readFile(activeStatePath(dataRoot));
  const beforeJournals = await readdir(
    path.join(dataRoot, "bin", "transactions")
  );

  const result = await rollback({
    ...dependencies,
    toVersion: "0.3.0",
    dryRun: true
  });

  assert.equal(result.preflight.outcome, "passed");
  assert.deepEqual(result.preflight.sidecars, [
    { name: "settings.json", status: "absent" },
    { name: "remote.json", status: "absent" },
    { name: "host-connectors.json", status: "absent" },
    { name: "relay.json", status: "absent" }
  ]);
  assert.equal(result.preflight.ledger.scannedItems, 0);
  assert.deepEqual(await readFile(activeStatePath(dataRoot)), beforeActive);
  assert.deepEqual(
    await readdir(path.join(dataRoot, "bin", "transactions")),
    beforeJournals
  );
  await assert.rejects(lstat(path.join(dataRoot, "state")), /ENOENT/);
  await assert.rejects(lstat(path.join(dataRoot, "config")), /ENOENT/);
});

test("upgrade service failure restores and restarts the previous service", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const configDir = path.join(dataRoot, "config");
  const stateDir = path.join(dataRoot, "state");
  const base = {
    dataRoot,
    configDir,
    stateDir,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    smokeCore: async () => {},
    restartService: async () => {}
  };
  await upgrade({ ...base, toVersion: "0.3.0" });
  await writeJSON(
    path.join(configDir, "settings.json"),
    compatibleSettings("0.3.0")
  );
  const restartCalls = [];

  await assert.rejects(
    upgrade({
      ...base,
      toVersion: "0.4.0",
      restartService: async (request) => {
        restartCalls.push(request);
        if (restartCalls.length === 1) {
          throw new Error("new service unhealthy");
        }
      }
    }),
    /new service unhealthy/
  );

  assert.equal(restartCalls.length, 2);
  assert.match(restartCalls[0].corePath, /0\.4\.0[\\/]agentbell$/);
  assert.match(restartCalls[1].corePath, /0\.3\.0[\\/]agentbell$/);
  assert.equal(restartCalls[1].active.activeVersion, "0.3.0");
  const journals = await journalValues(dataRoot);
  const failed = journals.find((journal) => journal.toVersion === "0.4.0");
  assert.equal(failed.status, "rolled-back");
  assert.equal(failed.preflight.outcome, "passed");
  assert.equal(failed.compensation.status, "completed");
  assert.deepEqual(
    failed.compensation.steps.map(({ action, status }) => [action, status]),
    [
      ["restore-bridge", "completed"],
      ["restore-active", "completed"],
      ["restart-previous-service", "completed"],
      ["remove-staged-version", "completed"]
    ]
  );
});

test("all M2 sidecars participate in semantic-version rollback checks", async (context) => {
  for (const [fileName, value] of [
    ["settings.json", compatibleSettings("0.3.0")],
    ["remote.json", compatibleRemote("0.3.0")],
    ["host-connectors.json", compatibleHostConnectors("0.3.0")],
    ["relay.json", compatibleRelay("0.3.0")]
  ]) {
    await context.test(fileName, async () => {
      const dataRoot = await temporaryDataRoot(context);
      const configDir = path.join(dataRoot, "config");
      const dependencies = {
        dataRoot,
        configDir,
        stateDir: path.join(dataRoot, "state"),
        platform: "linux",
        architecture: "x64",
        downloadBundle: async ({ version }) => releaseBundle(version),
        smokeCore: async () => {},
        restartService: async () => {}
      };
      await upgrade({ ...dependencies, toVersion: "0.3.0-rc.2" });
      await upgrade({ ...dependencies, toVersion: "0.3.0-rc.3" });
      await writeJSON(path.join(configDir, fileName), {
        ...value,
        minCoreVersion: "0.3.0-rc.2+build.9"
      });
      const safe = await rollback({ ...dependencies, dryRun: true });
      assert.equal(safe.preflight.outcome, "passed");

      await writeJSON(path.join(configDir, fileName), {
        ...value,
        minCoreVersion: "v0.3.0"
      });
      await assert.rejects(
        rollback({ ...dependencies, dryRun: true }),
        new RegExp(`${fileName.replace(".", "\\.")} requires.*0\\.3\\.0`)
      );
    });
  }
});

test("automatic rollback is blocked if restart creates incompatible state", async (context) => {
  const dataRoot = await temporaryDataRoot(context);
  const configDir = path.join(dataRoot, "config");
  const stateDir = path.join(dataRoot, "state");
  const base = {
    dataRoot,
    configDir,
    stateDir,
    platform: "linux",
    architecture: "x64",
    downloadBundle: async ({ version }) => releaseBundle(version),
    smokeCore: async () => {},
    restartService: async () => {}
  };
  await upgrade({ ...base, toVersion: "0.3.0" });
  let restarts = 0;

  await assert.rejects(
    upgrade({
      ...base,
      toVersion: "0.4.0",
      restartService: async () => {
        restarts++;
        await writeJSON(
          path.join(configDir, "settings.json"),
          compatibleSettings("0.4.0")
        );
        throw new Error("new service failed after migration");
      }
    }),
    /automatic rollback was blocked by preflight/i
  );

  assert.equal(restarts, 1);
  assert.equal(
    JSON.parse(await readFile(activeStatePath(dataRoot), "utf8")).activeVersion,
    "0.4.0"
  );
  const journals = await journalValues(dataRoot);
  const blocked = journals.find((journal) => journal.toVersion === "0.4.0");
  assert.equal(blocked.status, "compensation-blocked");
  assert.equal(blocked.compensation.status, "blocked");
  assert.equal(blocked.compensationPreflight.outcome, "rejected");
});

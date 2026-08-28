import { spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import {
  chmod,
  lstat,
  mkdir,
  open,
  readFile,
  readdir,
  rename,
  rm,
  stat
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { URL } from "node:url";

import { resolveDataRoot, resolveTarget } from "./platform.mjs";
import { fetchGitHubReleaseMetadata } from "./github-release.mjs";

const defaultReleaseBase =
  "https://github.com/liming0791/agentbell/releases";
const githubAPIOrigin = "https://api.github.com";
const activeSchemaVersion = 1;
const journalSchemaVersion = 1;
const bridgeProtocolVersion = "v1";
const lockTimeoutMs = 10_000;
const lockStaleMs = 60_000;
const windowsServiceTaskName = "\\AgentBell\\AgentBell";
const maximumServiceLockBytes = 4096;
const versionPattern =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;
const checksumPattern = /^[a-f0-9]{64}$/;
const sidecarSemverPattern =
  /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;
const maximumSidecarBytes = 1 << 20;
const windowsFileBusyCodes = new Set(["EACCES", "EBUSY", "EPERM"]);
const queueStates = ["pending", "inflight", "history", "dead"];
const sidecarDefinitions = [
  {
    name: "settings.json",
    fields: [
      "version",
      "minCoreVersion",
      "events",
      "defaultTemplate",
      "templates",
      "quietHours",
      "policies"
    ]
  },
  {
    name: "remote.json",
    fields: [
      "version",
      "minCoreVersion",
      "teamId",
      "originId",
      "runtime",
      "outbox",
      "connector",
      "privateKeyRef"
    ]
  },
  {
    name: "host-connectors.json",
    fields: ["version", "minCoreVersion", "connectors"]
  },
  {
    name: "relay.json",
    fields: ["version", "minCoreVersion", "listener", "peers"]
  }
];

function checksum(value) {
  return createHash("sha256").update(value).digest("hex");
}

function assertVersion(version) {
  if (!versionPattern.test(version)) {
    throw new Error(`Invalid AgentBell version: ${version}`);
  }
}

function parseSemanticVersion(value, label) {
  if (typeof value !== "string") {
    throw new Error(`${label} must be a semantic version.`);
  }
  const match = sidecarSemverPattern.exec(value);
  if (!match) {
    throw new Error(`${label} must be a semantic version.`);
  }
  return {
    normalized: `${match[1]}.${match[2]}.${match[3]}` +
      (match[4] ? `-${match[4]}` : ""),
    core: [BigInt(match[1]), BigInt(match[2]), BigInt(match[3])],
    prerelease: match[4]?.split(".") || []
  };
}

function compareSemanticVersions(left, right) {
  const leftVersion = typeof left === "string"
    ? parseSemanticVersion(left, "version")
    : left;
  const rightVersion = typeof right === "string"
    ? parseSemanticVersion(right, "version")
    : right;
  for (let index = 0; index < 3; index++) {
    if (leftVersion.core[index] < rightVersion.core[index]) {
      return -1;
    }
    if (leftVersion.core[index] > rightVersion.core[index]) {
      return 1;
    }
  }
  if (leftVersion.prerelease.length === 0 ||
      rightVersion.prerelease.length === 0) {
    return leftVersion.prerelease.length === rightVersion.prerelease.length
      ? 0
      : leftVersion.prerelease.length === 0 ? 1 : -1;
  }
  const length = Math.max(
    leftVersion.prerelease.length,
    rightVersion.prerelease.length
  );
  for (let index = 0; index < length; index++) {
    const leftPart = leftVersion.prerelease[index];
    const rightPart = rightVersion.prerelease[index];
    if (leftPart === rightPart) {
      continue;
    }
    if (leftPart === undefined) {
      return -1;
    }
    if (rightPart === undefined) {
      return 1;
    }
    const leftNumeric = /^\d+$/.test(leftPart);
    const rightNumeric = /^\d+$/.test(rightPart);
    if (leftNumeric && rightNumeric) {
      const leftNumber = BigInt(leftPart);
      const rightNumber = BigInt(rightPart);
      return leftNumber < rightNumber ? -1 : 1;
    }
    if (leftNumeric !== rightNumeric) {
      return leftNumeric ? -1 : 1;
    }
    return leftPart < rightPart ? -1 : 1;
  }
  return 0;
}

function assertObject(value, label) {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${label} must be an object.`);
  }
  return value;
}

function assertKnownFields(value, allowed, label, required = []) {
  const object = assertObject(value, label);
  const known = new Set(allowed);
  for (const name of Object.keys(object)) {
    if (!known.has(name)) {
      throw new Error(`${label} has unknown field ${name}.`);
    }
  }
  for (const name of required) {
    if (!(name in object) || object[name] === null) {
      throw new Error(`${label} is missing required field ${name}.`);
    }
  }
  return object;
}

function assertObjectArray(value, label, callback) {
  if (!Array.isArray(value)) {
    throw new Error(`${label} must be an array.`);
  }
  value.forEach((entry, index) => callback(entry, `${label}[${index}]`));
}

function validatePathRefShape(value, label) {
  assertKnownFields(value, ["platform", "value"], label);
}

function validateSettingsShape(value) {
  assertKnownFields(
    value.events,
    [
      "task.completed",
      "task.failed",
      "agent.waiting",
      "approval.required",
      "session.interrupted",
      "subagent.completed",
      "agent.info"
    ],
    "settings.json events"
  );
  assertObjectArray(value.templates, "settings.json templates", (entry, label) => {
    assertKnownFields(entry, ["id", "body"], label);
  });
  const quietHours = assertKnownFields(
    value.quietHours,
    ["enabled", "timezone", "action", "intervals", "bypassEvents"],
    "settings.json quietHours"
  );
  if (quietHours.intervals !== undefined) {
    assertObjectArray(
      quietHours.intervals,
      "settings.json quietHours.intervals",
      (entry, label) => assertKnownFields(entry, ["days", "start", "end"], label)
    );
  }
  assertObjectArray(value.policies, "settings.json policies", (entry, label) => {
    const policy = assertKnownFields(entry, ["id", "match", "action"], label);
    if (policy.match !== undefined) {
      assertKnownFields(
        policy.match,
        ["events", "sources", "surfaces", "runtimes", "priorities", "projects"],
        `${label}.match`
      );
    }
    if (policy.action !== undefined) {
      assertKnownFields(
        policy.action,
        ["enabled", "channelIds", "templateId"],
        `${label}.action`
      );
    }
  });
}

function validateRemoteShape(value) {
  const outbox = assertKnownFields(
    value.outbox,
    ["path", "maxBytes"],
    "remote.json outbox"
  );
  if (outbox.path !== undefined) {
    validatePathRefShape(outbox.path, "remote.json outbox.path");
  }
  const connector = assertKnownFields(
    value.connector,
    ["type", "wsl", "ssh", "container", "https", "vendorCloud"],
    "remote.json connector"
  );
  const connectorShapes = {
    wsl: ["distribution", "hostExecutable", "remoteExecutable"],
    ssh: [
      "host",
      "port",
      "user",
      "hostExecutable",
      "knownHostsFile",
      "remoteExecutable"
    ],
    container: [
      "runtime",
      "hostExecutable",
      "containerId",
      "remoteExecutable"
    ],
    https: ["endpoint", "pinnedSpki"],
    vendorCloud: ["provider", "capability", "endpoint"]
  };
  for (const [arm, fields] of Object.entries(connectorShapes)) {
    if (connector[arm] === undefined) {
      continue;
    }
    const branch = assertKnownFields(
      connector[arm],
      fields,
      `remote.json connector.${arm}`
    );
    for (const field of [
      "hostExecutable",
      "knownHostsFile",
      "remoteExecutable"
    ]) {
      if (branch[field] !== undefined) {
        validatePathRefShape(
          branch[field],
          `remote.json connector.${arm}.${field}`
        );
      }
    }
  }
  const privateKeyRef = assertKnownFields(
    value.privateKeyRef,
    ["store", "id", "path", "fileFallbackAcknowledged"],
    "remote.json privateKeyRef"
  );
  if (privateKeyRef.path !== undefined) {
    validatePathRefShape(privateKeyRef.path, "remote.json privateKeyRef.path");
  }
}

function validateHostConnectorsShape(value) {
  const connectorShapes = {
    wsl: {
      fields: ["distribution", "hostExecutable", "remoteExecutable"],
      paths: ["hostExecutable", "remoteExecutable"]
    },
    ssh: {
      fields: [
        "host",
        "port",
        "user",
        "hostExecutable",
        "knownHostsFile",
        "remoteExecutable"
      ],
      paths: [
        "hostExecutable",
        "knownHostsFile",
        "remoteExecutable"
      ]
    },
    container: {
      fields: [
        "runtime",
        "hostExecutable",
        "containerId",
        "remoteExecutable"
      ],
      paths: ["hostExecutable", "remoteExecutable"]
    }
  };
  assertObjectArray(
    value.connectors,
    "host-connectors.json connectors",
    (entry, label) => {
      const hostConnector = assertKnownFields(
        entry,
        ["id", "teamId", "originId", "runtime", "connector"],
        label,
        ["id", "teamId", "originId", "runtime", "connector"]
      );
      const connector = assertKnownFields(
        hostConnector.connector,
        ["type", "wsl", "ssh", "container"],
        `${label}.connector`,
        ["type"]
      );
      const shape = connectorShapes[hostConnector.runtime];
      if (!shape || connector.type !== hostConnector.runtime) {
        throw new Error(
          `${label}.connector type must match a supported host runtime.`
        );
      }
      for (const arm of Object.keys(connectorShapes)) {
        if (arm !== connector.type && connector[arm] !== undefined) {
          throw new Error(`${label}.connector has unexpected ${arm} arm.`);
        }
      }
      const branch = assertKnownFields(
        connector[connector.type],
        shape.fields,
        `${label}.connector.${connector.type}`,
        shape.fields
      );
      for (const field of shape.paths) {
        const pathRef = assertKnownFields(
          branch[field],
          ["platform", "value"],
          `${label}.connector.${connector.type}.${field}`,
          ["platform", "value"]
        );
        if (typeof pathRef.platform !== "string" ||
            typeof pathRef.value !== "string" ||
            pathRef.platform.length === 0 ||
            pathRef.value.length === 0) {
          throw new Error(
            `${label}.connector.${connector.type}.${field} must be a path reference.`
          );
        }
      }
    }
  );
}

function validateRelayShape(value) {
  const listener = assertKnownFields(
    value.listener,
    ["enabled", "address", "tls", "sshTunnel"],
    "relay.json listener"
  );
  if (listener.tls !== undefined) {
    const tls = assertKnownFields(
      listener.tls,
      ["certFile", "keyFile"],
      "relay.json listener.tls"
    );
    if (tls.certFile !== undefined) {
      validatePathRefShape(tls.certFile, "relay.json listener.tls.certFile");
    }
    if (tls.keyFile !== undefined) {
      validatePathRefShape(tls.keyFile, "relay.json listener.tls.keyFile");
    }
  }
  assertObjectArray(value.peers, "relay.json peers", (entry, label) => {
    assertKnownFields(
      entry,
      [
        "id",
        "teamId",
        "originId",
        "publicKey",
        "scopes",
        "allowedSources",
        "allowedRuntimes",
        "revoked"
      ],
      label
    );
  });
}

function rejectDuplicateJSONKeys(raw, label) {
  let index = 0;
  const whitespace = () => {
    while (/\s/.test(raw[index] || "")) {
      index++;
    }
  };
  const parseString = () => {
    const start = index++;
    while (index < raw.length) {
      if (raw[index] === "\\") {
        index += raw[index + 1] === "u" ? 6 : 2;
        continue;
      }
      if (raw[index++] === "\"") {
        return JSON.parse(raw.slice(start, index));
      }
    }
    throw new Error(`parse ${label}: unterminated JSON string.`);
  };
  const parseValue = () => {
    whitespace();
    if (raw[index] === "{") {
      index++;
      whitespace();
      const fields = new Set();
      while (raw[index] !== "}") {
        if (raw[index] !== "\"") {
          throw new Error(`parse ${label}: invalid object key.`);
        }
        const name = parseString();
        if (fields.has(name)) {
          throw new Error(`parse ${label}: duplicate field ${name}.`);
        }
        fields.add(name);
        whitespace();
        if (raw[index++] !== ":") {
          throw new Error(`parse ${label}: missing object colon.`);
        }
        parseValue();
        whitespace();
        if (raw[index] === ",") {
          index++;
          whitespace();
          continue;
        }
        break;
      }
      index++;
      return;
    }
    if (raw[index] === "[") {
      index++;
      whitespace();
      while (raw[index] !== "]") {
        parseValue();
        whitespace();
        if (raw[index] === ",") {
          index++;
          whitespace();
          continue;
        }
        break;
      }
      index++;
      return;
    }
    if (raw[index] === "\"") {
      parseString();
      return;
    }
    const start = index;
    while (index < raw.length && !/[\s,\]}]/.test(raw[index])) {
      index++;
    }
    if (start === index) {
      throw new Error(`parse ${label}: invalid JSON value.`);
    }
  };
  parseValue();
  whitespace();
  if (index !== raw.length) {
    throw new Error(`parse ${label}: trailing JSON data.`);
  }
}

function assertManagedRoot(dataRoot) {
  const resolved = path.resolve(dataRoot);
  if (resolved === path.parse(resolved).root) {
    throw new Error("AgentBell data root cannot be a filesystem root.");
  }
  return resolved;
}

async function rejectSymlinkIfPresent(filePath, label) {
  try {
    const info = await lstat(filePath);
    if (info.isSymbolicLink()) {
      throw new Error(`${label} must not be a symbolic link: ${filePath}`);
    }
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
}

function executableName(platform) {
  return platform === "win32" ? "agentbell.exe" : "agentbell";
}

function bridgeName(platform) {
  return platform === "win32" ? "agentbell-bridge.exe" : "agentbell-bridge";
}

function serviceBridgeName(platform) {
  return platform === "win32" ? "agentbell-service.exe" : bridgeName(platform);
}

export function activeStatePath(dataRoot) {
  return path.join(assertManagedRoot(dataRoot), "bin", "active.json");
}

export function stableBridgePath({
  dataRoot,
  platform = process.platform
}) {
  return path.join(
    assertManagedRoot(dataRoot),
    "bin",
    "bridge",
    bridgeProtocolVersion,
    bridgeName(platform)
  );
}

export function stableServiceBridgePath({
  dataRoot,
  platform = process.platform
}) {
  return path.join(
    assertManagedRoot(dataRoot),
    "bin",
    "bridge",
    bridgeProtocolVersion,
    serviceBridgeName(platform)
  );
}

function installedCorePath(dataRoot, version, platform) {
  assertVersion(version);
  return path.join(
    assertManagedRoot(dataRoot),
    "bin",
    version,
    executableName(platform)
  );
}

function transactionDirectory(dataRoot) {
  return path.join(assertManagedRoot(dataRoot), "bin", "transactions");
}

function transactionPath(dataRoot, transactionId) {
  return path.join(transactionDirectory(dataRoot), `${transactionId}.json`);
}

async function syncDirectory(directory) {
  if (process.platform === "win32") {
    return;
  }
  const handle = await open(directory, "r");
  try {
    await handle.sync();
  } finally {
    await handle.close();
  }
}

async function atomicWrite(filePath, value, mode = 0o600) {
  const directory = path.dirname(filePath);
  await mkdir(directory, { recursive: true, mode: 0o700 });
  const temporaryPath = path.join(
    directory,
    `.${path.basename(filePath)}.${process.pid}.${randomBytes(8).toString("hex")}.tmp`
  );
  const handle = await open(temporaryPath, "wx", mode);
  try {
    await handle.writeFile(value);
    await handle.sync();
  } finally {
    await handle.close();
  }
  await chmod(temporaryPath, mode);
  const backupPath = `${temporaryPath}.previous`;
  try {
    try {
      await rename(temporaryPath, filePath);
    } catch (error) {
      if (!["EACCES", "EEXIST", "ENOTEMPTY", "EPERM"].includes(error?.code)) {
        throw error;
      }
      await renameAtomicEntry(filePath, backupPath);
      try {
        await rename(temporaryPath, filePath);
      } catch (replacementError) {
        await renameAtomicEntry(backupPath, filePath);
        throw replacementError;
      }
      await removeAtomicResidue(backupPath);
    }
    await syncDirectory(directory);
  } finally {
    await rm(temporaryPath, { force: true });
    await removeAtomicResidue(backupPath);
  }
}

async function renameAtomicEntry(
  source,
  destination,
  { platform = process.platform, attempts = 25 } = {}
) {
  for (let attempt = 0; attempt < attempts; attempt++) {
    try {
      await rename(source, destination);
      return;
    } catch (error) {
      if (platform !== "win32" || !windowsFileBusyCodes.has(error?.code) ||
          attempt === attempts - 1) {
        throw error;
      }
      await new Promise((resolve) => setTimeout(
        resolve,
        Math.min(40 * (attempt + 1), 200)
      ));
    }
  }
}

async function removeAtomicResidue(
  filePath,
  { platform = process.platform, attempts = 25 } = {}
) {
  for (let attempt = 0; attempt < attempts; attempt++) {
    try {
      await rm(filePath, { force: true });
      return;
    } catch (error) {
      if (platform !== "win32" || !windowsFileBusyCodes.has(error?.code) ||
          attempt === attempts - 1) {
        throw error;
      }
      await new Promise((resolve) => setTimeout(
        resolve,
        Math.min(40 * (attempt + 1), 200)
      ));
    }
  }
}

async function cleanupStableWriteResidues(filePath, platform) {
  const directory = path.dirname(filePath);
  const escapedName = path.basename(filePath).replace(
    /[.*+?^${}()|[\]\\]/g,
    "\\$&"
  );
  const residuePattern = new RegExp(
    `^\\.${escapedName}\\.\\d+\\.[a-f0-9]{16}\\.tmp\\.previous$`
  );
  let entries;
  try {
    entries = await readdir(directory, { withFileTypes: true });
  } catch (error) {
    if (error?.code === "ENOENT") {
      return;
    }
    throw error;
  }
  for (const entry of entries) {
    if (entry.isFile() && residuePattern.test(entry.name)) {
      await removeAtomicResidue(path.join(directory, entry.name), { platform });
    }
  }
}

async function atomicJSON(filePath, value) {
  await atomicWrite(
    filePath,
    `${JSON.stringify(value, null, 2)}\n`,
    0o600
  );
}

async function acquireLock(dataRoot) {
  const binRoot = path.join(assertManagedRoot(dataRoot), "bin");
  await mkdir(binRoot, { recursive: true, mode: 0o700 });
  const lockPath = path.join(binRoot, "upgrade.lock");
  const deadline = Date.now() + lockTimeoutMs;
  while (true) {
    try {
      await mkdir(lockPath, { mode: 0o700 });
      return async () => {
        await rm(lockPath, { recursive: true, force: true });
      };
    } catch (error) {
      if (error?.code !== "EEXIST") {
        throw error;
      }
      const info = await stat(lockPath);
      if (Date.now() - info.mtimeMs > lockStaleMs) {
        await rm(lockPath, { recursive: true, force: true });
        continue;
      }
      if (Date.now() >= deadline) {
        throw new Error(
          "Timed out waiting for the AgentBell upgrade lock.",
          { cause: error }
        );
      }
      await new Promise((resolve) => setTimeout(resolve, 20));
    }
  }
}

function validateActiveState(value) {
  const allowed = new Set([
    "schemaVersion",
    "generation",
    "activeVersion",
    "previousVersion",
    "serviceVersion",
    "target",
    "checksum",
    "serviceChecksum",
    "bridgeChecksum",
    "serviceBridgeChecksum",
    "transactionId"
  ]);
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error("Invalid AgentBell active state.");
  }
  for (const key of Object.keys(value)) {
    if (!allowed.has(key)) {
      throw new Error(`Unknown active-state field ${key}.`);
    }
  }
  if (value.schemaVersion !== activeSchemaVersion ||
      !Number.isSafeInteger(value.generation) ||
      value.generation < 1 ||
      !versionPattern.test(value.activeVersion) ||
      (value.previousVersion !== undefined &&
        !versionPattern.test(value.previousVersion)) ||
      ((value.serviceVersion === undefined) !==
        (value.serviceChecksum === undefined)) ||
      (value.serviceVersion !== undefined &&
        !versionPattern.test(value.serviceVersion)) ||
      (value.serviceChecksum !== undefined &&
        !checksumPattern.test(value.serviceChecksum)) ||
      typeof value.target !== "string" ||
      !checksumPattern.test(value.checksum) ||
      !checksumPattern.test(value.bridgeChecksum) ||
      (value.serviceBridgeChecksum !== undefined &&
        !checksumPattern.test(value.serviceBridgeChecksum)) ||
      typeof value.transactionId !== "string" ||
      value.transactionId.length === 0) {
    throw new Error("Invalid AgentBell active state.");
  }
  return value;
}

function supportsM2Runtime(version) {
  const match = /^(\d+)\.(\d+)\.\d+/.exec(version);
  if (!match) {
    return false;
  }
  return Number(match[1]) > 0 || Number(match[2]) >= 3;
}

async function loadActive(dataRoot, { optional = false } = {}) {
  try {
    return validateActiveState(JSON.parse(
      await readFile(activeStatePath(dataRoot), "utf8")
    ));
  } catch (error) {
    if (optional && error?.code === "ENOENT") {
      return null;
    }
    throw error;
  }
}

function newTransaction(operation, fromVersion, toVersion, target) {
  const now = new Date().toISOString();
  return {
    schemaVersion: journalSchemaVersion,
    id: `${Date.now()}-${randomBytes(12).toString("hex")}`,
    operation,
    status: "preparing",
    fromVersion: fromVersion || "",
    toVersion,
    target,
    startedAt: now,
    updatedAt: now
  };
}

async function writeJournal(dataRoot, transaction, status, error = "") {
  transaction.status = status;
  transaction.updatedAt = new Date().toISOString();
  if (error) {
    transaction.error = String(error).slice(0, 4096);
  } else {
    delete transaction.error;
  }
  await atomicJSON(transactionPath(dataRoot, transaction.id), transaction);
}

function parseChecksums(value) {
  const result = new Map();
  for (const line of value.split(/\r?\n/)) {
    const match = /^([a-fA-F0-9]{64})\s+[* ]?(.+)$/.exec(line.trim());
    if (match) {
      result.set(match[2], match[1].toLowerCase());
    }
  }
  return result;
}

async function fetchRequired(fetchImpl, url, token, accept) {
  const headers = {
    accept,
    "user-agent": "agentbell-bootstrap"
  };
  if (token) {
    headers.authorization = `Bearer ${token}`;
  }
  const response = await fetchImpl(url, {
    headers,
    redirect: "follow"
  });
  if (!response.ok) {
    throw new Error(`Download failed (${response.status}) for ${url}`);
  }
  return response;
}

function githubReleaseAPI(releaseBase) {
  try {
    const parsed = new URL(releaseBase);
    const match = /^\/([^/]+)\/([^/]+)\/releases\/?$/.exec(parsed.pathname);
    if (parsed.hostname !== "github.com" || !match) {
      return null;
    }
    return `${githubAPIOrigin}/repos/${encodeURIComponent(match[1])}/${encodeURIComponent(match[2])}`;
  } catch {
    return null;
  }
}

async function defaultDownloadBundle({
  version,
  target,
  releaseBase =
    process.env.AGENTBELL_RELEASE_BASE_URL || defaultReleaseBase,
  token = process.env.AGENTBELL_GITHUB_TOKEN || process.env.GH_TOKEN,
  fetchImpl = globalThis.fetch
}) {
  if (typeof fetchImpl !== "function") {
    throw new Error("This Node.js runtime does not provide fetch.");
  }
  const names = [
    "checksums.txt",
    "release-manifest.json",
    target.fileName,
    target.bridgeFileName,
    ...(target.serviceBridgeFileName ? [target.serviceBridgeFileName] : [])
  ];
  let urls;
  const repositoryAPI = token ? githubReleaseAPI(releaseBase) : null;
  if (repositoryAPI) {
    const metadata = await fetchGitHubReleaseMetadata({
      fetchImpl,
      repositoryAPI,
      tagName: `v${version}`,
      token
    });
    if (!Array.isArray(metadata.assets)) {
      throw new Error(`GitHub release v${version} returned no asset list.`);
    }
    urls = new Map(names.map((name) => {
      const asset = metadata.assets.find((candidate) => candidate?.name === name);
      if (!asset || typeof asset.url !== "string") {
        throw new Error(`GitHub release v${version} does not contain ${name}.`);
      }
      return [name, asset.url];
    }));
  } else {
    const base = `${releaseBase.replace(/\/$/, "")}/download/v${version}`;
    urls = new Map(names.map((name) => [name, `${base}/${name}`]));
  }

  const checksumsResponse = await fetchRequired(
    fetchImpl,
    urls.get("checksums.txt"),
    token,
    "application/octet-stream"
  );
  const checksums = parseChecksums(await checksumsResponse.text());
  const manifestResponse = await fetchRequired(
    fetchImpl,
    urls.get("release-manifest.json"),
    token,
    "application/octet-stream"
  );
  const manifest = await manifestResponse.json();
  if (manifest?.schemaVersion !== 1 ||
      manifest.version !== version ||
      manifest.signatureStatus !== "technical-preview" ||
      !Array.isArray(manifest.artifacts)) {
    throw new Error("Release manifest is incompatible with this bootstrap.");
  }
  const download = async (name) => {
    const expected = checksums.get(name);
    const manifestEntry = manifest.artifacts.find(
      (artifact) => artifact?.fileName === name
    );
    if (!expected || manifestEntry?.sha256 !== expected) {
      throw new Error(`Release metadata does not consistently describe ${name}.`);
    }
    const response = await fetchRequired(
      fetchImpl,
      urls.get(name),
      token,
      "application/octet-stream"
    );
    return Buffer.from(await response.arrayBuffer());
  };
  const core = await download(target.fileName);
  const bridge = await download(target.bridgeFileName);
  const serviceBridge = target.serviceBridgeFileName
    ? await download(target.serviceBridgeFileName)
    : null;
  return {
    core,
    bridge,
    ...(serviceBridge ? { serviceBridge } : {}),
    coreChecksum: checksums.get(target.fileName),
    bridgeChecksum: checksums.get(target.bridgeFileName),
    ...(target.serviceBridgeFileName
      ? { serviceBridgeChecksum: checksums.get(target.serviceBridgeFileName) }
      : {}),
    signatureStatus: manifest.signatureStatus,
    manifest
  };
}

function validateBundle(bundle, version, target) {
  if (!bundle || !Buffer.isBuffer(bundle.core) ||
      !Buffer.isBuffer(bundle.bridge)) {
    throw new Error("Release bundle must contain Core and bridge bytes.");
  }
  if (!checksumPattern.test(bundle.coreChecksum || "") ||
      checksum(bundle.core) !== bundle.coreChecksum) {
    throw new Error("Core SHA-256 mismatch.");
  }
  if (!checksumPattern.test(bundle.bridgeChecksum || "") ||
      checksum(bundle.bridge) !== bundle.bridgeChecksum) {
    throw new Error("Bridge SHA-256 mismatch.");
  }
  if (target.serviceBridgeFileName && (
    !Buffer.isBuffer(bundle.serviceBridge) ||
    !checksumPattern.test(bundle.serviceBridgeChecksum || "") ||
    checksum(bundle.serviceBridge) !== bundle.serviceBridgeChecksum
  )) {
    throw new Error("Service bridge SHA-256 mismatch.");
  }
  if (bundle.signatureStatus !== "technical-preview" ||
      bundle.manifest?.schemaVersion !== 1 ||
      bundle.manifest?.version !== version) {
    throw new Error("Release bundle manifest is invalid.");
  }
}

async function defaultSmokeCore({ path: executable }) {
  const code = await runExecutable(
    executable,
    ["version", "--json"],
    { stdin: "ignore", stdout: "ignore", stderr: "ignore" }
  );
  if (code !== 0) {
    throw new Error(`AgentBell Core smoke test exited with code ${code}.`);
  }
}

async function defaultRestartService({ corePath }) {
  const code = await runExecutable(
    corePath,
    ["service", "restart", "--json"],
    { stdin: "ignore", stdout: "ignore", stderr: "inherit" }
  );
  if (code !== 0) {
    throw new Error(`AgentBell service restart exited with code ${code}.`);
  }
}

export function serviceTransitionAction({
  operation,
  active,
  compensation = false,
  serviceQuiesced = false
}) {
  if (operation === "repair") {
    return "install";
  }
  if (operation === "upgrade") {
    if (compensation) {
      return active === null || serviceQuiesced ? "install" : "restart";
    }
    return "install";
  }
  return "restart";
}

export async function runUpgradeServiceTransition(
  request,
  execute = runExecutable
) {
  const action = serviceTransitionAction({
    operation: request.operation || "upgrade",
    active: request.active,
    previousActive: request.previousActive,
    compensation: request.compensation,
    serviceQuiesced: request.serviceQuiesced
  });
  const code = await execute(
    request.corePath,
    ["service", action, "--json"],
    { stdin: "ignore", stdout: "ignore", stderr: "inherit" }
  );
  if (code !== 0) {
    throw new Error(
      `AgentBell service ${action} exited with code ${code}.`
    );
  }
}

async function defaultUpgradeService(request) {
  await runUpgradeServiceTransition(request);
}

export async function runUpgradeServiceQuiesce(
  request,
  execute = runExecutable,
  inspectProcess = inspectWindowsServiceProcess
) {
  const taskName = request.taskName || windowsServiceTaskName;
  const queryCode = await execute(
    "schtasks.exe",
    ["/Query", "/TN", taskName],
    { stdin: "ignore", stdout: "ignore", stderr: "ignore" }
  );
  if (queryCode === 0) {
    await execute(
      "schtasks.exe",
      ["/End", "/TN", taskName],
      { stdin: "ignore", stdout: "ignore", stderr: "ignore" }
    );
    const deleteCode = await execute(
      "schtasks.exe",
      ["/Delete", "/TN", taskName, "/F"],
      { stdin: "ignore", stdout: "ignore", stderr: "inherit" }
    );
    if (deleteCode !== 0) {
      throw new Error(
        `AgentBell Windows task removal exited with code ${deleteCode}.`
      );
    }
  }
  await stopVerifiedWindowsServiceOrphan(request, execute, inspectProcess);
}

async function defaultUpgradeServiceQuiesce(request) {
  await runUpgradeServiceQuiesce(request);
}

function runExecutable(executable, args, stdio) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {
      shell: false,
      windowsHide: true,
      stdio: [stdio.stdin, stdio.stdout, stdio.stderr]
    });
    child.once("error", reject);
    child.once("close", (code, signal) => {
      if (signal) {
        reject(new Error(`AgentBell process terminated by ${signal}.`));
      } else {
        resolve(code ?? 1);
      }
    });
  });
}

function inspectWindowsServiceProcess(pid) {
  const script = [
    `$process = Get-CimInstance Win32_Process -Filter "ProcessId = ${pid}" ` +
      "-ErrorAction Stop",
    "if ($null -eq $process) { exit 3 }",
    "$result = [ordered]@{ executablePath = [string]$process.ExecutablePath; " +
      "commandLine = [string]$process.CommandLine }",
    "[Console]::Out.Write(($result | ConvertTo-Json -Compress))"
  ].join("; ");
  return new Promise((resolve, reject) => {
    const child = spawn("powershell.exe", [
      "-NoProfile",
      "-NonInteractive",
      "-Command",
      script
    ], {
      shell: false,
      windowsHide: true,
      stdio: ["ignore", "pipe", "pipe"]
    });
    let stdout = "";
    let stderr = "";
    let oversized = false;
    const collect = (field, chunk) => {
      const next = field + chunk.toString("utf8");
      if (Buffer.byteLength(next) > maximumServiceLockBytes) {
        oversized = true;
        child.kill();
        return field;
      }
      return next;
    };
    child.stdout.on("data", (chunk) => {
      stdout = collect(stdout, chunk);
    });
    child.stderr.on("data", (chunk) => {
      stderr = collect(stderr, chunk);
    });
    child.once("error", reject);
    child.once("close", (code, signal) => {
      if (oversized) {
        reject(new Error("Windows service process inspection exceeded its output limit."));
      } else if (signal) {
        reject(new Error(`Windows service process inspection ended by ${signal}.`));
      } else if (code === 3) {
        resolve(null);
      } else if (code !== 0) {
        reject(new Error(
          `Windows service process inspection exited with code ${code}: ` +
          stderr.trim().slice(0, 1024)
        ));
      } else {
        try {
          const value = JSON.parse(stdout);
          assertKnownFields(
            value,
            ["executablePath", "commandLine"],
            "Windows service process",
            ["executablePath", "commandLine"]
          );
          if (typeof value.executablePath !== "string" ||
              typeof value.commandLine !== "string") {
            throw new Error("Windows service process fields must be strings.");
          }
          resolve(value);
        } catch (error) {
          reject(new Error(`parse Windows service process: ${error.message}`, {
            cause: error
          }));
        }
      }
    });
  });
}

async function loadWindowsServiceLock(stateDir) {
  if (!stateDir) {
    return null;
  }
  const lockPath = path.join(stateDir, "queue", "service.lock");
  let info;
  try {
    info = await lstat(lockPath);
  } catch (error) {
    if (error?.code === "ENOENT") {
      return null;
    }
    throw error;
  }
  if (!info.isFile() || info.isSymbolicLink() || info.size > maximumServiceLockBytes) {
    throw new Error("AgentBell service lock is not a bounded regular file.");
  }
  let record;
  try {
    record = JSON.parse(await readFile(lockPath, "utf8"));
  } catch (error) {
    throw new Error(`parse AgentBell service lock: ${error.message}`, {
      cause: error
    });
  }
  assertKnownFields(record, ["pid", "heartbeat"], "AgentBell service lock", [
    "pid",
    "heartbeat"
  ]);
  if (!Number.isSafeInteger(record.pid) || record.pid <= 0 ||
      typeof record.heartbeat !== "string") {
    throw new Error("AgentBell service lock has invalid process metadata.");
  }
  return { lockPath, pid: record.pid };
}

function sameWindowsPath(left, right) {
  return path.win32.normalize(left).toLowerCase() ===
    path.win32.normalize(right).toLowerCase();
}

function isWindowsServiceCommand(commandLine) {
  return /(?:^|\s)"?service"?\s+"?run"?\s+"?--foreground"?\s*$/i.test(
    commandLine
  );
}

async function stopVerifiedWindowsServiceOrphan(
  request,
  execute,
  inspectProcess
) {
  const lock = await loadWindowsServiceLock(request.stateDir);
  if (lock === null) {
    return;
  }
  let process = await inspectProcess(lock.pid);
  if (process === null) {
    await rm(lock.lockPath, { force: true });
    return;
  }
  const allowedCorePaths = [
    ...(request.allowedCorePaths || []),
    request.corePath
  ].filter((value) => typeof value === "string" && value !== "");
  if (!allowedCorePaths.some((value) =>
    sameWindowsPath(value, process.executablePath)) ||
      !isWindowsServiceCommand(process.commandLine)) {
    throw new Error(
      `refuse to terminate unverified AgentBell service lock PID ${lock.pid}.`
    );
  }
  const killCode = await execute(
    "taskkill.exe",
    ["/PID", String(lock.pid), "/T", "/F"],
    { stdin: "ignore", stdout: "ignore", stderr: "inherit" }
  );
  for (let attempt = 0; attempt < 20; attempt += 1) {
    process = await inspectProcess(lock.pid);
    if (process === null) {
      break;
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  if (process !== null) {
    throw new Error(
      `AgentBell managed service process ${lock.pid} remained after taskkill ` +
      `exited with code ${killCode}.`
    );
  }
  await rm(lock.lockPath, { force: true });
}

async function readOptional(filePath) {
  try {
    return await readFile(filePath);
  } catch (error) {
    if (error?.code === "ENOENT") {
      return null;
    }
    throw error;
  }
}

function resolveRuntimeDirectories({
  dataRoot,
  dataRootWasExplicit,
  configDir,
  stateDir,
  platform,
  home = os.homedir(),
  env = process.env
}) {
  if (configDir && stateDir) {
    return {
      configDir: path.resolve(configDir),
      stateDir: path.resolve(stateDir)
    };
  }
  if (dataRootWasExplicit) {
    return {
      configDir: path.resolve(configDir || path.join(dataRoot, "config")),
      stateDir: path.resolve(stateDir || path.join(dataRoot, "state"))
    };
  }
  let defaultConfigDir;
  let defaultStateDir;
  if (platform === "win32") {
    defaultConfigDir = path.join(
      env.APPDATA || path.join(home, "AppData", "Roaming"),
      "AgentBell"
    );
    defaultStateDir = path.join(
      env.LOCALAPPDATA || path.join(home, "AppData", "Local"),
      "AgentBell",
      "state"
    );
  } else if (platform === "darwin") {
    defaultConfigDir = path.join(
      home,
      "Library",
      "Application Support",
      "AgentBell"
    );
    defaultStateDir = path.join(defaultConfigDir, "state");
  } else {
    defaultConfigDir = path.join(
      env.XDG_CONFIG_HOME || path.join(home, ".config"),
      "agentbell"
    );
    defaultStateDir = path.join(
      env.XDG_STATE_HOME || path.join(home, ".local", "state"),
      "agentbell"
    );
  }
  return {
    configDir: path.resolve(
      configDir ||
      (env.AGENTBELL_CONFIG
        ? path.dirname(env.AGENTBELL_CONFIG)
        : defaultConfigDir)
    ),
    stateDir: path.resolve(
      stateDir || env.AGENTBELL_STATE_DIR || defaultStateDir
    )
  };
}

function validateSidecar(name, value) {
  const definition = sidecarDefinitions.find((entry) => entry.name === name);
  const document = assertKnownFields(
    value,
    definition.fields,
    name,
    definition.fields
  );
  if (document.version !== 1) {
    throw new Error(`${name} has unsupported sidecar version ${document.version}.`);
  }
  const minimum = parseSemanticVersion(
    document.minCoreVersion,
    `${name} minCoreVersion`
  );
  if (name === "settings.json") {
    validateSettingsShape(document);
  } else if (name === "remote.json") {
    validateRemoteShape(document);
  } else if (name === "host-connectors.json") {
    validateHostConnectorsShape(document);
  } else {
    validateRelayShape(document);
  }
  return minimum;
}

async function loadSidecarPreflight(configDir, definition, targetVersion) {
  const filePath = path.join(configDir, definition.name);
  const raw = await readOptional(filePath);
  if (raw === null) {
    return { name: definition.name, status: "absent" };
  }
  if (raw.length > maximumSidecarBytes) {
    const error = new Error(
      `${definition.name} exceeds ${maximumSidecarBytes} bytes.`
    );
    error.sidecar = { name: definition.name, status: "invalid" };
    throw error;
  }
  let document;
  try {
    document = JSON.parse(raw.toString("utf8"));
    rejectDuplicateJSONKeys(raw.toString("utf8"), definition.name);
  } catch (error) {
    const wrapped = new Error(`parse ${definition.name}: ${error.message}`, {
      cause: error
    });
    wrapped.sidecar = { name: definition.name, status: "invalid" };
    throw wrapped;
  }
  let minimum;
  try {
    minimum = validateSidecar(definition.name, document);
  } catch (error) {
    error.sidecar = { name: definition.name, status: "invalid" };
    throw error;
  }
  if (compareSemanticVersions(minimum, targetVersion) > 0) {
    const error = new Error(
      `${definition.name} requires AgentBell Core ` +
      `${minimum.normalized}, but rollback target is ${targetVersion.normalized}.`
    );
    error.sidecar = {
      name: definition.name,
      status: "incompatible",
      minCoreVersion: minimum.normalized,
      checksum: checksum(raw)
    };
    throw error;
  }
  return {
    name: definition.name,
    status: "compatible",
    minCoreVersion: minimum.normalized,
    checksum: checksum(raw)
  };
}

async function scanQueueRollbackPreflight(stateDir) {
  const result = {
    scannedItems: 0,
    hasPartialSuccess: false,
    partialItems: 0
  };
  const queueRoot = path.join(stateDir, "queue");
  for (const state of queueStates) {
    const directory = path.join(queueRoot, state);
    let entries;
    try {
      entries = await readdir(directory, { withFileTypes: true });
    } catch (error) {
      if (error?.code === "ENOENT") {
        continue;
      }
      throw error;
    }
    for (const entry of entries) {
      if (!entry.name.endsWith(".json")) {
        continue;
      }
      if (!entry.isFile()) {
        throw new Error(
          `queue ${state} contains a non-regular JSON entry.`
        );
      }
      const raw = await readFile(path.join(directory, entry.name));
      if (raw.length > maximumSidecarBytes) {
        throw new Error(`queue ${state} item exceeds the rollback scan limit.`);
      }
      let envelope;
      try {
        envelope = JSON.parse(raw.toString("utf8"));
        rejectDuplicateJSONKeys(raw.toString("utf8"), `queue ${state} item`);
      } catch (error) {
        throw new Error(`parse queue ${state} item: ${error.message}`, {
          cause: error
        });
      }
      assertObject(envelope, `queue ${state} item`);
      if (envelope.queueVersion !== 1) {
        throw new Error(
          `queue ${state} item has unsupported queueVersion.`
        );
      }
      result.scannedItems++;
      if (envelope.ledger === undefined) {
        continue;
      }
      if (!Array.isArray(envelope.ledger) || envelope.ledger.length === 0) {
        throw new Error(`queue ${state} item has an invalid delivery ledger.`);
      }
      let succeeded = 0;
      for (const ledgerEntry of envelope.ledger) {
        const validated = assertKnownFields(
          ledgerEntry,
          [
            "channelId",
            "templateId",
            "state",
            "attempts",
            "nextAttemptAt",
            "lastError",
            "messageId"
          ],
          `queue ${state} delivery ledger`
        );
        if (!["pending", "succeeded", "dead"].includes(validated.state)) {
          throw new Error(
            `queue ${state} item has an invalid delivery ledger state.`
          );
        }
        if (validated.state === "succeeded") {
          succeeded++;
        }
      }
      if (succeeded > 0 && succeeded < envelope.ledger.length) {
        result.hasPartialSuccess = true;
        result.partialItems++;
      }
    }
  }
  return result;
}

class RollbackPreflightError extends Error {
  constructor(message, preflight, options) {
    super(message, options);
    this.name = "RollbackPreflightError";
    this.preflight = preflight;
  }
}

async function runRollbackPreflight({
  configDir,
  stateDir,
  targetVersion
}) {
  const parsedTarget = parseSemanticVersion(
    targetVersion,
    "rollback target version"
  );
  const result = {
    outcome: "passed",
    targetVersion: parsedTarget.normalized,
    checkedAt: new Date().toISOString(),
    sidecars: [],
    ledger: {
      scannedItems: 0,
      hasPartialSuccess: false,
      partialItems: 0
    }
  };
  try {
    for (const definition of sidecarDefinitions) {
      try {
        result.sidecars.push(await loadSidecarPreflight(
          configDir,
          definition,
          parsedTarget
        ));
      } catch (error) {
        if (error.sidecar) {
          result.sidecars.push(error.sidecar);
        }
        throw error;
      }
    }
    result.ledger = await scanQueueRollbackPreflight(stateDir);
    if (result.ledger.hasPartialSuccess) {
      throw new Error(
        "Rollback is unsafe while a partially successful delivery ledger exists."
      );
    }
    return result;
  } catch (error) {
    result.outcome = "rejected";
    throw new RollbackPreflightError(error.message, result, {
      cause: error
    });
  }
}

async function installVersion({
  dataRoot,
  version,
  platform,
  target,
  bundle,
  transaction
}) {
  const destination = path.dirname(installedCorePath(dataRoot, version, platform));
  await rejectSymlinkIfPresent(destination, "managed version directory");
  const metadata = {
    schemaVersion: 1,
    version,
    target: target.id,
    fileName: target.fileName,
    checksum: bundle.coreChecksum,
    bridgeFileName: target.bridgeFileName,
    bridgeChecksum: bundle.bridgeChecksum,
    ...(target.serviceBridgeFileName
      ? {
          serviceBridgeFileName: target.serviceBridgeFileName,
          serviceBridgeChecksum: bundle.serviceBridgeChecksum
        }
      : {}),
    installedAt: new Date().toISOString(),
    signatureStatus: bundle.signatureStatus,
    transactionId: transaction.id
  };
  try {
    const existing = await readFile(path.join(destination, executableName(platform)));
    if (checksum(existing) !== bundle.coreChecksum) {
      throw new Error(`Installed Core ${version} conflicts with the release checksum.`);
    }
    try {
      const cached = await installedMetadata(
        dataRoot,
        version,
        platform,
        target
      );
      if (cached.legacy ||
          cached.metadata.checksum !== bundle.coreChecksum ||
          cached.metadata.bridgeChecksum !== bundle.bridgeChecksum ||
          cached.metadata.serviceBridgeChecksum !== bundle.serviceBridgeChecksum) {
        throw new Error(
          `Installed Core ${version} conflicts with the release metadata.`
        );
      }
    } catch (error) {
      if (error?.code !== "ENOENT") {
        throw error;
      }
      await atomicJSON(path.join(destination, "install.json"), metadata);
    }
    return false;
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
  await rm(destination, { recursive: true, force: true });
  const staging = path.join(
    transactionDirectory(dataRoot),
    `${transaction.id}.stage`
  );
  await rm(staging, { recursive: true, force: true });
  await mkdir(staging, { recursive: true, mode: 0o700 });
  const stagedCore = path.join(staging, executableName(platform));
  await atomicWrite(stagedCore, bundle.core, 0o700);
  await atomicJSON(path.join(staging, "install.json"), metadata);
  await rename(staging, destination);
  await syncDirectory(path.dirname(destination));
  return true;
}

async function installedMetadata(dataRoot, version, platform, target) {
  const directory = path.dirname(installedCorePath(dataRoot, version, platform));
  await rejectSymlinkIfPresent(directory, "managed version directory");
  const metadataPath = path.join(directory, "install.json");
  const raw = await readFile(metadataPath, "utf8");
  rejectDuplicateJSONKeys(raw, `AgentBell ${version} install metadata`);
  const metadata = JSON.parse(raw);
  const legacy = metadata?.schemaVersion === undefined;
  assertKnownFields(
    metadata,
    legacy
      ? [
          "version",
          "target",
          "fileName",
          "checksum",
          "installedAt",
          "signatureStatus"
        ]
      : [
          "schemaVersion",
          "version",
          "target",
          "fileName",
          "checksum",
          "bridgeFileName",
          "bridgeChecksum",
          "serviceBridgeFileName",
          "serviceBridgeChecksum",
          "installedAt",
          "signatureStatus",
          "transactionId"
        ],
    `AgentBell ${version} install metadata`
  );
  const core = await readFile(path.join(directory, executableName(platform)));
  if ((!legacy && metadata.schemaVersion !== 1) ||
      metadata.version !== version ||
      metadata.target !== target.id ||
      metadata.fileName !== target.fileName ||
      !checksumPattern.test(metadata.checksum || "") ||
      checksum(core) !== metadata.checksum ||
      metadata.signatureStatus !== "technical-preview" ||
      typeof metadata.installedAt !== "string" ||
      metadata.installedAt.length === 0) {
    throw new Error(`Installed AgentBell version ${version} is invalid.`);
  }
  if (!legacy && (
    metadata.bridgeFileName !== target.bridgeFileName ||
    !checksumPattern.test(metadata.bridgeChecksum || "") ||
    ((metadata.serviceBridgeFileName === undefined) !==
      (metadata.serviceBridgeChecksum === undefined)) ||
    (metadata.serviceBridgeFileName !== undefined && (
      metadata.serviceBridgeFileName !== target.serviceBridgeFileName ||
      !checksumPattern.test(metadata.serviceBridgeChecksum || "")
    )) ||
    (!target.serviceBridgeFileName && (
      metadata.serviceBridgeFileName !== undefined ||
      metadata.serviceBridgeChecksum !== undefined
    )) ||
    typeof metadata.transactionId !== "string" ||
    metadata.transactionId.length === 0
  )) {
    throw new Error(`Installed AgentBell version ${version} is invalid.`);
  }
  return {
    metadata,
    legacy,
    corePath: path.join(directory, executableName(platform))
  };
}

async function discoverLegacyInstall({
  dataRoot,
  fromVersion,
  platform,
  target
}) {
  if (fromVersion !== undefined) {
    assertVersion(fromVersion);
    const installed = await installedMetadata(
      dataRoot,
      fromVersion,
      platform,
      target
    );
    return { version: fromVersion, installed };
  }
  const binRoot = path.join(dataRoot, "bin");
  let entries;
  try {
    entries = await readdir(binRoot, { withFileTypes: true });
  } catch (error) {
    if (error?.code === "ENOENT") {
      return null;
    }
    throw error;
  }
  const candidates = [];
  for (const entry of entries) {
    if (!entry.isDirectory() || !versionPattern.test(entry.name)) {
      continue;
    }
    try {
      candidates.push({
        version: entry.name,
        installed: await installedMetadata(
          dataRoot,
          entry.name,
          platform,
          target
        )
      });
    } catch {
      // Invalid or foreign cached directories cannot become rollback targets.
    }
  }
  if (candidates.length === 0) {
    return null;
  }
  if (candidates.length > 1) {
    throw new Error(
      "Multiple legacy AgentBell versions are installed; " +
      "rerun upgrade with --from <version>."
    );
  }
  return candidates[0];
}

async function runCompensationStep(compensation, action, callback) {
  const step = { action, status: "running" };
  compensation.steps.push(step);
  try {
    await callback();
    step.status = "completed";
  } catch (error) {
    step.status = "failed";
    step.error = String(error.message || error).slice(0, 4096);
    throw error;
  } finally {
    step.updatedAt = new Date().toISOString();
  }
}

function newCompensation() {
  return {
    status: "running",
    startedAt: new Date().toISOString(),
    steps: []
  };
}

async function compensateUpgrade({
  dataRoot,
  stateDir,
  previousActive,
  previousCorePath,
  attemptedGeneration,
  transaction,
  bridgePath,
  previousBridge,
  bridgeChanged,
  serviceBridgePath,
  previousServiceBridge,
  serviceBridgeChanged,
  serviceQuiesced,
  activeWritten,
  installedNew,
  corePath,
  operation = "upgrade",
  restartService,
  quiesceService,
  writeStableBridge
}) {
  const compensation = newCompensation();
  const errors = [];
  let restoredActive = null;
  let activeRestored = !activeWritten;
  const attempt = async (action, callback) => {
    try {
      await runCompensationStep(compensation, action, callback);
    } catch (error) {
      errors.push(error);
    }
  };
  if (serviceQuiesced && previousCorePath) {
    await attempt("quiesce-attempted-service", async () => {
      await quiesceService({
        operation,
        corePath: activeWritten && corePath ? corePath : previousCorePath,
        active: activeWritten
          ? { activeVersion: transaction.toVersion }
          : previousActive,
        previousActive,
        stateDir,
        allowedCorePaths: [corePath, previousCorePath],
        compensation: true
      });
    });
  }
  if (bridgeChanged) {
    await attempt("restore-bridge", async () => {
      if (previousBridge === null) {
        await rm(bridgePath, { force: true });
      } else {
        await writeStableBridge(bridgePath, previousBridge, 0o700);
      }
    });
  }
  if (serviceBridgeChanged) {
    await attempt("restore-service-bridge", async () => {
      if (previousServiceBridge === null) {
        await rm(serviceBridgePath, { force: true });
      } else {
        await writeStableBridge(serviceBridgePath, previousServiceBridge, 0o700);
      }
    });
  }
  if (activeWritten) {
    await attempt("restore-active", async () => {
      if (previousActive) {
        const restored = {
          ...previousActive,
          generation: attemptedGeneration + 1,
          transactionId: transaction.id
        };
        await atomicJSON(activeStatePath(dataRoot), restored);
        restoredActive = restored;
      } else {
        await rm(activeStatePath(dataRoot), { force: true });
      }
      activeRestored = true;
    });
  }
  if ((activeWritten || serviceQuiesced) && previousCorePath && activeRestored) {
    await attempt("restart-previous-service", async () => {
      await restartService({
        operation,
        corePath: previousCorePath,
        bridgePath,
        active: restoredActive || previousActive,
        compensation: true,
        serviceQuiesced
      });
    });
  }
  if (installedNew && corePath && activeRestored) {
    await attempt("remove-staged-version", async () => {
      await rm(path.dirname(corePath), { recursive: true, force: true });
    });
  } else if (installedNew && corePath) {
    compensation.steps.push({
      action: "remove-staged-version",
      status: "skipped",
      error: "active state was not restored; activated Core was retained",
      updatedAt: new Date().toISOString()
    });
  }
  compensation.status = errors.length === 0 ? "completed" : "failed";
  compensation.completedAt = new Date().toISOString();
  return { compensation, errors };
}

export async function upgrade({
  toVersion,
  fromVersion,
  channel = "stable",
  dryRun = false,
  dataRoot,
  configDir,
  stateDir,
  home = os.homedir(),
  env = process.env,
  platform = process.platform,
  architecture = process.arch,
  releaseBase,
  token,
  fetchImpl,
  downloadBundle = defaultDownloadBundle,
  smokeCore = defaultSmokeCore,
  restartService = defaultUpgradeService,
  quiesceService = defaultUpgradeServiceQuiesce,
  writeStableBridge = atomicWrite,
  writeActive = async (filePath, state) => atomicJSON(filePath, state)
}) {
  assertVersion(toVersion);
  if (fromVersion !== undefined) {
    assertVersion(fromVersion);
    if (fromVersion === toVersion) {
      throw new Error("--from must differ from --to.");
    }
  }
  if (!["stable", "next"].includes(channel)) {
    throw new Error(`Unsupported AgentBell release channel: ${channel}`);
  }
  const dataRootWasExplicit = dataRoot !== undefined;
  dataRoot = assertManagedRoot(
    dataRoot || resolveDataRoot({ platform, home, env })
  );
  const runtimeDirectories = resolveRuntimeDirectories({
    dataRoot,
    dataRootWasExplicit,
    configDir,
    stateDir,
    platform,
    home,
    env
  });
  await rejectSymlinkIfPresent(dataRoot, "AgentBell data root");
  const target = resolveTarget(platform, architecture);
  const current = await loadActive(dataRoot, { optional: true });
  if (current && fromVersion !== undefined) {
    throw new Error("--from is only valid when migrating a legacy installation.");
  }
  let legacy = current
    ? null
    : await discoverLegacyInstall({
        dataRoot,
        fromVersion,
        platform,
        target
      });
  if (legacy?.version === toVersion) {
    if (fromVersion !== undefined) {
      throw new Error("--from must differ from --to.");
    }
    legacy = null;
  }
  const currentVersion = current?.activeVersion || legacy?.version || "";
  if (dryRun) {
    const preflight = currentVersion
      ? await runRollbackPreflight({
          ...runtimeDirectories,
          targetVersion: currentVersion
        })
      : {
        outcome: "skipped",
        reason: "no-active-version"
      };
    return {
      dryRun: true,
      operation: "upgrade",
      fromVersion: currentVersion,
      toVersion,
      target: target.id,
      channel,
      preflight
    };
  }
  const release = await acquireLock(dataRoot);
  let transaction;
  let active = null;
  let legacyInstall;
  let previousVersion = "";
  let previousCorePath = "";
  let installedNew = false;
  let corePath = "";
  let bridgePath = "";
  let previousBridge = null;
  let bridgeChanged = false;
  let serviceBridgePath = "";
  let previousServiceBridge = null;
  let serviceBridgeChanged = false;
  let serviceQuiesced = false;
  let activeWritten = false;
  let attemptedGeneration = 0;
  let repairingSameVersion = false;
  try {
    active = await loadActive(dataRoot, { optional: true });
    if (active && fromVersion !== undefined) {
      throw new Error("--from is only valid when migrating a legacy installation.");
    }
    legacyInstall = active
      ? null
      : await discoverLegacyInstall({
          dataRoot,
          fromVersion,
          platform,
          target
        });
    if (legacyInstall?.version === toVersion) {
      if (fromVersion !== undefined) {
        throw new Error("--from must differ from --to.");
      }
      legacyInstall = null;
    }
    previousVersion =
      active?.activeVersion || legacyInstall?.version || "";
    previousCorePath = active
      ? installedCorePath(dataRoot, active.activeVersion, platform)
      : legacyInstall?.installed.corePath || "";
    if (active?.target && active.target !== target.id) {
      throw new Error(
        `Active target ${active.target} does not match ${target.id}.`
      );
    }
    if (active?.activeVersion === toVersion) {
      let installed = null;
      try {
        installed = await installedMetadata(
          dataRoot,
          toVersion,
          platform,
          target
        );
      } catch (error) {
        if (error?.code !== "ENOENT") {
          throw error;
        }
      }
      if (installed) {
        if (installed.legacy ||
            installed.metadata.checksum !== active.checksum ||
            installed.metadata.bridgeChecksum !== active.bridgeChecksum ||
            installed.metadata.serviceBridgeChecksum !== active.serviceBridgeChecksum) {
          throw new Error(
            `Active AgentBell version ${toVersion} conflicts with its install metadata.`
          );
        }
        bridgePath = stableBridgePath({ dataRoot, platform });
        await rejectSymlinkIfPresent(
          path.dirname(bridgePath),
          "stable bridge directory"
        );
        previousBridge = await readOptional(bridgePath);
        serviceBridgePath = stableServiceBridgePath({ dataRoot, platform });
        previousServiceBridge = target.serviceBridgeFileName
          ? await readOptional(serviceBridgePath)
          : previousBridge;
        const serviceBridgeReady = !target.serviceBridgeFileName || (
          previousServiceBridge !== null &&
          checksum(previousServiceBridge) === active.serviceBridgeChecksum
        );
        if (previousBridge !== null &&
            checksum(previousBridge) === active.bridgeChecksum &&
            serviceBridgeReady) {
          await restartService({
            operation: "repair",
            corePath: installed.corePath,
            bridgePath,
            active,
            previousActive: active
          });
          return {
            dryRun: false,
            reused: true,
            repaired: false,
            activeVersion: toVersion,
            previousVersion: active.previousVersion || "",
            generation: active.generation,
            transactionId: active.transactionId,
            corePath: installed.corePath,
            bridgePath,
            rolledBack: false
          };
        }
      }
      repairingSameVersion = true;
    }
    transaction = newTransaction(
      repairingSameVersion ? "repair" : "upgrade",
      previousVersion,
      toVersion,
      target.id
    );
    try {
      transaction.preflight = previousVersion
        ? await runRollbackPreflight({
          ...runtimeDirectories,
          targetVersion: previousVersion
        })
        : {
          outcome: "skipped",
          reason: "no-active-version"
        };
    } catch (error) {
      if (error instanceof RollbackPreflightError) {
        transaction.preflight = error.preflight;
      }
      await writeJournal(
        dataRoot,
        transaction,
        "preflight-rejected",
        error.message
      );
      throw error;
    }
    await writeJournal(dataRoot, transaction, "preflight-passed");
    await writeJournal(dataRoot, transaction, "downloading");
    const bundle = await downloadBundle({
      version: toVersion,
      target,
      releaseBase,
      token,
      fetchImpl
    });
    validateBundle(bundle, toVersion, target);
    if (repairingSameVersion && (
      bundle.coreChecksum !== active.checksum ||
      bundle.bridgeChecksum !== active.bridgeChecksum ||
      bundle.serviceBridgeChecksum !== active.serviceBridgeChecksum
    )) {
      throw new Error(
        `Release v${toVersion} conflicts with the active install checksums.`
      );
    }
    await writeJournal(dataRoot, transaction, "staging");
    installedNew = await installVersion({
      dataRoot,
      version: toVersion,
      platform,
      target,
      bundle,
      transaction
    });
    corePath = installedCorePath(dataRoot, toVersion, platform);
    await smokeCore({ path: corePath, version: toVersion, target: target.id });

    bridgePath = stableBridgePath({ dataRoot, platform });
    await rejectSymlinkIfPresent(path.dirname(bridgePath), "stable bridge directory");
    previousBridge = await readOptional(bridgePath);
    serviceBridgePath = stableServiceBridgePath({ dataRoot, platform });
    previousServiceBridge = target.serviceBridgeFileName
      ? await readOptional(serviceBridgePath)
      : previousBridge;
    if (platform === "win32" && previousCorePath) {
      serviceQuiesced = true;
      await writeJournal(dataRoot, transaction, "quiescing-service");
      await quiesceService({
        operation: repairingSameVersion ? "repair" : "upgrade",
        corePath: previousCorePath,
        bridgePath,
        active,
        previousActive: active,
        stateDir: runtimeDirectories.stateDir,
        allowedCorePaths: [previousCorePath]
      });
      await cleanupStableWriteResidues(bridgePath, platform);
      if (target.serviceBridgeFileName) {
        await cleanupStableWriteResidues(serviceBridgePath, platform);
      }
    }
    await writeJournal(dataRoot, transaction, "activating");
    await writeStableBridge(bridgePath, bundle.bridge, 0o700);
    bridgeChanged = true;
    if (target.serviceBridgeFileName) {
      await writeStableBridge(serviceBridgePath, bundle.serviceBridge, 0o700);
      serviceBridgeChanged = true;
    }
    const generation = active
      ? active.generation + 1
      : legacyInstall
        ? 2
        : 1;
    attemptedGeneration = generation;
    const nextActive = repairingSameVersion
      ? {
          ...active,
          generation,
          checksum: bundle.coreChecksum,
          bridgeChecksum: bundle.bridgeChecksum,
          ...(bundle.serviceBridgeChecksum
            ? { serviceBridgeChecksum: bundle.serviceBridgeChecksum }
            : {}),
          transactionId: transaction.id
        }
      : {
          schemaVersion: activeSchemaVersion,
          generation,
          activeVersion: toVersion,
          ...(previousVersion
            ? { previousVersion }
            : {}),
          target: target.id,
          checksum: bundle.coreChecksum,
          bridgeChecksum: bundle.bridgeChecksum,
          ...(bundle.serviceBridgeChecksum
            ? { serviceBridgeChecksum: bundle.serviceBridgeChecksum }
            : {}),
          transactionId: transaction.id
        };
    await writeActive(activeStatePath(dataRoot), nextActive);
    activeWritten = true;
    await restartService({
      operation: repairingSameVersion ? "repair" : "upgrade",
      corePath,
      bridgePath,
      active: nextActive,
      previousActive: active,
      serviceQuiesced
    });
    await writeJournal(dataRoot, transaction, "committed");
    return {
      dryRun: false,
      reused: false,
      repaired: repairingSameVersion,
      activeVersion: toVersion,
      previousVersion: repairingSameVersion
        ? active.previousVersion || ""
        : previousVersion,
      generation,
      transactionId: transaction.id,
      corePath,
      bridgePath,
      rolledBack: false
    };
  } catch (error) {
    if (transaction?.status === "preflight-rejected") {
      throw error;
    }
    if (transaction && activeWritten && previousVersion) {
      try {
        transaction.compensationPreflight = await runRollbackPreflight({
          ...runtimeDirectories,
          targetVersion: previousVersion
        });
      } catch (preflightError) {
        transaction.compensationPreflight =
          preflightError instanceof RollbackPreflightError
            ? preflightError.preflight
            : { outcome: "rejected" };
        transaction.compensation = {
          status: "blocked",
          startedAt: new Date().toISOString(),
          completedAt: new Date().toISOString(),
          steps: [{
            action: "post-failure-rollback-preflight",
            status: "failed",
            error: String(preflightError.message).slice(0, 4096),
            updatedAt: new Date().toISOString()
          }]
        };
        await writeJournal(
          dataRoot,
          transaction,
          "compensation-blocked",
          `${error.message}; rollback preflight: ${preflightError.message}`
        );
        throw new AggregateError(
          [error, preflightError],
          "AgentBell upgrade failed and automatic rollback was blocked by preflight.",
          { cause: preflightError }
        );
      }
    }
    let compensationErrors = [];
    if (transaction && (
      bridgeChanged || serviceBridgeChanged || serviceQuiesced ||
      activeWritten || installedNew
    )) {
      const result = await compensateUpgrade({
        dataRoot,
        stateDir: runtimeDirectories.stateDir,
        previousActive: active,
        previousCorePath,
        attemptedGeneration,
        transaction,
        bridgePath,
        previousBridge,
        bridgeChanged,
        serviceBridgePath,
        previousServiceBridge,
        serviceBridgeChanged,
        serviceQuiesced,
        activeWritten,
        installedNew,
        corePath,
        operation: repairingSameVersion ? "repair" : "upgrade",
        restartService,
        quiesceService,
        writeStableBridge
      });
      transaction.compensation = result.compensation;
      compensationErrors = result.errors;
    }
    if (transaction && ![
      "rolled-back",
      "committed",
      "preflight-rejected",
      "compensation-blocked"
    ].includes(transaction.status)) {
      await writeJournal(
        dataRoot,
        transaction,
        bridgeChanged || serviceBridgeChanged || serviceQuiesced || activeWritten
          ? compensationErrors.length === 0
            ? "rolled-back"
            : "compensation-failed"
          : "failed",
        compensationErrors.length > 0
          ? `${error.message}; compensation: ` +
            compensationErrors.map((value) => value.message).join("; ")
          : error.message
      );
    }
    if (compensationErrors.length > 0) {
      throw new AggregateError(
        [error, ...compensationErrors],
        `AgentBell upgrade failed (${error.message}) and compensation was incomplete.`,
        { cause: error }
      );
    }
    throw error;
  } finally {
    await release();
  }
}

async function resolveRollbackSelection({
  dataRoot,
  toVersion,
  platform,
  target
}) {
  const current = await loadActive(dataRoot);
  const selected = toVersion || current.previousVersion;
  if (!selected) {
    throw new Error("No previous AgentBell version is available.");
  }
  assertVersion(selected);
  if (selected === current.activeVersion) {
    throw new Error(`AgentBell ${selected} is already active.`);
  }
  try {
    return {
      current,
      selected,
      currentInstalled: await installedMetadata(
        dataRoot,
        current.activeVersion,
        platform,
        target
      ),
      installed: await installedMetadata(
        dataRoot,
        selected,
        platform,
        target
      )
    };
  } catch (error) {
    if (error?.code === "ENOENT") {
      throw new Error(
        `AgentBell ${selected} is not installed.`,
        { cause: error }
      );
    }
    throw error;
  }
}

export async function rollback({
  toVersion,
  dryRun = false,
  dataRoot,
  configDir,
  stateDir,
  home = os.homedir(),
  env = process.env,
  platform = process.platform,
  architecture = process.arch,
  smokeCore = defaultSmokeCore,
  restartService = defaultRestartService
}) {
  const dataRootWasExplicit = dataRoot !== undefined;
  dataRoot = assertManagedRoot(
    dataRoot || resolveDataRoot({ platform, home, env })
  );
  const runtimeDirectories = resolveRuntimeDirectories({
    dataRoot,
    dataRootWasExplicit,
    configDir,
    stateDir,
    platform,
    home,
    env
  });
  await rejectSymlinkIfPresent(dataRoot, "AgentBell data root");
  const target = resolveTarget(platform, architecture);
  if (dryRun) {
    const { current, selected } = await resolveRollbackSelection({
      dataRoot,
      toVersion,
      platform,
      target
    });
    const preflight = await runRollbackPreflight({
      ...runtimeDirectories,
      targetVersion: selected
    });
    return {
      dryRun: true,
      operation: "rollback",
      fromVersion: current.activeVersion,
      toVersion: selected,
      target: target.id,
      preflight
    };
  }

  const release = await acquireLock(dataRoot);
  let current;
  let selected;
  let installed;
  let currentInstalled;
  try {
    ({ current, selected, installed, currentInstalled } =
      await resolveRollbackSelection({
      dataRoot,
      toVersion,
      platform,
      target
      }));
  } catch (error) {
    await release();
    throw error;
  }
  const transaction = newTransaction(
    "rollback",
    current.activeVersion,
    selected,
    target.id
  );
  try {
    try {
      transaction.preflight = await runRollbackPreflight({
        ...runtimeDirectories,
        targetVersion: selected
      });
    } catch (error) {
      if (error instanceof RollbackPreflightError) {
        transaction.preflight = error.preflight;
      }
      await writeJournal(
        dataRoot,
        transaction,
        "preflight-rejected",
        error.message
      );
      throw error;
    }
    await writeJournal(dataRoot, transaction, "preflight-passed");
    await writeJournal(dataRoot, transaction, "smoke");
    await smokeCore({
      path: installed.corePath,
      version: selected,
      target: target.id
    });
    const generation = current.generation + 1;
    const nextActive = {
      schemaVersion: activeSchemaVersion,
      generation,
      activeVersion: selected,
      previousVersion: current.activeVersion,
      ...(!supportsM2Runtime(selected)
        ? {
            serviceVersion:
              current.serviceVersion || current.activeVersion,
            serviceChecksum:
              current.serviceChecksum || current.checksum
          }
        : {}),
      target: target.id,
      checksum: installed.metadata.checksum,
      bridgeChecksum: current.bridgeChecksum,
      ...(current.serviceBridgeChecksum
        ? { serviceBridgeChecksum: current.serviceBridgeChecksum }
        : {}),
      transactionId: transaction.id
    };
    await writeJournal(dataRoot, transaction, "activating");
    await atomicJSON(activeStatePath(dataRoot), nextActive);
    try {
      await restartService({
        // The rollback target may predate service-management commands. The
        // current Core remains the transaction controller and must restart
        // the version-independent bridge after the active pointer changes.
        corePath: currentInstalled.corePath,
        bridgePath: stableBridgePath({ dataRoot, platform }),
        active: nextActive
      });
    } catch (error) {
      const compensation = newCompensation();
      const compensationErrors = [];
      let restoredActive;
      const attempt = async (action, callback) => {
        try {
          await runCompensationStep(compensation, action, callback);
        } catch (compensationError) {
          compensationErrors.push(compensationError);
        }
      };
      await attempt("restore-active", async () => {
        const restored = {
          ...current,
          generation: generation + 1,
          transactionId: transaction.id
        };
        await atomicJSON(activeStatePath(dataRoot), restored);
        restoredActive = restored;
      });
      if (restoredActive) {
        await attempt("restart-previous-service", async () => {
          await restartService({
            corePath: currentInstalled.corePath,
            bridgePath: stableBridgePath({ dataRoot, platform }),
            active: restoredActive,
            compensation: true
          });
        });
      }
      compensation.status =
        compensationErrors.length === 0 ? "completed" : "failed";
      compensation.completedAt = new Date().toISOString();
      transaction.compensation = compensation;
      await writeJournal(
        dataRoot,
        transaction,
        compensationErrors.length === 0
          ? "rolled-back"
          : "compensation-failed",
        compensationErrors.length === 0
          ? error.message
          : `${error.message}; compensation: ` +
            compensationErrors.map((value) => value.message).join("; ")
      );
      if (compensationErrors.length > 0) {
        throw new AggregateError(
          [error, ...compensationErrors],
          `AgentBell rollback failed (${error.message}) and compensation was incomplete.`,
          { cause: error }
        );
      }
      throw error;
    }
    await writeJournal(dataRoot, transaction, "committed");
    return {
      dryRun: false,
      activeVersion: selected,
      previousVersion: current.activeVersion,
      generation,
      transactionId: transaction.id,
      rolledBack: false
    };
  } catch (error) {
    if (![
      "rolled-back",
      "committed",
      "preflight-rejected",
      "compensation-failed"
    ].includes(transaction.status)) {
      await writeJournal(dataRoot, transaction, "failed", error.message);
    }
    throw error;
  } finally {
    await release();
  }
}

function compareVersions(left, right) {
  const leftParts = left.version.split(/[.-]/);
  const rightParts = right.version.split(/[.-]/);
  for (let index = 0; index < Math.max(leftParts.length, rightParts.length); index++) {
    const leftPart = leftParts[index];
    const rightPart = rightParts[index];
    if (leftPart === rightPart) {
      continue;
    }
    if (leftPart === undefined) {
      return -1;
    }
    if (rightPart === undefined) {
      return 1;
    }
    const leftNumber = /^\d+$/.test(leftPart) ? Number(leftPart) : null;
    const rightNumber = /^\d+$/.test(rightPart) ? Number(rightPart) : null;
    if (leftNumber !== null && rightNumber !== null) {
      return leftNumber - rightNumber;
    }
    return leftPart.localeCompare(rightPart);
  }
  return 0;
}

export async function listVersions({
  dataRoot = resolveDataRoot(),
  platform = process.platform,
  architecture = process.arch
} = {}) {
  dataRoot = assertManagedRoot(dataRoot);
  await rejectSymlinkIfPresent(dataRoot, "AgentBell data root");
  const target = resolveTarget(platform, architecture);
  const active = await loadActive(dataRoot, { optional: true });
  const binRoot = path.join(dataRoot, "bin");
  let entries;
  try {
    entries = await readdir(binRoot, { withFileTypes: true });
  } catch (error) {
    if (error?.code === "ENOENT") {
      entries = [];
    } else {
      throw error;
    }
  }
  const installed = [];
  for (const entry of entries) {
    if (!entry.isDirectory() || !versionPattern.test(entry.name)) {
      continue;
    }
    try {
      const { metadata, corePath } = await installedMetadata(
        dataRoot,
        entry.name,
        platform,
        target
      );
      installed.push({
        version: entry.name,
        active: active?.activeVersion === entry.name,
        previous: active?.previousVersion === entry.name,
        checksum: metadata.checksum,
        signatureStatus: metadata.signatureStatus,
        installedAt: metadata.installedAt,
        corePath
      });
    } catch (error) {
      installed.push({
        version: entry.name,
        active: false,
        previous: false,
        invalid: true,
        error: error.message
      });
    }
  }
  installed.sort(compareVersions);
  return {
    activeVersion: active?.activeVersion || "",
    previousVersion: active?.previousVersion || "",
    generation: active?.generation || 0,
    target: target.id,
    installed
  };
}

export async function resolveActiveCore({
  dataRoot = resolveDataRoot(),
  platform = process.platform,
  architecture = process.arch
} = {}) {
  dataRoot = assertManagedRoot(dataRoot);
  await rejectSymlinkIfPresent(dataRoot, "AgentBell data root");
  const active = await loadActive(dataRoot, { optional: true });
  if (!active) {
    return null;
  }
  const target = resolveTarget(platform, architecture);
  if (active.target !== target.id) {
    throw new Error(
      `Active target ${active.target} does not match ${target.id}.`
    );
  }
  const installed = await installedMetadata(
    dataRoot,
    active.activeVersion,
    platform,
    target
  );
  if (installed.metadata.checksum !== active.checksum) {
    throw new Error("Active Core checksum differs from installed metadata.");
  }
  return {
    path: installed.corePath,
    version: active.activeVersion,
    generation: active.generation,
    target: active.target,
    checksum: active.checksum
  };
}

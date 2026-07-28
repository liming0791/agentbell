import { spawn } from "node:child_process";
import { createHash, X509Certificate } from "node:crypto";
import {
  chmod,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  stat,
  writeFile
} from "node:fs/promises";
import https from "node:https";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { findGo, root as repositoryRoot } from "./go-tool.mjs";
import { parseHTTPSRelaySmokeProof } from "./https-relay-proof.mjs";

const maximumCommandOutput = 1024 * 1024;

function appendBounded(chunks, chunk) {
  const used = chunks.reduce((total, value) => total + value.length, 0);
  if (used >= maximumCommandOutput) {
    return;
  }
  chunks.push(chunk.subarray(0, maximumCommandOutput - used));
}

async function run(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: options.cwd,
      env: options.env ?? process.env,
      shell: false,
      stdio: ["pipe", "pipe", "pipe"],
      windowsHide: true
    });
    const stdout = [];
    const stderr = [];
    child.stdout.on("data", (chunk) => appendBounded(stdout, chunk));
    child.stderr.on("data", (chunk) => appendBounded(stderr, chunk));
    child.once("error", reject);
    child.once("close", (code, signal) => {
      const output = Buffer.concat(stdout);
      const errorOutput = Buffer.concat(stderr);
      if (code !== 0 || signal) {
        reject(new Error(
          `${path.basename(command)} failed (${signal ?? code}): ` +
          errorOutput.toString("utf8").slice(0, 2048)
        ));
        return;
      }
      resolve(output);
    });
    child.stdin.end(options.input);
  });
}

function start(command, args, env) {
  const child = spawn(command, args, {
    env,
    shell: false,
    stdio: ["ignore", "pipe", "pipe"],
    windowsHide: true
  });
  const logs = [];
  child.stdout.on("data", (chunk) => appendBounded(logs, chunk));
  child.stderr.on("data", (chunk) => appendBounded(logs, chunk));
  return { child, logs };
}

async function stop(processValue) {
  if (!processValue || processValue.child.exitCode !== null) {
    return;
  }
  processValue.child.kill("SIGTERM");
  await Promise.race([
    new Promise((resolve) => processValue.child.once("close", resolve)),
    new Promise((_, reject) => setTimeout(
      () => reject(new Error("HTTPS smoke child shutdown timed out.")),
      5000
    ))
  ]);
}

async function freePort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  const port = typeof address === "object" && address ? address.port : 0;
  await new Promise((resolve, reject) => server.close((error) => {
    if (error) {
      reject(error);
    } else {
      resolve();
    }
  }));
  if (!port) {
    throw new Error("HTTPS smoke could not reserve a loopback port.");
  }
  return port;
}

function isolatedEnvironment(directory, certificatePath) {
  return {
    ...process.env,
    AGENTBELL_CONFIG: path.join(directory, "config.json"),
    AGENTBELL_DATA_DIR: path.join(directory, "data"),
    AGENTBELL_STATE_DIR: path.join(directory, "state"),
    AGENTBELL_LOG_DIR: path.join(directory, "logs"),
    HOME: path.join(directory, "home"),
    SSL_CERT_FILE: certificatePath
  };
}

async function writeBaseConfig(directory) {
  await mkdir(path.join(directory, "home"), {
    recursive: true,
    mode: 0o700
  });
  await writeFile(
    path.join(directory, "config.json"),
    `${JSON.stringify({
      defaultChannel: "isolated",
      larkCliPath: "/usr/bin/true",
      notifications: {
        events: ["task.completed", "task.failed", "agent.info"],
        includeSummary: false,
        privacyLevel: "metadata-only"
      },
      channels: [{
        id: "isolated",
        name: "Isolated smoke",
        type: "feishu",
        chatId: "not-a-real-chat",
        as: "bot"
      }]
    }, null, 2)}\n`,
    { mode: 0o600 }
  );
}

async function waitForTLS(port, certificatePath) {
  const ca = await readFile(certificatePath);
  const deadline = Date.now() + 10000;
  while (Date.now() < deadline) {
    try {
      await new Promise((resolve, reject) => {
        const request = https.get({
          hostname: "localhost",
          port,
          path: "/",
          ca,
          timeout: 1000
        }, (response) => {
          response.resume();
          response.once("end", resolve);
        });
        request.once("error", reject);
        request.once("timeout", () => request.destroy(
          new Error("HTTPS smoke TLS readiness timed out.")
        ));
      });
      return;
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
  }
  throw new Error("HTTPS smoke relay TLS listener did not become ready.");
}

async function parseJSON(executable, args, options = {}) {
  const raw = await run(executable, args, options);
  return JSON.parse(raw.toString("utf8"));
}

async function waitUntil(check, timeoutMilliseconds, label) {
  const deadline = Date.now() + timeoutMilliseconds;
  while (Date.now() < deadline) {
    try {
      const result = await check();
      if (result) {
        return result;
      }
    } catch {
      // Startup, disconnect and retry windows are expected and bounded.
    }
    await new Promise((resolve) => setTimeout(resolve, 200));
  }
  throw new Error(`HTTPS smoke ${label} timed out.`);
}

async function countJSONFiles(directory) {
  try {
    return (await readdir(directory)).filter((name) => name.endsWith(".json"))
      .length;
  } catch (error) {
    if (error?.code === "ENOENT") {
      return 0;
    }
    throw error;
  }
}

async function assertNotPersisted(directory, needles) {
  const entries = await readdir(directory, { withFileTypes: true });
  for (const entry of entries) {
    const entryPath = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      await assertNotPersisted(entryPath, needles);
      continue;
    }
    if (!entry.isFile() || (await stat(entryPath)).size > maximumCommandOutput) {
      continue;
    }
    const raw = await readFile(entryPath);
    for (const needle of needles) {
      if (raw.includes(needle)) {
        throw new Error(
          "HTTPS smoke found private Hook content in durable state."
        );
      }
    }
  }
}

async function buildSmokeCore(root) {
  const configured = process.env.AGENTBELL_HTTPS_SMOKE_BINARY;
  if (configured) {
    if (!path.isAbsolute(configured)) {
      throw new Error(
        "AGENTBELL_HTTPS_SMOKE_BINARY must be an absolute path."
      );
    }
    return configured;
  }
  const executable = path.join(root, "agentbell");
  const goExecutable = await findGo();
  await run(goExecutable, [
    "build",
    "-trimpath",
    "-o",
    executable,
    "./cmd/agentbell"
  ], {
    cwd: path.join(repositoryRoot, "core"),
    env: {
      ...process.env,
      CGO_ENABLED: "0"
    }
  });
  await chmod(executable, 0o700);
  return executable;
}

async function createCertificate(directory) {
  const certificatePath = path.join(directory, "cert.pem");
  const keyPath = path.join(directory, "key.pem");
  const configPath = path.join(directory, "openssl.cnf");
  await writeFile(configPath, `[req]
distinguished_name = dn
prompt = no
x509_extensions = v3_req
[dn]
CN = localhost
[v3_req]
basicConstraints = critical,CA:TRUE
keyUsage = critical,digitalSignature,keyEncipherment,keyCertSign
extendedKeyUsage = serverAuth
subjectAltName = @alt_names
[alt_names]
DNS.1 = localhost
IP.1 = 127.0.0.1
`, { mode: 0o600 });
  await run("openssl", [
    "req",
    "-x509",
    "-newkey",
    "rsa:2048",
    "-sha256",
    "-nodes",
    "-days",
    "1",
    "-keyout",
    keyPath,
    "-out",
    certificatePath,
    "-config",
    configPath
  ]);
  await chmod(certificatePath, 0o600);
  await chmod(keyPath, 0o600);
  const certificate = new X509Certificate(await readFile(certificatePath));
  const spki = certificate.publicKey.export({
    type: "spki",
    format: "der"
  });
  return {
    certificatePath,
    keyPath,
    pin: createHash("sha256").update(spki).digest("hex")
  };
}

export async function runHTTPSRelaySmoke() {
  if (process.platform !== "linux") {
    throw new Error(
      "The TLS trust-store smoke runs on Linux; use the Ubuntu CI job."
    );
  }
  const smokeRoot = await mkdtemp(path.join(
    os.tmpdir(),
    "agentbell-https-smoke-"
  ));
  await chmod(smokeRoot, 0o700);
  const relayRoot = path.join(smokeRoot, "relay");
  const remoteRoot = path.join(smokeRoot, "remote");
  const tlsRoot = path.join(smokeRoot, "tls");
  await Promise.all([
    mkdir(relayRoot, { recursive: true, mode: 0o700 }),
    mkdir(remoteRoot, { recursive: true, mode: 0o700 }),
    mkdir(tlsRoot, { recursive: true, mode: 0o700 })
  ]);

  let relayProcess;
  let serviceProcess;
  try {
    const [executable, certificate] = await Promise.all([
      buildSmokeCore(smokeRoot),
      createCertificate(tlsRoot)
    ]);
    const port = await freePort();
    const endpoint = `https://localhost:${port}`;
    const relayEnvironment = isolatedEnvironment(
      relayRoot,
      certificate.certificatePath
    );
    const remoteEnvironment = isolatedEnvironment(
      remoteRoot,
      certificate.certificatePath
    );
    await Promise.all([
      writeBaseConfig(relayRoot),
      writeBaseConfig(remoteRoot)
    ]);

    await run(executable, [
      "relay",
      "configure",
      "--listen",
      `127.0.0.1:${port}`,
      "--tls-cert",
      certificate.certificatePath,
      "--tls-key",
      certificate.keyPath,
      "--json"
    ], { env: relayEnvironment });
    const remoteOutbox = path.join(remoteRoot, "outbox");
    await run(executable, [
      "remote",
      "configure",
      "--team",
      "team-e2e",
      "--origin",
      "origin-https-e2e",
      "--runtime",
      "ssh",
      "--outbox",
      remoteOutbox,
      "--connector",
      "https",
      "--endpoint",
      `${endpoint}/v1/events`,
      "--pinned-spki",
      certificate.pin,
      "--key-file",
      path.join(remoteRoot, "secrets", "device.key"),
      "--acknowledge-file-fallback",
      "--json"
    ], { env: remoteEnvironment });

    relayProcess = start(executable, [
      "relay",
      "run",
      "--foreground"
    ], relayEnvironment);
    await waitForTLS(port, certificate.certificatePath);
    {
      const binding = await parseJSON(executable, [
        "relay",
        "bind",
        "create",
        "--team",
        "team-e2e",
        "--source",
        "codex",
        "--runtime",
        "ssh",
        "--json"
      ], { env: relayEnvironment });
      const pairingCode = binding.code;
      if (
        typeof pairingCode !== "string" ||
        !pairingCode.startsWith("AGBR-")
      ) {
        throw new Error("HTTPS smoke did not receive a valid pairing code.");
      }
      await run(executable, [
        "remote",
        "pair",
        "--code-stdin",
        "--json"
      ], {
        env: remoteEnvironment,
        input: `${pairingCode}\n`
      });
    }

    const peers = await parseJSON(executable, [
      "relay",
      "peers",
      "list",
      "--json"
    ], { env: relayEnvironment });
    if (!Array.isArray(peers) || peers.length !== 1 || peers[0].revoked) {
      throw new Error("HTTPS smoke expected one active relay peer.");
    }

    serviceProcess = start(executable, [
      "service",
      "run",
      "--foreground"
    ], remoteEnvironment);
    const probe = await parseJSON(executable, [
      "remote",
      "test",
      "--adapter",
      "codex",
      "--surface",
      "cli",
      "--wait",
      "20s",
      "--json"
    ], { env: remoteEnvironment });
    if (!probe.queued || !probe.acknowledged || probe.state !== "history") {
      throw new Error("HTTPS smoke probe was not durably acknowledged.");
    }

    await stop(relayProcess);
    relayProcess = undefined;
    const privateSession = "private-session-https-smoke";
    const privateSummary = "private-summary-https-smoke";
    const privateTurn = "private-turn-https-smoke";
    const rawHook = JSON.stringify({
      hook_event_name: "Stop",
      session_id: privateSession,
      turn_id: privateTurn,
      summary: privateSummary
    });
    for (let index = 0; index < 2; index += 1) {
      await run(executable, [
        "remote",
        "emit",
        "--adapter",
        "codex",
        "--surface",
        "cli",
        "--runtime",
        "ssh",
        "--stdin"
      ], { env: remoteEnvironment, input: rawHook });
    }

    relayProcess = start(executable, [
      "relay",
      "run",
      "--foreground"
    ], relayEnvironment);
    await waitForTLS(port, certificate.certificatePath);
    const receipts = await waitUntil(async () => {
      const value = await parseJSON(executable, [
        "relay",
        "receipts",
        "list",
        "--json"
      ], { env: relayEnvironment });
      return Array.isArray(value) && value.length === 2 ? value : undefined;
    }, 30000, "offline recovery");
    const remoteProof = await waitUntil(async () => {
      const doctor = await parseJSON(executable, ["doctor", "--json"], {
        env: remoteEnvironment
      });
      const proof = doctor?.relay?.runtimeProofs?.[0];
      return proof?.healthy && proof?.running ? proof : undefined;
    }, 10000, "runtime proof");

    const historyCount = await countJSONFiles(path.join(
      remoteOutbox,
      "history"
    ));
    if (historyCount !== 2 || receipts.length !== 2) {
      throw new Error("HTTPS smoke delivery or producer dedup count failed.");
    }
    await assertNotPersisted(smokeRoot, [
      Buffer.from(privateSession),
      Buffer.from(privateSummary),
      Buffer.from(privateTurn)
    ]);
    if (
      remoteProof.connector !== "https" ||
      remoteProof.state !== "healthy"
    ) {
      throw new Error("HTTPS smoke runtime proof is incomplete.");
    }

    const proof = [
      "HTTPS_RELAY_SMOKE_PASS",
      "paired=1",
      "deliveries=2",
      "history=2",
      "duplicate=1",
      "recovery=1",
      "metadata=1",
      "connector=https",
      "state=healthy",
      "running=1",
      "tls=verified",
      "spki=pinned"
    ].join(" ");
    parseHTTPSRelaySmokeProof(proof);
    return proof;
  } finally {
    await stop(serviceProcess).catch(() => {});
    await stop(relayProcess).catch(() => {});
    await rm(smokeRoot, { recursive: true, force: true });
  }
}

const isMain = process.argv[1] &&
  path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
  const proof = await runHTTPSRelaySmoke();
  process.stdout.write(`${proof}\n`);
}

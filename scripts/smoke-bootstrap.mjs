import {
  access,
  mkdtemp,
  readFile,
  rm
} from "node:fs/promises";
import http from "node:http";
import os from "node:os";
import path from "node:path";

import {
  installCore,
  runCore
} from "../packages/cli/src/core.mjs";
import { resolveTarget } from "../packages/cli/src/platform.mjs";

function argument(name, fallback) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : fallback;
}

const directory = path.resolve(argument("--directory", "artifacts/core"));
const version = argument("--version", "dev");
const target = resolveTarget();
const binary = await readFile(path.join(directory, target.fileName));
const checksums = await readFile(path.join(directory, "checksums.txt"));
const dataRoot = await mkdtemp(
  path.join(os.tmpdir(), "agentbell-bootstrap-smoke-")
);

const server = http.createServer((request, response) => {
  if (request.url?.endsWith("/checksums.txt")) {
    response.writeHead(200, { "content-type": "text/plain" });
    response.end(checksums);
    return;
  }
  if (request.url?.endsWith(`/${target.fileName}`)) {
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
  const installed = await installCore({
    version,
    releaseBase: `http://127.0.0.1:${address.port}`,
    dataRoot
  });
  const exitCode = await runCore(installed.path, ["version", "--json"]);
  if (exitCode !== 0) {
    throw new Error(`Installed Core smoke test exited with ${exitCode}.`);
  }
  await rm(dataRoot, { recursive: true, force: true });
  try {
    await access(installed.path);
    throw new Error("Bootstrap smoke uninstall left the Core binary behind.");
  } catch (error) {
    if (error?.code !== "ENOENT") {
      throw error;
    }
  }
  console.log(`Bootstrap smoke passed for ${version} (${target.id}).`);
} finally {
  await new Promise((resolve, reject) => {
    server.close((error) => error ? reject(error) : resolve());
  });
  await rm(dataRoot, { recursive: true, force: true });
}

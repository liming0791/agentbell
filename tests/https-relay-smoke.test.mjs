import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

import {
  parseHTTPSRelaySmokeProof
} from "../scripts/https-relay-proof.mjs";

const validProof = [
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

test("HTTPS relay smoke proof requires every security and durability invariant", () => {
  assert.deepEqual(parseHTTPSRelaySmokeProof(validProof), {
    paired: 1,
    deliveries: 2,
    history: 2,
    duplicate: true,
    recovery: true,
    metadataOnly: true,
    connector: "https",
    state: "healthy",
    running: true,
    tls: "verified",
    spki: "pinned"
  });

  for (const output of [
    "",
    validProof.replace("paired=1", "paired=2"),
    validProof.replace("deliveries=2", "deliveries=3"),
    validProof.replace("history=2", "history=1"),
    validProof.replace("duplicate=1", "duplicate=0"),
    validProof.replace("recovery=1", "recovery=0"),
    validProof.replace("metadata=1", "metadata=0"),
    validProof.replace("connector=https", "connector=http"),
    validProof.replace("state=healthy", "state=backoff"),
    validProof.replace("running=1", "running=0"),
    validProof.replace("tls=verified", "tls=skipped"),
    validProof.replace("spki=pinned", "spki=none"),
    `${validProof} endpoint=https://private.example`
  ]) {
    assert.throws(() => parseHTTPSRelaySmokeProof(output));
  }
});

test("Ubuntu CI owns the real TLS and HTTPS relay smoke", async () => {
  const workflow = await readFile(
    new URL("../.github/workflows/ci.yml", import.meta.url),
    "utf8"
  );
  assert.match(workflow, /\n[ ]{2}m2-https:\n/u);
  assert.match(workflow, /name: M2 TLS and HTTPS relay smoke/u);
  assert.match(workflow, /runs-on: ubuntu-latest/u);
  assert.match(workflow, /run: npm run smoke:https/u);
});

test("HTTPS smoke does not permit certificate verification bypass", async () => {
  const script = await readFile(
    new URL("../scripts/smoke-https-relay.mjs", import.meta.url),
    "utf8"
  );
  for (const forbidden of [
    "InsecureSkipVerify",
    "NODE_TLS_REJECT_UNAUTHORIZED",
    "--no-check-certificate",
    "rejectUnauthorized: false"
  ]) {
    assert.ok(!script.includes(forbidden), `forbidden TLS bypass: ${forbidden}`);
  }
});

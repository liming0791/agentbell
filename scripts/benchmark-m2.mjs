import { spawnSync } from "node:child_process";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { findGo, root } from "./go-tool.mjs";

const defaultStressItems = 96;
const proofPattern =
  /M2_STRESS_PASS items=(\d+) deliveries=(\d+) attempts=(\d+) recovered=(\d+)/u;

export function parseStressProof(output, expectedItems) {
  const match = proofPattern.exec(output);
  if (!match) {
    throw new Error("M2 relay stress proof marker is missing.");
  }
  const proof = {
    items: Number(match[1]),
    deliveries: Number(match[2]),
    attempts: Number(match[3]),
    recovered: Number(match[4])
  };
  if (
    !Number.isSafeInteger(expectedItems) ||
    expectedItems < 8 ||
    proof.items !== expectedItems ||
    proof.deliveries !== proof.items ||
    proof.attempts !== proof.items * 3 ||
    proof.recovered < 1 ||
    proof.recovered > proof.items
  ) {
    throw new Error(
      `M2 relay stress invariants failed: ${JSON.stringify(proof)}`
    );
  }
  return proof;
}

export function stressItemCount(environment = process.env) {
  const raw = environment.AGENTBELL_M2_STRESS_ITEMS;
  if (raw === undefined || raw === "") {
    return defaultStressItems;
  }
  if (!/^[0-9]+$/u.test(raw)) {
    throw new Error("AGENTBELL_M2_STRESS_ITEMS must be an integer.");
  }
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value < 8 || value > 512) {
    throw new Error(
      "AGENTBELL_M2_STRESS_ITEMS must be between 8 and 512."
    );
  }
  return value;
}

async function main() {
  const items = stressItemCount();
  const goExecutable = await findGo();
  const result = spawnSync(
    goExecutable,
    [
      "test",
      "-run",
      "^TestRelayDurableStressGate$",
      "-count=1",
      "-v",
      "./internal/relay"
    ],
    {
      cwd: path.join(root, "core"),
      encoding: "utf8",
      windowsHide: true,
      env: {
        ...process.env,
        AGENTBELL_M2_STRESS_ITEMS: String(items)
      }
    }
  );
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(
      `M2 relay stress test failed (${result.status}): ` +
        `${result.stderr || result.stdout || ""}`
    );
  }
  const proof = parseStressProof(result.stdout, items);
  console.log(
    "AgentBell M2 relay stress gate passed: " +
      `${proof.items} durable items, ${proof.deliveries} unique deliveries, ` +
      `${proof.attempts} bounded attempts, ${proof.recovered} crash recoveries.`
  );
}

const invokedPath = process.argv[1]
  ? pathToFileURL(path.resolve(process.argv[1])).href
  : "";
if (invokedPath === import.meta.url) {
  await main();
}

import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

import {
  parseStressProof,
  stressItemCount
} from "../scripts/benchmark-m2.mjs";

test("M2 stress proof requires exact semantic invariants", () => {
  assert.deepEqual(
    parseStressProof(
      "M2_STRESS_PASS items=96 deliveries=96 attempts=288 recovered=24",
      96
    ),
    {
      items: 96,
      deliveries: 96,
      attempts: 288,
      recovered: 24
    }
  );
  for (const output of [
    "",
    "M2_STRESS_PASS items=96 deliveries=95 attempts=288 recovered=24",
    "M2_STRESS_PASS items=96 deliveries=96 attempts=289 recovered=24",
    "M2_STRESS_PASS items=96 deliveries=96 attempts=288 recovered=0",
    "M2_STRESS_PASS items=95 deliveries=95 attempts=285 recovered=24"
  ]) {
    assert.throws(() => parseStressProof(output, 96));
  }
});

test("M2 stress item count is bounded and deterministic", () => {
  assert.equal(stressItemCount({}), 96);
  assert.equal(
    stressItemCount({ AGENTBELL_M2_STRESS_ITEMS: "128" }),
    128
  );
  for (const value of ["7", "513", "8.5", "-8", "secret"]) {
    assert.throws(() =>
      stressItemCount({ AGENTBELL_M2_STRESS_ITEMS: value })
    );
  }
});

test("Ubuntu CI runs the M2 durable relay stress gate", async () => {
  const workflow = await readFile(
    new URL("../.github/workflows/ci.yml", import.meta.url),
    "utf8"
  );
  assert.match(workflow, /\n[ ]{2}m2-stress:\n/u);
  assert.match(workflow, /runs-on: ubuntu-latest/u);
  assert.match(workflow, /run: node scripts\/benchmark-m2\.mjs/u);
  assert.doesNotMatch(
    workflow.match(/\n {2}m2-stress:\n[\s\S]*?(?=\n {2}[a-z0-9-]+:\n|$)/u)?.[0] ?? "",
    /npm run perf:m2/u
  );
  assert.match(workflow, /AGENTBELL_M2_STRESS_ITEMS: "96"/u);
});

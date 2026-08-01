import assert from "node:assert/strict";
import test from "node:test";
import { setImmediate } from "node:timers/promises";

import {
  releaseArtifacts,
  releaseTargets
} from "../scripts/build-targets.mjs";
import {
  buildConcurrency,
  runBounded
} from "../scripts/build-core.mjs";

test("defines six Core and bridge release targets", () => {
  assert.equal(releaseTargets.length, 6);
  const artifacts = releaseTargets.flatMap((target) =>
    releaseArtifacts(target)
  );
  assert.equal(artifacts.length, 12);
  assert.equal(new Set(artifacts.map((artifact) => artifact.fileName)).size, 12);

  for (const target of releaseTargets) {
    const targetArtifacts = releaseArtifacts(target);
    assert.deepEqual(
      targetArtifacts.map((artifact) => artifact.command),
      ["./cmd/agentbell", "./cmd/agentbell-bridge"]
    );
    assert.ok(
      targetArtifacts[0].fileName.startsWith(
        `agentbell-${target.goos}-${target.goarch}`
      )
    );
    assert.ok(
      targetArtifacts[1].fileName.startsWith(
        `agentbell-bridge-${target.goos}-${target.goarch}`
      )
    );
    const expectedExtension = target.goos === "windows" ? ".exe" : "";
    assert.ok(
      targetArtifacts.every((artifact) =>
        artifact.fileName.endsWith(expectedExtension)
      )
    );
  }
});

test("rejects unsupported release targets", () => {
  assert.throws(
    () => releaseArtifacts({ goos: "plan9", goarch: "amd64" }),
    /unsupported release target/i
  );
});

test("release builds use deterministic bounded concurrency", async () => {
  assert.equal(buildConcurrency({}), 2);
  assert.equal(
    buildConcurrency({ AGENTBELL_BUILD_CONCURRENCY: "3" }),
    3
  );
  for (const value of ["0", "7", "1.5", "fast"]) {
    assert.throws(() =>
      buildConcurrency({ AGENTBELL_BUILD_CONCURRENCY: value })
    );
  }

  let active = 0;
  let maximum = 0;
  const completed = [];
  await runBounded(
    [0, 1, 2, 3, 4, 5],
    2,
    async (value) => {
      active += 1;
      maximum = Math.max(maximum, active);
      await setImmediate();
      completed.push(value);
      active -= 1;
    }
  );
  assert.equal(maximum, 2);
  assert.deepEqual(completed.sort((left, right) => left - right), [
    0,
    1,
    2,
    3,
    4,
    5
  ]);
});

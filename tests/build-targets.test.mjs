import assert from "node:assert/strict";
import test from "node:test";
import { setImmediate } from "node:timers/promises";

import {
  releaseArtifacts,
  releaseTargets
} from "../scripts/build-targets.mjs";
import {
  artifactLDFlags,
  buildConcurrency,
  runBounded
} from "../scripts/build-core.mjs";

test("defines six Core and bridge targets plus Windows service entries", () => {
  assert.equal(releaseTargets.length, 6);
  const artifacts = releaseTargets.flatMap((target) =>
    releaseArtifacts(target)
  );
  assert.equal(artifacts.length, 14);
  assert.equal(new Set(artifacts.map((artifact) => artifact.fileName)).size, 14);

  for (const target of releaseTargets) {
    const targetArtifacts = releaseArtifacts(target);
    assert.deepEqual(targetArtifacts.map((artifact) => artifact.command),
      target.goos === "windows"
        ? ["./cmd/agentbell", "./cmd/agentbell-bridge", "./cmd/agentbell-bridge"]
        : ["./cmd/agentbell", "./cmd/agentbell-bridge"]);
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
    if (target.goos === "windows") {
      assert.ok(
        targetArtifacts[2].fileName.startsWith(
          `agentbell-service-${target.goos}-${target.goarch}`
        )
      );
    }
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

test("only the Windows service entry uses the GUI subsystem", () => {
  const base = "-s -w -X example.Version=test";
  for (const target of releaseTargets) {
    const [core, bridge, service] = releaseArtifacts(target);
    const coreFlags = artifactLDFlags(base, target, core);
    const bridgeFlags = artifactLDFlags(base, target, bridge);
    assert.equal(coreFlags, base);
    assert.equal(bridgeFlags, base);
    if (target.goos === "windows") {
      assert.equal(artifactLDFlags(base, target, service), `${base} -H=windowsgui`);
    }
  }
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

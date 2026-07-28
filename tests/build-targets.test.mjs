import assert from "node:assert/strict";
import test from "node:test";

import {
  releaseArtifacts,
  releaseTargets
} from "../scripts/build-targets.mjs";

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

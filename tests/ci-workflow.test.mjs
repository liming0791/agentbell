import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const ciURL = new URL("../.github/workflows/ci.yml", import.meta.url);
const docsURL = new URL("../.github/workflows/docs.yml", import.meta.url);

test("CI reserves expensive jobs for code-ready changes and direct pushes", async () => {
  const workflow = await readFile(ciURL, "utf8");
  for (const required of [
    "ready_for_review",
    "converted_to_draft",
    "paths-ignore:",
    "- \"**/*.md\"",
    "github.event.pull_request.draft == false",
    "!startsWith(github.event.head_commit.message, 'Merge pull request #')"
  ]) {
    assert.ok(workflow.includes(required), `missing CI gate: ${required}`);
  }

  const fullGateMatches = workflow.match(
    /github\.event\.pull_request\.draft == false/g
  );
  assert.ok(
    (fullGateMatches?.length ?? 0) >= 5,
    "every expensive job must use the full-CI gate"
  );
});

test("CI avoids duplicate Node and Go matrices while preserving platform gates", async () => {
  const workflow = await readFile(ciURL, "utf8");
  const compatibility = workflow.match(
    /\n {2}compatibility:\n[\s\S]*?(?=\n {2}[a-z0-9-]+:\n|$)/
  )?.[0];
  const quality = workflow.match(
    /\n {2}quality:\n[\s\S]*?(?=\n {2}[a-z0-9-]+:\n|$)/
  )?.[0];
  assert.ok(compatibility);
  assert.ok(quality);
  assert.doesNotMatch(compatibility, /node: 24/u);
  assert.match(compatibility, /node: 20/u);
  assert.match(compatibility, /node: 22/u);
  assert.match(compatibility, /platform: true/u);
  assert.match(compatibility, /if: matrix\.platform == true/u);
  assert.match(compatibility, /npm run test:migrations/u);
  assert.match(compatibility, /npm run perf:emit/u);
  assert.match(quality, /npm run test:migrations/u);
  assert.match(quality, /npm run perf:emit/u);
});

test("Markdown-only changes use a lightweight docs workflow", async () => {
  const workflow = await readFile(docsURL, "utf8");
  assert.match(workflow, /paths:\n(?:.*\n)*?[ ]{6}- "\*\*\/\*\.md"/u);
  assert.match(workflow, /npm run check:docs/u);
  assert.doesNotMatch(workflow, /npm ci|setup-go|go test/u);
  assert.match(workflow, /cancel-in-progress: true/u);
});

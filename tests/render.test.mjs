import assert from "node:assert/strict";
import { test } from "node:test";

import { renderText } from "../packages/hook-runtime/src/render.mjs";

const notification = {
  source: "codex",
  status: "completed",
  event: "Stop",
  project: "agentbell",
  summary: "All checks passed."
};

test("renders project metadata without summaries by default", () => {
  const text = renderText(notification, {
    notifications: {
      includeSummary: false
    }
  });

  assert.match(text, /✅ AgentBell · Codex/);
  assert.match(text, /项目：agentbell/);
  assert.doesNotMatch(text, /摘要：/);
});

test("includes summaries when configured", () => {
  const text = renderText(notification, {
    notifications: {
      includeSummary: true
    }
  });

  assert.match(text, /摘要：All checks passed\./);
});

import assert from "node:assert/strict";
import { test } from "node:test";

import { normalizeEvent } from "../packages/hook-runtime/src/normalize.mjs";

test("normalizes a completed Codex event", () => {
  const notification = normalizeEvent(
    "codex",
    JSON.stringify({
      hook_event_name: "Stop",
      cwd: "/work/agentbell",
      thread_id: "thread-1",
      last_assistant_message: "  Task   complete.  "
    })
  );

  assert.equal(notification.version, "1");
  assert.equal(notification.source, "codex");
  assert.equal(notification.event, "Stop");
  assert.equal(notification.status, "completed");
  assert.equal(notification.project, "agentbell");
  assert.equal(notification.sessionId, "thread-1");
  assert.equal(notification.summary, "Task complete.");
  assert.match(notification.occurredAt, /^\d{4}-\d{2}-\d{2}T/);
});

test("maps failure and permission events to their statuses", () => {
  assert.equal(
    normalizeEvent("claude", '{"event":"StopFailure"}').status,
    "failed"
  );
  assert.equal(
    normalizeEvent("kimi", '{"event":"PermissionRequest"}').status,
    "attention"
  );
});

test("accepts plain text and truncates long summaries", () => {
  const notification = normalizeEvent("codex", "x".repeat(350));

  assert.equal(notification.event, "Unknown");
  assert.equal(notification.status, "info");
  assert.equal(notification.summary.length, 300);
});

test("rejects unsupported hook sources", () => {
  assert.throws(
    () => normalizeEvent("unknown", "{}"),
    /Unsupported hook source/
  );
});

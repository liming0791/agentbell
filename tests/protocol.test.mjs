import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { URL } from "node:url";

const fixtureUrl = new URL(
  "../core/testdata/notification-event.golden.json",
  import.meta.url
);
const schemaUrl = new URL(
  "../schemas/notification-event.schema.json",
  import.meta.url
);

test("NotificationEvent golden fixture matches the shared schema", async () => {
  const fixture = JSON.parse(await readFile(fixtureUrl, "utf8"));
  const schema = JSON.parse(await readFile(schemaUrl, "utf8"));

  for (const field of schema.required) {
    assert.ok(Object.hasOwn(fixture, field), `missing required field: ${field}`);
  }
  for (const field of Object.keys(fixture)) {
    assert.ok(Object.hasOwn(schema.properties, field), `unknown field: ${field}`);
  }
  for (const [field, definition] of Object.entries(schema.properties)) {
    if (Object.hasOwn(fixture, field) && definition.enum) {
      assert.ok(
        definition.enum.includes(fixture[field]),
        `${field} is outside its enum`
      );
    }
    if (Object.hasOwn(fixture, field) && definition.const !== undefined) {
      assert.equal(fixture[field], definition.const);
    }
  }

  assert.equal(
    new Date(fixture.occurredAt).toISOString(),
    "2026-07-23T04:00:00.000Z"
  );
  assert.match(fixture.idempotencyKey, /^sha256:[a-f0-9]{64}$/);
  assert.match(fixture.sessionId, /^sha256:[a-f0-9]{16}$/);
  assert.equal(fixture.cwd, undefined);
  assert.equal(fixture.summary, undefined);
});

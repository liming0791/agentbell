import assert from "node:assert/strict";
import { test } from "node:test";

import { buildSetupPlan } from "../packages/cli/src/plan.mjs";

test("plans lark-cli installation and detected agent plugins", () => {
  const actions = buildSetupPlan({
    larkCli: { installed: false },
    agents: {
      codex: true,
      claude: false,
      kimi: true
    }
  });

  assert.deepEqual(
    actions.map((action) => action.id),
    [
      "install-lark-cli",
      "configure-lark-cli",
      "login-lark-cli",
      "install-codex-plugin",
      "install-kimi-plugin"
    ]
  );
  assert.ok(actions.every((action) => action.requiresConfirmation));
});

test("does not reinstall lark-cli when it is already available", () => {
  const actions = buildSetupPlan({
    larkCli: { installed: true },
    agents: {
      codex: false,
      claude: false,
      kimi: false
    }
  });

  assert.deepEqual(
    actions.map((action) => action.id),
    ["configure-lark-cli", "login-lark-cli"]
  );
});

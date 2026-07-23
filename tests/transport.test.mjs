import assert from "node:assert/strict";
import { test } from "node:test";

import {
  sendWithLarkCli
} from "../packages/hook-runtime/src/transport/lark-cli.mjs";

test("requires a Feishu chat id before spawning lark-cli", async () => {
  await assert.rejects(
    async () => sendWithLarkCli({}, "hello"),
    /has no chatId/
  );
});

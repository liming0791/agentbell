import { spawnSync } from "node:child_process";
import path from "node:path";
import { findGo, root } from "./go-tool.mjs";

const goExecutable = await findGo();
const result = spawnSync(
  goExecutable,
  [
    "test",
    "./internal/config",
    "./internal/queue",
    "./internal/adapter",
    "-run=^TestMigrationFixture",
    "-count=1"
  ],
  {
    cwd: path.join(root, "core"),
    stdio: "inherit"
  }
);

if (result.error) {
  throw result.error;
}
if (result.status !== 0) {
  throw new Error(`Migration fixture tests failed with status ${result.status}.`);
}

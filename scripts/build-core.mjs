import { spawnSync } from "node:child_process";
import { mkdir } from "node:fs/promises";
import path from "node:path";

import { findGo, root } from "./go-tool.mjs";

const targets = [
  ["windows", "amd64"],
  ["windows", "arm64"],
  ["darwin", "amd64"],
  ["darwin", "arm64"],
  ["linux", "amd64"],
  ["linux", "arm64"]
];

function argument(name, fallback) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : fallback;
}

const output = path.resolve(argument("--output", path.join(root, "artifacts", "core")));
const version = argument("--version", "dev");
const commit = argument("--commit", "none");
const buildTime = argument("--build-time", new Date().toISOString());
const goExecutable = await findGo();
const core = path.join(root, "core");
const ldflags = [
  "-s",
  "-w",
  `-X github.com/liming0791/agentbell/core/internal/version.Version=${version}`,
  `-X github.com/liming0791/agentbell/core/internal/version.Commit=${commit}`,
  `-X github.com/liming0791/agentbell/core/internal/version.BuildTime=${buildTime}`
].join(" ");

await mkdir(output, { recursive: true });

for (const [goos, goarch] of targets) {
  const extension = goos === "windows" ? ".exe" : "";
  const fileName = `agentbell-${goos}-${goarch}${extension}`;
  const result = spawnSync(
    goExecutable,
    [
      "build",
      "-trimpath",
      "-ldflags",
      ldflags,
      "-o",
      path.join(output, fileName),
      "./cmd/agentbell"
    ],
    {
      cwd: core,
      env: {
        ...process.env,
        CGO_ENABLED: "0",
        GOOS: goos,
        GOARCH: goarch
      },
      stdio: "inherit",
      windowsHide: true
    }
  );
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(`Failed to build ${goos}/${goarch}.`);
  }
  console.log(`Built ${fileName}`);
}

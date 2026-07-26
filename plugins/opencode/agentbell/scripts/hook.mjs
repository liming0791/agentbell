import { spawn } from "node:child_process";

const allowedSources = new Set(["opencode"]);
const requestedSource = process.argv[2];
const source = allowedSources.has(requestedSource) ? requestedSource : "opencode";
const chunks = [];

for await (const chunk of process.stdin) {
  chunks.push(chunk);
}

const input = Buffer.concat(chunks);
const windows = process.platform === "win32";
const child = windows
  ? spawn(process.env.ComSpec || "cmd.exe", ["/d", "/s", "/c", `agentbell-hook --source ${source}`], {
      stdio: ["pipe", "ignore", "ignore"],
      windowsHide: true
    })
  : spawn("agentbell-hook", ["--source", source], {
      stdio: ["pipe", "ignore", "ignore"]
    });

child.on("error", () => process.exit(0));
child.stdin.on("error", () => {});
child.once("close", () => process.exit(0));
child.stdin.end(input);

import { spawn } from "node:child_process";

export function sendWithLarkCli(channel, text, timeoutMs = 15_000) {
  if (!channel?.chatId) {
    throw new Error("The selected Feishu channel has no chatId.");
  }

  const args = [
    "im",
    "+messages-send",
    "--chat-id",
    channel.chatId,
    "--text",
    text,
    "--as",
    channel.as === "user" ? "user" : "bot",
    "--format",
    "json"
  ];

  return new Promise((resolve, reject) => {
    const child = spawn("lark-cli", args, {
      shell: false,
      windowsHide: true,
      stdio: ["ignore", "ignore", "pipe"]
    });

    let stderr = "";
    child.stderr.on("data", (chunk) => {
      stderr = `${stderr}${chunk}`.slice(-4000);
    });

    const timeout = setTimeout(() => {
      child.kill();
      reject(new Error("lark-cli notification timed out."));
    }, timeoutMs);

    child.once("error", (error) => {
      clearTimeout(timeout);
      reject(error);
    });

    child.once("close", (code) => {
      clearTimeout(timeout);
      if (code === 0) {
        resolve();
      } else {
        reject(new Error(stderr.trim() || `lark-cli exited with code ${code}.`));
      }
    });
  });
}


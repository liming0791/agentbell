#!/usr/bin/env node

import { handleHook } from "../src/index.mjs";

async function readStdin() {
  const chunks = [];
  for await (const chunk of process.stdin) {
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString("utf8");
}

function parseArgs(args) {
  const sourceIndex = args.indexOf("--source");
  return {
    source: sourceIndex >= 0 ? args[sourceIndex + 1] : args[0],
    dryRun: args.includes("--dry-run")
  };
}

const options = parseArgs(process.argv.slice(2));

try {
  const result = await handleHook({
    source: options.source,
    rawInput: await readStdin(),
    dryRun: options.dryRun
  });

  if (options.dryRun || process.env.AGENTBELL_DEBUG === "1") {
    console.log(JSON.stringify(result, null, 2));
  }
} catch (error) {
  if (process.env.AGENTBELL_DEBUG === "1") {
    console.error(error instanceof Error ? error.message : String(error));
  }
}


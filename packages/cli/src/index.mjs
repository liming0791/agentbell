import { detectEnvironment } from "./detect.mjs";
import { buildSetupPlan } from "./plan.mjs";

function printHelp() {
  console.log(`AgentBell

Usage:
  agentbell doctor
  agentbell setup --plan

The scaffold currently provides read-only detection and planning.`);
}

function printEnvironment(environment) {
  console.log(JSON.stringify(environment, null, 2));
}

export async function run(args) {
  const [command, ...options] = args;

  if (!command || command === "help" || command === "--help" || command === "-h") {
    printHelp();
    return;
  }

  const environment = detectEnvironment();

  if (command === "doctor") {
    printEnvironment(environment);
    return;
  }

  if (command === "setup" && options.includes("--plan")) {
    console.log(JSON.stringify({
      environment,
      actions: buildSetupPlan(environment)
    }, null, 2));
    return;
  }

  throw new Error(`Unsupported command: ${args.join(" ")}`);
}


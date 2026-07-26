import { createRequire } from "node:module";

import { detectEnvironment } from "./detect.mjs";
import { buildSetupPlan } from "./plan.mjs";
import {
  coreInstallPath,
  installCore,
  runCore,
  uninstallCore
} from "./core.mjs";

const require = createRequire(import.meta.url);
const packageMetadata = require("../package.json");

function printHelp() {
  console.log(`AgentBell

Usage:
  agentbell bootstrap-doctor
  agentbell setup --plan
  agentbell install-core [--version <version>]
  agentbell core-path [--version <version>]
  agentbell <setup|test|version|emit|service|doctor|queue|adapter|uninstall> ...

"setup --plan" prints a local dry-run plan without the Core; plain "setup"
and "test" are forwarded to the installed Core.

The npm package bootstraps the native AgentBell Core.`);
}

function printEnvironment(environment) {
  console.log(JSON.stringify(environment, null, 2));
}

export function booleanFlagEnabled(options, name) {
  for (const option of options) {
    if (option === name) {
      return true;
    }
    if (option.startsWith(`${name}=`)) {
      return !["false", "0"].includes(
        option.slice(name.length + 1).toLowerCase()
      );
    }
  }
  return false;
}

export async function run(args, dependencies = {}) {
  const [command, ...options] = args;

  if (!command || command === "help" || command === "--help" || command === "-h") {
    printHelp();
    return;
  }

  const environment = detectEnvironment();

  if (command === "bootstrap-doctor") {
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

  if (command === "install-core") {
    const versionIndex = options.indexOf("--version");
    const version = versionIndex >= 0
      ? options[versionIndex + 1]
      : packageMetadata.version;
    const result = await installCore({ version });
    console.log(JSON.stringify(result, null, 2));
    return;
  }

  if (command === "core-path") {
    const versionIndex = options.indexOf("--version");
    const version = versionIndex >= 0
      ? options[versionIndex + 1]
      : packageMetadata.version;
    console.log(coreInstallPath({ version }));
    return;
  }

  if ([
    "setup",
    "test",
    "emit",
    "service",
    "doctor",
    "queue",
    "adapter",
    "version",
    "uninstall"
  ].includes(command)) {
    const version = packageMetadata.version;
    const executable = coreInstallPath({ version });
    let exitCode;
    try {
      exitCode = await (dependencies.runCore || runCore)(
        executable,
        [command, ...options]
      );
    } catch (error) {
      if (error?.code === "ENOENT") {
        throw new Error(
          `AgentBell Core ${version} is not installed. Run "agentbell install-core".`,
          { cause: error }
        );
      }
      throw error;
    }
    if (exitCode !== 0) {
      throw new Error(`AgentBell Core exited with code ${exitCode}.`);
    }
    if (command === "uninstall" && !booleanFlagEnabled(options, "--dry-run")) {
      const result = await (dependencies.uninstallCore || uninstallCore)({
        version
      });
      if (!booleanFlagEnabled(options, "--json")) {
        console.log(
          result.removed
            ? `Removed managed AgentBell Core ${version}.`
            : `Managed AgentBell Core ${version} was already absent.`
        );
      }
    }
    return;
  }

  throw new Error(`Unsupported command: ${args.join(" ")}`);
}

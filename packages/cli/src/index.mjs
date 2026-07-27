import { createRequire } from "node:module";

import { detectEnvironment } from "./detect.mjs";
import { buildSetupPlan } from "./plan.mjs";
import {
  coreInstallPath,
  runCore,
  uninstallCore
} from "./core.mjs";
import {
  listVersions,
  resolveActiveCore,
  rollback,
  upgrade
} from "./upgrade.mjs";

const require = createRequire(import.meta.url);
const packageMetadata = require("../package.json");

function printHelp() {
  console.log(`AgentBell

Usage:
  agentbell bootstrap-doctor
  agentbell setup --plan
  agentbell install-core [--version <version>]
  agentbell core-path [--version <version>]
  agentbell upgrade [--from <legacy-version>] [--to <version>] [--channel <stable|next>] [--dry-run] [--json]
  agentbell rollback [--to <version>] [--dry-run] [--json]
  agentbell versions [--json]
  agentbell <setup|test|version|emit|service|doctor|queue|settings|policy|bind|bridge|hook|plugin|relay|remote|adapter|uninstall> ...

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

function optionValue(options, name, fallback) {
  const index = options.indexOf(name);
  if (index < 0) {
    return fallback;
  }
  const value = options[index + 1];
  if (!value || value.startsWith("--")) {
    throw new Error(`${name} requires a value.`);
  }
  return value;
}

function printTransactionResult(result, asJSON) {
  if (asJSON) {
    console.log(JSON.stringify(result, null, 2));
    return;
  }
  if (result.dryRun) {
    console.log(
      `AgentBell ${result.operation} plan: ${result.fromVersion || "none"} -> ${result.toVersion}.`
    );
    return;
  }
  console.log(
    `AgentBell ${result.activeVersion} is active (generation ${result.generation}).`
  );
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
    const active = await (
      dependencies.resolveActiveCore || resolveActiveCore
    )();
    if (active && active.version !== version) {
      throw new Error(
        `AgentBell Core ${active.version} is already active; ` +
        `use "agentbell upgrade --to ${version}" instead.`
      );
    }
    const result = await (dependencies.upgrade || upgrade)({
      toVersion: version,
      channel: "stable",
      dryRun: false,
      // A first install has no registered login service yet. setup installs
      // the stable-bridge service after configuration and Hook installation.
      restartService: async () => {}
    });
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

  if (command === "upgrade") {
    const fromVersion = optionValue(options, "--from", undefined);
    const result = await (dependencies.upgrade || upgrade)({
      toVersion: optionValue(options, "--to", packageMetadata.version),
      ...(fromVersion === undefined ? {} : { fromVersion }),
      channel: optionValue(options, "--channel", "stable"),
      dryRun: booleanFlagEnabled(options, "--dry-run")
    });
    printTransactionResult(
      result,
      booleanFlagEnabled(options, "--json")
    );
    return;
  }

  if (command === "rollback") {
    const result = await (dependencies.rollback || rollback)({
      toVersion: optionValue(options, "--to", undefined),
      dryRun: booleanFlagEnabled(options, "--dry-run")
    });
    printTransactionResult(
      result,
      booleanFlagEnabled(options, "--json")
    );
    return;
  }

  if (command === "versions") {
    const unexpected = options.filter((option) => option !== "--json");
    if (unexpected.length > 0) {
      throw new Error(`Unsupported versions option: ${unexpected[0]}`);
    }
    const result = await (dependencies.listVersions || listVersions)();
    if (booleanFlagEnabled(options, "--json")) {
      console.log(JSON.stringify(result, null, 2));
    } else {
      console.log(`Active AgentBell: ${result.activeVersion || "none"}`);
      for (const installed of result.installed) {
        console.log(
          `${installed.active ? "*" : " "} ${installed.version}` +
          `${installed.invalid ? " (invalid)" : ""}`
        );
      }
    }
    return;
  }

  if ([
    "setup",
    "test",
    "emit",
    "service",
    "doctor",
    "queue",
    "settings",
    "policy",
    "bind",
    "bridge",
    "hook",
    "plugin",
    "relay",
    "remote",
    "adapter",
    "version",
    "uninstall"
  ].includes(command)) {
    const active = await (
      dependencies.resolveActiveCore || resolveActiveCore
    )();
    const version = active?.version || packageMetadata.version;
    const executable = active?.path || coreInstallPath({ version });
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

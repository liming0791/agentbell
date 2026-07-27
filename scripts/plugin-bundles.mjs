import { createHash } from "node:crypto";
import {
  copyFile,
  mkdir,
  readFile,
  readdir,
  stat,
  writeFile
} from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const releasePlugins = Object.freeze([
  Object.freeze({
    name: "codex",
    pluginId: "agentbell.codex",
    source: "plugins/codex/agentbell",
    productManifest: ".codex-plugin/plugin.json",
    files: [
      ".codex-plugin/plugin.json",
      "hooks/hooks.json",
      "scripts/hook.mjs",
      "skills/agentbell-setup/SKILL.md"
    ]
  }),
  Object.freeze({
    name: "claude-code",
    pluginId: "agentbell.claude-code",
    source: "plugins/claude/agentbell",
    productManifest: ".claude-plugin/plugin.json",
    files: [
      ".claude-plugin/plugin.json",
      "hooks/hooks.json",
      "scripts/hook.mjs",
      "skills/agentbell-setup/SKILL.md"
    ]
  }),
  Object.freeze({
    name: "kimi-code",
    pluginId: "agentbell.kimi-code",
    source: "plugins/kimi/agentbell",
    productManifest: "kimi.plugin.json",
    files: [
      "kimi.plugin.json",
      "scripts/hook.mjs",
      "skills/agentbell-setup/SKILL.md"
    ]
  }),
  Object.freeze({
    name: "opencode",
    pluginId: "agentbell.opencode",
    source: "plugins/opencode/agentbell",
    productManifest: "opencode.plugin.json",
    files: [
      "opencode.plugin.json",
      "scripts/hook.mjs",
      "skills/agentbell-setup/SKILL.md"
    ]
  }),
  Object.freeze({
    name: "qoder",
    pluginId: "agentbell.qoder",
    source: "plugins/qoder/agentbell",
    productManifest: ".qoder-plugin/plugin.json",
    files: [
      ".qoder-plugin/plugin.json",
      "hooks/hooks.json",
      "scripts/hook.mjs",
      "skills/agentbell-setup/SKILL.md"
    ]
  })
]);

const manifestName = "plugin-manifest.json";
const signatureBundleName = "plugin.sigstore.json";
const signedStatus = "sigstore-verified";

export async function buildPluginBundles({
  rootDirectory,
  outputDirectory,
  version,
  minCoreVersion = version,
  maxCoreVersion = version,
  signatureStatus = signedStatus,
  plugins = releasePlugins
}) {
  const root = path.resolve(rootDirectory);
  const output = path.resolve(outputDirectory);
  if (!validSemanticVersion(version) ||
      !validSemanticVersion(minCoreVersion) ||
      !validSemanticVersion(maxCoreVersion)) {
    throw new Error("Plugin and Core compatibility versions must be semantic versions.");
  }
  if (signatureStatus !== signedStatus) {
    throw new Error(
      `Release plugin bundles must use ${signedStatus}; refusing ${signatureStatus}.`
    );
  }
  if (output === root || output.startsWith(`${root}${path.sep}`) === false) {
    throw new Error("Plugin bundle output must be a dedicated directory inside the repository.");
  }
  await mkdir(output, { recursive: false });

  const results = [];
  for (const plugin of plugins) {
    validatePluginDefinition(plugin);
    const sourceRoot = resolveInside(root, plugin.source);
    const sourceInfo = await stat(sourceRoot);
    if (!sourceInfo.isDirectory()) {
      throw new Error(`Plugin source is not a directory: ${plugin.source}`);
    }
    const productManifestPath = resolveInside(sourceRoot, plugin.productManifest);
    const productManifest = JSON.parse(await readFile(productManifestPath, "utf8"));
    if (productManifest.version !== version) {
      throw new Error(
        `${plugin.productManifest} has version ${productManifest.version}; expected ${version}.`
      );
    }

    const directoryName = `agentbell-plugin-${plugin.name}-${version}`;
    const bundleRoot = resolveInside(output, directoryName);
    await mkdir(bundleRoot, { recursive: false });
    const files = [];
    await copyPluginTree(sourceRoot, bundleRoot, "", files);
    files.sort((left, right) => left.path.localeCompare(right.path, "en"));
    const actualPaths = files.map((file) => file.path);
    const expectedPaths = [...plugin.files].sort((left, right) =>
      left.localeCompare(right, "en")
    );
    if (JSON.stringify(actualPaths) !== JSON.stringify(expectedPaths)) {
      throw new Error(
        `${plugin.name} plugin file set differs from the release allowlist.`
      );
    }
    const manifest = {
      version: 1,
      pluginId: plugin.pluginId,
      pluginVersion: version,
      minCoreVersion,
      maxCoreVersion,
      signatureStatus,
      files
    };
    const manifestBytes = `${JSON.stringify(manifest, null, 2)}\n`;
    await writeFile(
      path.join(bundleRoot, manifestName),
      manifestBytes,
      { encoding: "utf8", mode: 0o600, flag: "wx" }
    );
    results.push({
      name: plugin.name,
      pluginId: plugin.pluginId,
      directoryName,
      bundleRoot,
      manifestPath: path.join(bundleRoot, manifestName),
      signatureBundlePath: path.join(bundleRoot, signatureBundleName),
      files: files.length
    });
  }
  return results;
}

async function copyPluginTree(sourceRoot, destinationRoot, relative, files) {
  const current = relative === "" ? sourceRoot : resolveInside(sourceRoot, relative);
  const entries = await readdir(current, { withFileTypes: true });
  entries.sort((left, right) => left.name.localeCompare(right.name, "en"));
  for (const entry of entries) {
    const childRelative = relative === ""
      ? entry.name
      : `${relative}/${entry.name}`;
    if (entry.isSymbolicLink()) {
      throw new Error(`Plugin source contains a symlink: ${childRelative}`);
    }
    const sourcePath = resolveInside(sourceRoot, childRelative);
    const destinationPath = resolveInside(destinationRoot, childRelative);
    if (entry.isDirectory()) {
      await mkdir(destinationPath, { mode: 0o700 });
      await copyPluginTree(sourceRoot, destinationRoot, childRelative, files);
      continue;
    }
    if (!entry.isFile()) {
      throw new Error(`Plugin source contains a non-regular file: ${childRelative}`);
    }
    if (childRelative === manifestName || childRelative === signatureBundleName) {
      throw new Error(`Plugin source contains reserved control file: ${childRelative}`);
    }
    const value = await readFile(sourcePath);
    await copyFile(sourcePath, destinationPath);
    files.push({
      path: childRelative,
      sha256: `sha256:${createHash("sha256").update(value).digest("hex")}`,
      size: value.length
    });
  }
}

function resolveInside(root, relative) {
  validateRelativePath(relative);
  const candidate = path.resolve(root, ...relative.split("/"));
  if (!candidate.startsWith(`${root}${path.sep}`)) {
    throw new Error(`Plugin path escapes its root: ${relative}`);
  }
  return candidate;
}

function validatePluginDefinition(plugin) {
  if (!plugin ||
      !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(plugin.name) ||
      !/^agentbell\.[a-z0-9]+(?:-[a-z0-9]+)*$/.test(plugin.pluginId) ||
      !Array.isArray(plugin.files) ||
      plugin.files.length === 0 ||
      new Set(plugin.files).size !== plugin.files.length) {
    throw new Error("Invalid release plugin definition.");
  }
  validateRelativePath(plugin.source);
  validateRelativePath(plugin.productManifest);
  for (const file of plugin.files) {
    validateRelativePath(file);
  }
  if (!plugin.files.includes(plugin.productManifest)) {
    throw new Error("Release plugin allowlist must include its product manifest.");
  }
}

function validateRelativePath(relative) {
  if (typeof relative !== "string" ||
      relative === "" ||
      relative.includes("\\") ||
      path.posix.isAbsolute(relative) ||
      path.posix.normalize(relative) !== relative ||
      relative.split("/").some((part) => part === "" || part === "." || part === "..")) {
    throw new Error(`Unsafe plugin path: ${relative}`);
  }
}

function validSemanticVersion(value) {
  return typeof value === "string" &&
    /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.test(value);
}

function argument(name, fallback) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : fallback;
}

const modulePath = fileURLToPath(import.meta.url);
if (process.argv[1] && path.resolve(process.argv[1]) === modulePath) {
  const repositoryRoot = path.resolve(path.dirname(modulePath), "..");
  const version = argument("--version", "");
  const output = path.resolve(
    argument("--output", path.join(repositoryRoot, "artifacts", "plugins"))
  );
  const results = await buildPluginBundles({
    rootDirectory: repositoryRoot,
    outputDirectory: output,
    version,
    minCoreVersion: argument("--min-core-version", version),
    maxCoreVersion: argument("--max-core-version", version),
    signatureStatus: argument("--signature-status", signedStatus)
  });
  for (const result of results) {
    console.log(
      `Staged ${result.pluginId} (${result.files} files) at ${result.bundleRoot}`
    );
  }
}

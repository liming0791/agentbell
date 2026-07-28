import { createHash } from "node:crypto";
import { readdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

function argument(name, fallback) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : fallback;
}

const directory = path.resolve(argument("--directory", "artifacts/core"));
const includeDirectoryValue = argument("--include-directory");
const includeDirectories = includeDirectoryValue
  ? [path.resolve(includeDirectoryValue)]
  : [];
const version = argument("--version", "dev");
const commit = argument("--commit", "none");
const signatureStatus = argument("--signature-status", "technical-preview");
if (signatureStatus !== "technical-preview") {
  throw new Error(
    `Unsupported signature status ${signatureStatus}; signed release jobs are not implemented.`
  );
}
const fileLocations = new Map();
for (const sourceDirectory of [directory, ...includeDirectories]) {
  for (const fileName of await readdir(sourceDirectory)) {
    if (!fileName.startsWith("agentbell-")) {
      continue;
    }
    if (fileLocations.has(fileName)) {
      throw new Error(`Duplicate release artifact name ${fileName}.`);
    }
    fileLocations.set(fileName, path.join(sourceDirectory, fileName));
  }
}
const files = [...fileLocations.keys()].sort();
const artifacts = [];
const checksumLines = [];

for (const fileName of files) {
  const value = await readFile(fileLocations.get(fileName));
  const sha256 = createHash("sha256").update(value).digest("hex");
  checksumLines.push(`${sha256}  ${fileName}`);
  artifacts.push({
    fileName,
    sha256,
    size: value.length
  });
}

await writeFile(
  path.join(directory, "checksums.txt"),
  `${checksumLines.join("\n")}\n`,
  "utf8"
);
await writeFile(
  path.join(directory, "release-manifest.json"),
  `${JSON.stringify({
    schemaVersion: 1,
    version,
    commit,
    signatureStatus,
    artifacts
  }, null, 2)}\n`,
  "utf8"
);

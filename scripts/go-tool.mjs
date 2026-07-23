import { access } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const portableVersion = "go1.26.5";

async function exists(filePath) {
  try {
    await access(filePath);
    return true;
  } catch {
    return false;
  }
}

export async function findGo() {
  if (process.env.AGENTBELL_GO) {
    return path.resolve(process.env.AGENTBELL_GO);
  }
  const portable = path.join(
    root,
    ".tools",
    portableVersion,
    "go",
    "bin",
    process.platform === "win32" ? "go.exe" : "go"
  );
  if (await exists(portable)) {
    return portable;
  }
  return process.platform === "win32" ? "go.exe" : "go";
}

export function executableNextToGo(goExecutable, name) {
  if (!path.isAbsolute(goExecutable)) {
    return process.platform === "win32" ? `${name}.exe` : name;
  }
  return path.join(
    path.dirname(goExecutable),
    process.platform === "win32" ? `${name}.exe` : name
  );
}

export { root };

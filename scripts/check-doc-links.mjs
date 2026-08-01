import { access, readdir, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const markdownRoots = ["README.md", "CLAUDE.md", "TODO.md", "docs"];

async function collectMarkdownFiles(entry) {
  const absolute = path.join(root, entry);
  const entries = await readdir(absolute, { withFileTypes: true }).catch(
    () => null
  );
  if (entries === null) {
    return entry.endsWith(".md") ? [absolute] : [];
  }

  const nested = await Promise.all(
    entries.map((item) =>
      collectMarkdownFiles(path.join(entry, item.name))
    )
  );
  return nested.flat();
}

function localLinkTarget(rawTarget) {
  const target = rawTarget.startsWith("<") && rawTarget.endsWith(">")
    ? rawTarget.slice(1, -1)
    : rawTarget;
  if (
    target === "" ||
    target.startsWith("#") ||
    target.startsWith("//") ||
    /^[a-z][a-z0-9+.-]*:/i.test(target)
  ) {
    return null;
  }
  return decodeURIComponent(target.split("#", 1)[0].split("?", 1)[0]);
}

export async function findBrokenMarkdownLinks(files) {
  const broken = [];
  for (const file of files) {
    const source = await readFile(file, "utf8");
    const links = source.matchAll(/\[[^\]]*]\(([^)\s]+)(?:\s+"[^"]*")?\)/g);
    for (const match of links) {
      const target = localLinkTarget(match[1]);
      if (target === null) {
        continue;
      }
      const absoluteTarget = path.resolve(path.dirname(file), target);
      try {
        await access(absoluteTarget);
      } catch {
        const line = source.slice(0, match.index).split("\n").length;
        broken.push(
          `${path.relative(root, file)}:${line}: missing ${match[1]}`
        );
      }
    }
  }
  return broken;
}

const files = (
  await Promise.all(markdownRoots.map((entry) => collectMarkdownFiles(entry)))
).flat();
const broken = await findBrokenMarkdownLinks(files);

if (broken.length > 0) {
  throw new Error(`Broken local documentation links:\n${broken.join("\n")}`);
}

console.log(`Checked ${files.length} Markdown files for broken local links.`);

import { gzipSync } from "node:zlib";
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { extname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const distDir = fileURLToPath(new URL("../../internal/web/dist/", import.meta.url));
const compressibleExtensions = new Set([".css", ".html", ".js", ".json", ".svg"]);

function walk(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      return entry.name === ".vite" ? [] : walk(path);
    }
    return [path];
  });
}

let compressedFiles = 0;
let compressedBytes = 0;

for (const path of walk(distDir)) {
  if (!compressibleExtensions.has(extname(path)) || path.endsWith(".gz")) {
    continue;
  }

  const source = readFileSync(path);
  const compressed = gzipSync(source, { level: 9 });
  if (compressed.length >= source.length) {
    continue;
  }

  writeFileSync(`${path}.gz`, compressed);
  compressedFiles += 1;
  compressedBytes += compressed.length;
}

console.log(
  `Precompressed ${compressedFiles} files (${(compressedBytes / 1024).toFixed(2)} KiB gzip) in ${relative(process.cwd(), distDir)}`,
);

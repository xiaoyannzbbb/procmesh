import { readFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

const defaultDistDir = fileURLToPath(new URL("../../internal/web/dist/", import.meta.url));
const distDir = process.argv[2] ? resolve(process.cwd(), process.argv[2]) : defaultDistDir;
const manifest = JSON.parse(readFileSync(join(distDir, ".vite/manifest.json"), "utf8"));
const kib = 1024;

function collectManifestFiles(entryKeys) {
  const visitedEntries = new Set();
  const files = new Set();

  function visit(key) {
    if (visitedEntries.has(key)) return;
    visitedEntries.add(key);
    const entry = manifest[key];
    if (!entry) throw new Error(`Missing Vite manifest entry: ${key}`);
    files.add(entry.file);
    for (const css of entry.css ?? []) files.add(css);
    for (const dependency of entry.imports ?? []) visit(dependency);
  }

  for (const key of entryKeys) visit(key);
  return files;
}

function compressedSize(files) {
  return [...files].reduce(
    (total, file) => total + gzipSync(readFileSync(join(distDir, file)), { level: 9 }).length,
    0,
  );
}

function formatKiB(bytes) {
  return `${(bytes / 1024).toFixed(2)} KiB`;
}

const coreFiles = collectManifestFiles(["index.html"]);
coreFiles.add("index.html");

const localeFiles = {
  en: ["locales/en/common.json", "locales/en/errors.json"],
  zh: [
    "locales/en/common.json",
    "locales/en/errors.json",
    "locales/zh/common.json",
    "locales/zh/errors.json",
  ],
};

const profiles = [
  { name: "login/en", entries: ["src/pages/LoginPage.vue"], locale: "en", budget: 128 * kib },
  { name: "login/zh", entries: ["src/pages/LoginPage.vue"], locale: "zh", budget: 128 * kib },
  {
    name: "overview/en",
    entries: ["src/components/AppShell.vue", "src/pages/OverviewPage.vue"],
    locale: "en",
    budget: 155 * kib,
  },
  {
    name: "overview/zh",
    entries: ["src/components/AppShell.vue", "src/pages/OverviewPage.vue"],
    locale: "zh",
    budget: 155 * kib,
  },
];

console.log("Bundle budgets (gzip):");
let failed = false;
for (const profile of profiles) {
  const files = new Set(coreFiles);
  for (const file of collectManifestFiles(profile.entries)) files.add(file);
  for (const file of localeFiles[profile.locale]) files.add(file);
  const size = compressedSize(files);
  const status = size <= profile.budget ? "PASS" : "FAIL";
  console.log(
    `  ${status} ${profile.name.padEnd(12)} ${formatKiB(size)} / ${formatKiB(profile.budget)}`,
  );
  failed ||= size > profile.budget;

  if (profile.name.startsWith("login/")) {
    const serviceNames = new Set();
    for (const file of files) {
      if (!file.endsWith(".js")) continue;
      const source = readFileSync(join(distDir, file), "utf8");
      for (const match of source.matchAll(/procmesh\.v1\.([A-Za-z]+Service)/g)) {
        serviceNames.add(match[1]);
      }
    }
    if (serviceNames.size !== 1 || !serviceNames.has("AuthService")) {
      console.error(
        `  FAIL ${profile.name} includes unexpected RPC services: ${[...serviceNames].sort().join(", ") || "none"}`,
      );
      failed = true;
    }
  }
}

const baseline = collectManifestFiles(["index.html"]);
const asyncEntries = Object.entries(manifest)
  .filter(([, entry]) => entry.isDynamicEntry)
  .map(([key]) => {
    const files = collectManifestFiles([key]);
    for (const file of baseline) files.delete(file);
    return { key, size: compressedSize(files) };
  })
  .sort((a, b) => b.size - a.size);

if (asyncEntries[0]) {
  console.log(
    `  INFO largest async route ${asyncEntries[0].key}: ${formatKiB(asyncEntries[0].size)}`,
  );
}

if (failed) {
  console.error("Initial payload or RPC service isolation check failed.");
  process.exitCode = 1;
} else {
  console.log(`Bundle manifest: ${relative(process.cwd(), join(distDir, ".vite/manifest.json"))}`);
}

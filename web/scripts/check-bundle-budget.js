import { readFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

const defaultDistDir = fileURLToPath(new URL("../../internal/web/dist/", import.meta.url));
const distDir = process.argv[2] ? resolve(process.cwd(), process.argv[2]) : defaultDistDir;
const manifest = JSON.parse(readFileSync(join(distDir, ".vite/manifest.json"), "utf8"));
const budgetBytes = 170 * 1024;

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
  { name: "login/en", entries: ["src/pages/LoginPage.vue"], locale: "en" },
  { name: "login/zh", entries: ["src/pages/LoginPage.vue"], locale: "zh" },
  {
    name: "overview/en",
    entries: ["src/components/AppShell.vue", "src/pages/OverviewPage.vue"],
    locale: "en",
  },
  {
    name: "overview/zh",
    entries: ["src/components/AppShell.vue", "src/pages/OverviewPage.vue"],
    locale: "zh",
  },
];

console.log(`Bundle budget (${formatKiB(budgetBytes)} gzip):`);
let failed = false;
for (const profile of profiles) {
  const files = new Set(coreFiles);
  for (const file of collectManifestFiles(profile.entries)) files.add(file);
  for (const file of localeFiles[profile.locale]) files.add(file);
  const size = compressedSize(files);
  const status = size <= budgetBytes ? "PASS" : "FAIL";
  console.log(`  ${status} ${profile.name.padEnd(12)} ${formatKiB(size)}`);
  failed ||= size > budgetBytes;
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
  console.error(`Initial payload exceeds the ${formatKiB(budgetBytes)} gzip budget.`);
  process.exitCode = 1;
} else {
  console.log(`Bundle manifest: ${relative(process.cwd(), join(distDir, ".vite/manifest.json"))}`);
}

import fs from "fs";
import path from "path";
import { gzip } from "zlib";
import { promisify } from "util";
import { fileURLToPath } from "url";
import { dirname } from "path";

const gzipAsync = promisify(gzip);
const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

async function analyzeBundle() {
  const distDir = path.join(__dirname, "../../internal/web/dist/assets");

  if (!fs.existsSync(distDir)) {
    console.error(
      "❌ internal/web/dist/assets directory not found. Run `npm run build` first."
    );
    process.exit(1);
  }

  const files = fs
    .readdirSync(distDir)
    .filter((file) => file.endsWith(".js"))
    .map((file) => {
      const filePath = path.join(distDir, file);
      const content = fs.readFileSync(filePath);
      return { file, size: content.length, content };
    });

  console.log("📦 Bundle Analysis\n");
  console.log("JavaScript Chunks:");
  console.log("─".repeat(80));

  let totalSize = 0;
  let totalGzipped = 0;

  for (const { file, size, content } of files.sort((a, b) => b.size - a.size)) {
    const gzipped = await gzipAsync(content);
    const gzippedSize = gzipped.length;

    totalSize += size;
    totalGzipped += gzippedSize;

    const sizeKB = (size / 1024).toFixed(2);
    const gzipKB = (gzippedSize / 1024).toFixed(2);

    let label = "";
    if (file.includes("i18n")) label = "[i18n]";
    else if (file.includes("vue")) label = "[vue]";
    else if (file.includes("connect")) label = "[api]";
    else if (file.includes("index")) label = "[main]";

    console.log(`${label.padEnd(10)} ${file}`);
    console.log(`           ${sizeKB} KB (${gzipKB} KB gzipped)`);
  }

  console.log("─".repeat(80));
  console.log(
    `Total: ${(totalSize / 1024).toFixed(2)} KB (${(totalGzipped / 1024).toFixed(2)} KB gzipped)`
  );
  console.log("");

  // Check i18n bundle size
  const i18nFiles = files.filter((f) => f.file.includes("i18n"));
  const i18nTotal = i18nFiles.reduce((sum, f) => sum + f.size, 0);
  const i18nGzipped = await Promise.all(
    i18nFiles.map((f) => gzipAsync(f.content))
  ).then((results) => results.reduce((sum, buf) => sum + buf.length, 0));

  console.log("🌐 i18n Bundle Impact:");
  console.log(
    `   ${(i18nTotal / 1024).toFixed(2)} KB (${(i18nGzipped / 1024).toFixed(2)} KB gzipped)`
  );

  if (i18nGzipped > 20 * 1024) {
    console.log("   ⚠️  Warning: i18n bundle exceeds 20KB target");
  } else {
    console.log("   ✓ Within 20KB target");
  }
}

analyzeBundle().catch(console.error);


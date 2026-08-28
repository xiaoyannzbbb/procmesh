import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const webRoot = join(dirname(fileURLToPath(import.meta.url)), "..");

describe("favicon", () => {
  it("index.html links the ProcMesh icon assets", () => {
    const html = readFileSync(join(webRoot, "index.html"), "utf8");
    expect(html).toContain('href="/favicon.svg"');
    expect(html).toContain('href="/favicon.ico"');
    expect(html).toContain('href="/apple-touch-icon.png"');
    expect(html).toContain('rel="apple-touch-icon"');
  });

  it("favicon.svg is a square ProcMesh mark using the accent color", () => {
    const svg = readFileSync(join(webRoot, "public/favicon.svg"), "utf8");
    expect(svg).toContain('viewBox="0 0 32 32"');
    expect(svg).toContain("#10a37f");
    expect(svg).toContain("<circle");
  });
});

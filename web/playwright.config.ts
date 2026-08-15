import { defineConfig, devices } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.dirname(fileURLToPath(import.meta.url));
const storageState = path.join(root, "playwright/.auth/user.json");

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  retries: 0,
  workers: 1,
  use: {
    ...devices["Desktop Chrome"],
    browserName: "chromium",
    baseURL: process.env.PROCMESH_E2E_URL,
  },
  projects: [
    { name: "setup", testMatch: /auth\.setup\.ts/ },
    { name: "login", testMatch: /login\.spec\.ts/ },
    {
      name: "chromium",
      testIgnore: /login\.spec\.ts|auth\.setup\.ts/,
      use: { storageState },
      dependencies: ["setup"],
    },
  ],
});


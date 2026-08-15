import { test as setup } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { loginAdmin } from "./helpers";

const storageState = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "playwright/.auth/user.json");

setup("authenticate", async ({ page }) => {
  fs.mkdirSync(path.dirname(storageState), { recursive: true });
  await loginAdmin(page);
  await page.context().storageState({ path: storageState });
});

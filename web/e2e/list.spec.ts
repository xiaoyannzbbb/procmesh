import { expect, test } from "@playwright/test";

test("Nodes or Processes shows applied sleep process", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Workload" })).toBeVisible();
  await page.getByRole("link", { name: "Processes" }).click();
  await expect(page.getByRole("heading", { name: "Processes" })).toBeVisible();
  await expect(page.getByRole("link", { name: "sleep" })).toBeVisible();
});

import { expect, test } from "@playwright/test";

test("node detail shows 24h history range button", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Workload" })).toBeVisible();
  await page.getByRole("link", { name: "Nodes" }).click();
  await expect(page.getByRole("heading", { name: "Nodes" })).toBeVisible();
  const link = page.locator("table tbody tr td a").first();
  await expect(link).toBeVisible();
  await link.click();
  await expect(page.getByRole("button", { name: "24h" })).toBeVisible();
});

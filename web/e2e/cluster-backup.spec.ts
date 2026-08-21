import { expect, test } from "@playwright/test";

test("Cluster backup workflow is reachable after login", async ({ page }) => {
  await page.goto("/backup");
  await expect(page).not.toHaveURL(/\/login|404/);
  await expect(page.getByRole("heading", { name: /Backup|备份/ }).first()).toBeVisible();
  await expect(page.locator("body")).not.toContainText("404");

  const nav = page.locator(".nav-link", { hasText: /Backup|备份/ });
  if (await nav.count()) {
    await expect(nav.first()).toBeVisible();
  }

  await expect(page.locator('[data-section="policies"]')).toBeVisible();
  const createPolicy = page.locator('[data-action="create-policy"]');
  if (await createPolicy.count()) {
    await expect(createPolicy.first()).toBeVisible();
  }
});

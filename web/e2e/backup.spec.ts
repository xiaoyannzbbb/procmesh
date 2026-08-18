import { expect, test } from "@playwright/test";

test("Backup page is reachable after login and nav is visible with permission", async ({ page }) => {
  await page.goto("/backup");
  await expect(page).not.toHaveURL(/\/login|404/);
  await expect(page.getByRole("heading", { name: /Backup|备份/ }).first()).toBeVisible();
  await expect(page.locator("body")).not.toContainText("404");

  const nav = page.locator(".nav-link", { hasText: /Backup|备份/ });
  if (await nav.count()) {
    await expect(nav.first()).toBeVisible();
  }
});

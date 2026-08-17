import { expect, test } from "@playwright/test";

test("Alerts page is reachable after login and banner/empty state are exclusive", async ({ page }) => {
  await page.goto("/alerts");
  await expect(page).not.toHaveURL(/\/login|404/);
  await expect(page.getByRole("heading", { name: /Alerts|告警/ }).first()).toBeVisible();
  await expect(page.locator("body")).not.toContainText("404");

  const banner = page.locator(".alert-stale-banner");
  const empty = page.locator(".empty-inbox");
  const bannerVisible = await banner.isVisible().catch(() => false);
  const emptyVisible = await empty.isVisible().catch(() => false);
  expect(bannerVisible && emptyVisible).toBe(false);
});

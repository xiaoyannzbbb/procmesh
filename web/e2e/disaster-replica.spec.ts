import { expect, test } from "@playwright/test";

test("Disaster replica workflow is reachable after login", async ({ page }) => {
  await page.goto("/disaster-replica");
  await expect(page).not.toHaveURL(/\/login|404/);
  await expect(page.getByRole("heading", { name: /Disaster replica|灾备副本/ }).first()).toBeVisible();
  await expect(page.locator("body")).not.toContainText("404");

  const nav = page.locator(".nav-link", { hasText: /Disaster replica|灾备副本/ });
  if (await nav.count()) {
    await expect(nav.first()).toBeVisible();
  }

  const granted = page.locator('[data-permission="granted"]');
  if (await granted.count()) {
    await expect(page.locator('[data-section="overview"]')).toBeVisible();
    await expect(page.locator('[data-section="config"]')).toBeVisible();
    const generate = page.locator('[data-action="generate"]');
    if (await generate.count()) {
      await expect(generate.first()).toBeVisible();
    }
  }
});

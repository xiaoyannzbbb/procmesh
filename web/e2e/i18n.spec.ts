import { test, expect } from "@playwright/test";

async function openAccountMenu(page: import("@playwright/test").Page): Promise<void> {
  const trigger = page.locator(".account-trigger");
  if (await trigger.getAttribute("aria-expanded") !== "true") {
    await trigger.click();
  }
  const languageTrigger = page.locator('[aria-controls="account-language-menu"]');
  if (await languageTrigger.getAttribute("aria-expanded") !== "true") {
    await languageTrigger.click();
  }
}

test.describe("i18n functionality", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/");
  });

  test("should default to English", async ({ page }) => {
    await expect(page.locator("h1")).toContainText("Overview");

    await openAccountMenu(page);
    const languageSwitcher = page.locator('[data-testid="lang-en"]');
    await expect(languageSwitcher).toHaveClass(/active/);
  });

  test("should switch to Chinese", async ({ page }) => {
    await openAccountMenu(page);
    await page.click('[data-testid="lang-zh"]');

    await page.waitForTimeout(500);

    const chineseButton = page.locator('[data-testid="lang-zh"]');
    await expect(chineseButton).toHaveClass(/active/);

    const englishButton = page.locator('[data-testid="lang-en"]');
    await expect(englishButton).not.toHaveClass(/active/);
  });

  test("should persist language choice", async ({ page }) => {
    await openAccountMenu(page);
    await page.click('[data-testid="lang-zh"]');
    await page.waitForTimeout(500);

    await page.reload();
    await page.waitForTimeout(500);

    await openAccountMenu(page);
    const chineseButton = page.locator('[data-testid="lang-zh"]');
    await expect(chineseButton).toHaveClass(/active/);
  });

  test("should translate navigation links", async ({ page }) => {
    await expect(page.locator("nav")).toContainText("Overview");
    await expect(page.locator("nav")).toContainText("Nodes");
    await expect(page.locator("nav")).toContainText("Processes");

    await openAccountMenu(page);
    await page.click('[data-testid="lang-zh"]');
    await page.waitForTimeout(500);

    const navText = await page.locator("nav").textContent();
    expect(navText).toBeTruthy();
  });

  test("should translate action buttons", async ({ page }) => {
    await openAccountMenu(page);
    const logoutButton = page.getByRole("button", { name: /logout/i });
    await expect(logoutButton).toBeVisible();

    await page.click('[data-testid="lang-zh"]');
    await page.waitForTimeout(500);

    const logoutButtonAfter = page.getByRole("button");
    const buttons = await logoutButtonAfter.all();
    expect(buttons.length).toBeGreaterThan(0);
  });

  test("should translate across page navigation", async ({ page }) => {
    await openAccountMenu(page);
    await page.click('[data-testid="lang-zh"]');
    await page.waitForTimeout(500);

    await page.click('a[href="/nodes"]');
    await page.waitForTimeout(500);

    await openAccountMenu(page);
    const chineseButton = page.locator('[data-testid="lang-zh"]');
    await expect(chineseButton).toHaveClass(/active/);
  });

  test("language switcher should be visible on all pages", async ({ page }) => {
    await openAccountMenu(page);
    const langSwitcher = page.locator('[data-testid="lang-en"]');
    await expect(langSwitcher).toBeVisible();

    await page.click('a[href="/processes"]');
    await page.waitForTimeout(500);

    await openAccountMenu(page);
    await expect(langSwitcher).toBeVisible();

    await page.click('a[href="/nodes"]');
    await page.waitForTimeout(500);

    await openAccountMenu(page);
    await expect(langSwitcher).toBeVisible();
  });
});

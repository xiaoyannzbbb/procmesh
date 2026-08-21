import { expect, test } from "@playwright/test";

test("Batches page shows local-only banner after login", async ({ page }) => {
  await page.goto("/batches");
  await expect(page).not.toHaveURL(/\/login|404/);
  await expect(page.getByRole("heading", { name: /Batches|批次/ })).toBeVisible();
  await expect(page.getByRole("status").first()).toContainText(
    /entry agent|this entry|只显示本入口创建的任务/,
  );
  await expect(page.locator("body")).not.toContainText("404");
});

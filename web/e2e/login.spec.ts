import { expect, test } from "@playwright/test";
import { e2eUser, loginAs, loginAdmin } from "./helpers";

test.describe.configure({ mode: "serial" });

test("wrong password shows localized invalid credentials", async ({ page }) => {
  await loginAs(page, e2eUser(), "wrong-password");
  await expect(page.getByRole("alert")).toHaveText("Invalid username or password");
  await expect(page).toHaveURL(/\/login/);
});

test("open / redirects to login then Overview Workload", async ({ page }) => {
  await loginAdmin(page);
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Workload" })).toBeVisible();
});

import { expect, type Page } from "@playwright/test";

export function e2eUser(): string {
  return process.env.PROCMESH_E2E_USER || "admin";
}

export function e2ePassword(): string {
  const pw = process.env.PROCMESH_E2E_PASSWORD;
  if (!pw) {
    throw new Error("PROCMESH_E2E_PASSWORD is required");
  }
  return pw;
}

export async function loginAs(
  page: Page,
  username: string,
  password: string,
): Promise<void> {
  await page.goto("/");
  await expect(page).toHaveURL(/\/login/);
  await page.locator('input[name="username"]').fill(username);
  await page.locator('input[name="password"]').fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
}

export async function loginAdmin(page: Page): Promise<void> {
  await loginAs(page, e2eUser(), e2ePassword());
  await expect(page).not.toHaveURL(/\/login/);
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
}

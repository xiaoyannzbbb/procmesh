import { expect, test } from "@playwright/test";

function conflictValue(): string {
  const payload = new Uint8Array(2 + "CONFLICT".length);
  payload[0] = 0x0a;
  payload[1] = 0x08;
  for (let i = 0; i < 8; i++) {
    payload[2 + i] = "CONFLICT".charCodeAt(i);
  }
  return Buffer.from(payload).toString("base64");
}

test("UpdateConfig 409 shows conflict banner", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Workload" })).toBeVisible();
  await page.getByRole("link", { name: "Processes" }).click();
  await expect(page.getByRole("heading", { name: "Processes" })).toBeVisible();
  await expect(page.getByRole("link", { name: "sleep" })).toBeVisible();
  await page.getByRole("link", { name: "sleep" }).click();
  await page.getByRole("tab", { name: "Config" }).click();
  await page.getByRole("button", { name: "Edit config" }).click();
  await expect(page.getByRole("group", { name: "Editor mode" })).toBeVisible();
  await expect(page.locator('[data-editor-mode="form"]')).toHaveAttribute("aria-pressed", "true");

  await page.route("**/procmesh.v1.ConfigService/UpdateConfig", async (route) => {
    await route.fulfill({
      status: 409,
      contentType: "application/json",
      body: JSON.stringify({
        code: "failed_precondition",
        message: "CONFLICT: revision mismatch",
        details: [
          {
            type: "procmesh.v1.ErrorInfo",
            value: conflictValue(),
            debug: { code: "CONFLICT", message: "CONFLICT: revision mismatch" },
          },
        ],
      }),
    });
  });

  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByRole("dialog", { name: "Edit process config" }).getByRole("alert"))
    .toContainText("409 Conflict");
});

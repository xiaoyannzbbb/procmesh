import { expect, test } from "@playwright/test";

test("admin creates a one-time join command from the nodes drawer", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.goto("/nodes");
  await expect(page.getByRole("heading", { name: "Nodes" })).toBeVisible();

  const addNode = page.getByRole("button", { name: "Add node" });
  await addNode.click();
  const drawer = page.getByRole("dialog", { name: "Add node" });
  await expect(drawer).toBeVisible();
  await expect(drawer.evaluate((element) => element.contains(document.activeElement))).resolves.toBe(true);

  await page.getByLabel("Seed node").selectOption({ index: 1 });
  await page.getByRole("button", { name: "Generate join command" }).click();
  await expect(page.getByText("Run this command on the new node", { exact: true })).toBeVisible();

  const command = page.locator(".command-block code");
  await expect(command).toContainText("procmesh agent join --seed");
  await expect(command).toContainText("--token '");
  await expect(command).not.toContainText("--server");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

  await page.keyboard.press("Escape");
  await expect(page.getByRole("heading", { name: "Close and lose the plaintext token?" })).toBeVisible();
  await page.getByRole("button", { name: "Keep open" }).click();
  await expect(command).toBeVisible();

  await page.getByRole("button", { name: "Close", exact: true }).click();
  await page.getByRole("button", { name: "Close drawer" }).click();
  await expect(drawer).toBeHidden();
  await expect(addNode).toBeFocused();

  await addNode.click();
  await expect(page.locator(".result")).toHaveCount(0);
  await expect(page.getByLabel("Valid for")).toHaveValue("1");
  await expect(page.getByLabel("Maximum uses")).toHaveValue("1");
});

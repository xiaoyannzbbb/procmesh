import { expect, test } from "@playwright/test";

test("FAILED node with old last_updated shows STALE not green", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Workload" })).toBeVisible();

  const staleMs = Date.now() - 60_000;
  await page.route("**/procmesh.v1.NodeService/ListNodes", async (route) => {
    const res = await route.fetch();
    const body = (await res.json()) as {
      nodes?: Array<{
        state?: string;
        lastUpdatedUnixMs?: string | number;
        processes?: Array<{
          name?: string;
          observed?: string;
          freshnessUnixMs?: string | number;
        }>;
      }>;
    };
    // Ensure we have at least one node with processes
    if (!body.nodes || body.nodes.length === 0) {
      body.nodes = [
        {
          state: "FAILED",
          lastUpdatedUnixMs: String(staleMs),
          processes: [
            {
              name: "sleep",
              observed: "RUNNING",
              freshnessUnixMs: String(staleMs),
            },
          ],
        },
      ];
    } else {
      for (const node of body.nodes) {
        node.state = "FAILED";
        node.lastUpdatedUnixMs = String(staleMs);
        if (!node.processes?.length) {
          node.processes = [
            {
              name: "sleep",
              observed: "RUNNING",
              freshnessUnixMs: String(staleMs),
            },
          ];
        } else {
          for (const proc of node.processes) {
            proc.observed = "RUNNING";
            proc.freshnessUnixMs = String(staleMs);
          }
        }
      }
    }
    await route.fulfill({ response: res, json: body });
  });

  await page.goto("/processes");
  const badge = page.locator(".freshness-stale").first();
  await expect(badge).toBeVisible();
  await expect(page.locator(".freshness-live")).toHaveCount(0);

  const styles = await badge.evaluate((el) => {
    const cs = getComputedStyle(el);
    return { color: cs.color, background: cs.backgroundColor };
  });
  expect(styles.color).not.toMatch(/rgb\(\s*6,\s*95,\s*70\s*\)/);
  expect(styles.background).not.toMatch(/rgb\(\s*209,\s*250,\s*229\s*\)/);
  expect(styles.color.toLowerCase()).not.toContain("green");
  expect(styles.background.toLowerCase()).not.toContain("green");
});

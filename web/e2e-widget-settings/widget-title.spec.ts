import { test, expect } from "@playwright/test";
import { mkdirSync } from "node:fs";

// §5.6: in view mode a widget shows the instance title saved in its settings, not the type name; a widget without a
// title keeps the type name. 1440 and 393 (PRD §8). Screenshots go to e2e-widget-settings/shots/.

const SHOTS = "e2e-widget-settings/shots";
mkdirSync(SHOTS, { recursive: true });

const TITLED: [string, string][] = [
  ["mentions", "QA96 custom mentions"],
  ["my-tasks", "QA96 my queue"],
  ["event-feed", "QA96 feed"],
  ["cost-kpis", "QA96 spend"],
  ["recent-comments", "QA96 comments"],
];

for (const [width, height] of [[1440, 900], [393, 852]] as const) {
  test(`${width}x${height}: saved titles replace the type names, the untitled Mentions keeps its name`, async ({ page }) => {
    await page.setViewportSize({ width, height });
    await page.goto("/e2e-widget-settings/widget-title-harness.html");
    for (const [type, title] of TITLED) {
      const heading = page.locator(`[data-cell="${type}"] h3`).first();
      await expect(heading).toHaveText(title);
      await expect(heading).toBeVisible();
    }
    await expect(page.locator(`[data-cell="mentions"] h3`).first()).not.toContainText("Mentions");
    await expect(page.locator(`[data-cell="mentions-default"] h3`).first()).toHaveText("Mentions");
    const doc = await page.evaluate(() => ({ client: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
    expect(doc.scroll, JSON.stringify(doc)).toBeLessThanOrEqual(doc.client);
    await page.screenshot({ path: `${SHOTS}/widget-title-${width}x${height}.png`, fullPage: true });
  });
}

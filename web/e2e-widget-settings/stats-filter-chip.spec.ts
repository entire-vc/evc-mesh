import { test, expect } from "@playwright/test";
import { mkdirSync } from "node:fs";

// at 393 an active filter of the statistics page is whole on screen and has its ×; the page does not
// scroll sideways. The «Pool / account» chip used to sit in the sideways strip of controls at x=824 of 393, so a
// single pool's slice read as the workspace total. 1440 is the control. The real page: e2e/stats-filter-chip-mobile.

const SHOTS = "e2e-widget-settings/shots";
mkdirSync(SHOTS, { recursive: true });
const URL = "/e2e-widget-settings/stats-filter-harness.html?m=tokens&g=agent&p=7d&f.account=claude%2Fbob-team";

for (const [width, height] of [[393, 852], [1440, 900]] as const) {
  test(`${width}x${height}: the account chip is whole on the first screen, no sideways scroll`, async ({ page }) => {
    await page.setViewportSize({ width, height });
    await page.goto(URL);
    const chip = page.getByTestId("filter-chip").filter({ visible: true }).filter({ hasText: "claude/bob-team" });
    await expect(chip).toHaveCount(1);
    const box = (await chip.boundingBox())!;
    const m = await page.evaluate(() => ({ inner: window.innerWidth, scroll: document.documentElement.scrollWidth }));
    await page.screenshot({ path: `${SHOTS}/stats-filter-chip-${width}.png` });
    const at = JSON.stringify({ x: box.x, w: box.width, y: box.y, ...m });
    expect(box.x, at).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width, at).toBeLessThanOrEqual(m.inner);
    expect(box.y + box.height, at).toBeLessThanOrEqual(height);
    expect(m.scroll, at).toBe(m.inner);
    // At 393 the chip has a row of its own: the pool's name is read whole, not «claude/bob-te…» (1440 keeps 16rem).
    if (width === 393) {
      const label = await chip.locator(".truncate").evaluate((el) => ({ client: el.clientWidth, scroll: el.scrollWidth }));
      expect(label.scroll, JSON.stringify(label)).toBeLessThanOrEqual(label.client);
    }

    // The × drops the filter and the chip goes with it.
    await chip.getByRole("button", { name: /Remove filter/ }).click();
    await expect(page.getByTestId("filter-chip").filter({ visible: true })).toHaveCount(0);
  });
}

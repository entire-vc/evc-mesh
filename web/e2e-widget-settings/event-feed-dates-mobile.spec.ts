import { test, expect } from "@playwright/test";
import { mkdirSync } from "node:fs";

// PRD §8 / §11.14: no horizontal scroll at 393 px. The Events card's From/To date pair used to keep two fixed
// 144 px inputs on one row, 12 px wider than the filter panel: focusing «To date» scrolled the card sideways.
// The pair now takes the row and both inputs share it below the container breakpoint. 1440 is the control.

const SHOTS = "e2e-widget-settings/shots";
mkdirSync(SHOTS, { recursive: true });

for (const [width, height] of [[393, 852], [1440, 900]] as const) {
  test(`${width}x${height}: date pair stays inside the filter panel, the card does not scroll sideways`, async ({ page }) => {
    await page.setViewportSize({ width, height });
    await page.goto("/e2e-widget-settings/event-feed-harness.html");
    const card = page.getByTestId("event-feed-card");
    await expect(card).toBeVisible();
    await page.getByRole("button", { name: /^Filters/ }).click();

    const to = page.getByLabel("To date");
    const from = page.getByLabel("From date");
    await expect(to).toBeVisible();
    await to.focus();
    await page.keyboard.press("Tab"); // move through the field like a keyboard user; any scroll-into-view happens here
    await to.focus();

    const m = await card.evaluate((el) => ({ client: el.clientWidth, scroll: el.scrollWidth, left: el.scrollLeft }));
    expect(m.scroll, JSON.stringify(m)).toBeLessThanOrEqual(m.client);
    expect(m.left).toBe(0);

    const panel = page.locator("[data-widget=event-feed] > div.rounded-lg.border").first();
    const pb = (await panel.boundingBox())!;
    for (const input of [from, to]) {
      const b = (await input.boundingBox())!;
      expect(b.x).toBeGreaterThanOrEqual(pb.x);
      expect(b.x + b.width).toBeLessThanOrEqual(pb.x + pb.width);
      expect(b.width).toBeGreaterThanOrEqual(100); // still a usable date field
    }
    await page.screenshot({ path: `${SHOTS}/event-feed-dates-${width}x${height}.png` });
  });
}

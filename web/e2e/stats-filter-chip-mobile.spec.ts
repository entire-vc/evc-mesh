import { test, expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { loginWithRetry, pinUiLocale } from "./auth-helper";

/**
 * E2E: on the statistics page at 393 an active filter is on screen. «Who spent it» on a limits card opens
 * `?m=tokens&g=agent&p=7d&f.account=<pool>`; the «Pool / account» chip used to sit in the sideways-scrolling strip
 * of controls (QA measured x=824 at innerWidth 393), so the first screen read a single pool's slice as the whole
 * workspace. 1440 is the control: there the chip was always in the settings panel.
 *
 * The CI account is a `member` of the sandbox, and the pool cut is the admin's (§3.1: `account[]` from a member is
 * 403). The page is the admin's page here: the usage request goes to the real server without `account[]`, and its
 * real answer comes back with «account» added to `capabilities.group_by`, which is all the page reads to keep the
 * filter. Nothing is written.
 */

const SANDBOX_WS_SLUG = "e2e-ci-sandbox";
const ACCOUNT = "claude/bob-team";
// Screenshots only on request (STATS_CHIP_SHOTS=<dir>); the assertions are the check.
const SHOTS = process.env.STATS_CHIP_SHOTS;

async function actAsAdmin(page: Page) {
  await page.route((u) => /\/api\/v1\/workspaces\/[^/]+\/analytics\/usage$/.test(u.pathname), async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const url = new URL(route.request().url());
    for (const k of [...url.searchParams.keys()]) if (k === "account" || k === "account[]") url.searchParams.delete(k);
    const res = await route.fetch({ url: url.toString() });
    if (res.status() !== 200) return route.fulfill({ response: res });
    const body = (await res.json()) as { capabilities?: { group_by?: string[] } };
    const caps = body.capabilities;
    if (caps?.group_by && !caps.group_by.includes("account")) caps.group_by = [...caps.group_by, "account"];
    return route.fulfill({ response: res, json: body });
  });
}

test.describe.serial("Statistics: an active filter stays on screen", () => {
  let page: Page;

  test.beforeAll(async ({ browser }) => {
    test.setTimeout(120_000);
    const context = await browser.newContext();
    await pinUiLocale(context, "en");
    await loginWithRetry(context.request, process.env.E2E_USER_EMAIL!, process.env.E2E_USER_PASSWORD!);
    page = await context.newPage();
    await actAsAdmin(page);
  });

  test.afterAll(async () => {
    await page?.context().close();
  });

  for (const [width, height] of [[393, 852], [1440, 900]] as const) {
    test(`${width}: the «Pool / account» chip is whole inside the viewport, the page does not scroll sideways`, async () => {
      await page.setViewportSize({ width, height });
      const usage = page.waitForResponse((r) => r.url().includes("/analytics/usage?") && r.request().method() === "GET", { timeout: 60_000 });
      await page.goto(`/w/${SANDBOX_WS_SLUG}/stats?m=tokens&g=agent&p=7d&f.account=${encodeURIComponent(ACCOUNT)}`, { waitUntil: "domcontentloaded" });
      expect((await usage).status(), "the usage read answers").toBe(200);

      const chip = page.getByTestId("filter-chip").filter({ visible: true }).filter({ hasText: ACCOUNT });
      await expect(chip, "one visible account chip").toHaveCount(1, { timeout: 30_000 });
      // The filter survived the capabilities answer (a page without the pool cut drops it from the URL).
      await expect(page).toHaveURL(/f\.account=/);
      await expect(chip.getByRole("button", { name: /Remove filter/ }), "the chip can be dropped").toBeVisible();
      await page.waitForLoadState("networkidle");

      const box = (await chip.boundingBox())!;
      const m = await page.evaluate(() => ({ inner: window.innerWidth, scroll: document.documentElement.scrollWidth }));
      if (SHOTS) {
        mkdirSync(SHOTS, { recursive: true });
        await page.screenshot({ path: `${SHOTS}/stats-filter-chip-${width}.png` });
      }
      const at = JSON.stringify({ x: box.x, width: box.width, ...m });
      expect(box.x, `${width}: chip starts on screen ${at}`).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width, `${width}: chip ends on screen ${at}`).toBeLessThanOrEqual(m.inner);
      expect(box.y + box.height, `${width}: chip is on the first screen ${at}`).toBeLessThanOrEqual(height);
      expect(m.scroll, `${width}: no sideways scroll of the page ${at}`).toBe(m.inner);
    });
  }
});

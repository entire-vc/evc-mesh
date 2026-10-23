import { test, expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
// Docs screenshots against the loopback fixture, for a base-vs-head pixel diff (board-e2e/visual-diff.py).
// Two screens: an open long page with its branch of the tree (on a phone the page owns the screen), and the
// docs root (on a phone the tree owns the screen). $DOCS_SHOTS names the output directory.
// Setting $DOCS_SHOTS selects only this spec; the default run (CI, --repeat-each=20) leaves it out.
//   DOCS_SHOTS=/tmp/base pnpm exec playwright test -c board-e2e/playwright.config.ts docs-shots   # on base
//   DOCS_SHOTS=/tmp/head pnpm exec playwright test -c board-e2e/playwright.config.ts docs-shots   # on head
//   python3 board-e2e/visual-diff.py /tmp/base /tmp/head
const DOCS_URL = "/w/board-fixture/p/contenthub/docs";
const SHOTS = process.env.DOCS_SHOTS;
const BOOT_MS = 60_000;

const settle = async (page: Page) => {
  await page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}" });
  await page.evaluate(() => document.querySelectorAll("*").forEach((el) => { if (el.scrollLeft) el.scrollLeft = 0; }));
  await page.mouse.move(0, 0);
  await page.waitForTimeout(1500);
};

for (const theme of ["light", "dark"] as const) {
  for (const width of [1440, 393]) {
    test(`docs page ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 1440 ? 900 : 852 });
      await page.addInitScript((t) => localStorage.setItem("theme", t), theme);
      await page.goto(`${DOCS_URL}/deploys`);
      await expect(page.locator("h1[title]", { hasText: "Deploys and rollbacks" })).toBeVisible({ timeout: BOOT_MS });
      await expect(page.getByText("Step 30:")).toBeVisible({ timeout: BOOT_MS });
      await settle(page);
      if (SHOTS) {
        mkdirSync(SHOTS, { recursive: true });
        await page.screenshot({ path: `${SHOTS}/docs-page-${theme}-${width}.png` });
      }
    });

    test(`docs root ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 1440 ? 900 : 852 });
      await page.addInitScript((t) => localStorage.setItem("theme", t), theme);
      await page.goto(DOCS_URL);
      // Collapsed by default: the roots are what is on screen; open the first branch for a nested row.
      await expect(page.getByRole("treeitem").first()).toBeVisible({ timeout: BOOT_MS });
      await page.getByRole("button", { name: "Expand Handbook" }).click();
      await page.getByRole("button", { name: "Expand Onboarding" }).click();
      await page.getByRole("treeitem", { name: /Day one checklist/ }).hover();
      await settle(page);
      await page.getByRole("treeitem", { name: /Day one checklist/ }).hover();
      if (SHOTS) {
        mkdirSync(SHOTS, { recursive: true });
        await page.screenshot({ path: `${SHOTS}/docs-root-${theme}-${width}.png` });
      }
    });
  }
}

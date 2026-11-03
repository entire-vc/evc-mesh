import { test, expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
// Long labels must clip inside their card / table cell, never widen it. Loopback fixture; the labels are injected on the
// way through, no data is written. $LABEL_SHOTS names the screenshot directory.
const API = "http://127.0.0.1:8019";
const SHOTS = process.env.LABEL_SHOTS;
const BOOT_MS = 60_000;
const HASH = `ops-package:${"0123456789abcdef".repeat(4)}`;
const LABELS = [HASH, "очень-длинная-пользовательская-категория-без-пробелов-для-проверки-переноса", "frontend", "ux"];

const open = async (page: Page, path: string, ready: string) => {
  await page.route(/\/projects\/[^/]+\/tasks(\?|$)/, async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    body.items = body.items.map((t: Record<string, unknown>, i: number) => ({ ...t, labels: i % 2 === 0 ? LABELS : [] }));
    await route.fulfill({ response: res, json: body });
  });
  const tasks = page.waitForResponse((r) => r.url().startsWith(API) && /\/tasks(\?|$)/.test(r.url()), { timeout: BOOT_MS });
  await page.goto(path);
  await tasks;
  await expect(page.locator(ready).first()).toBeVisible({ timeout: BOOT_MS });
  await page.waitForTimeout(800);
};

/** Every label chip sits inside its container, and the page does not scroll sideways because of it. */
const assertContained = async (page: Page, container: string) => {
  const bad = await page.evaluate(({ container, hash }) => {
    const out: string[] = [];
    for (const box of document.querySelectorAll<HTMLElement>(container)) {
      const b = box.getBoundingClientRect();
      for (const el of box.querySelectorAll<HTMLElement>("[title]")) {
        if (!el.title.includes("ops-package") && !el.title.startsWith("очень")) continue;
        const r = el.getBoundingClientRect();
        if (r.right > b.right + 0.5 || r.left < b.left - 0.5) out.push(`${el.title.slice(0, 20)} ${r.right}>${b.right}`);
        const text = el.querySelector<HTMLElement>("span") ?? el;
        if (el.title === hash && text.scrollWidth <= text.clientWidth) out.push("hash not clipped");
      }
    }
    return out;
  }, { container, hash: HASH });
  expect(bad).toEqual([]);
};

for (const vp of [{ width: 1440, height: 900 }, { width: 393, height: 852 }]) {
  test.describe(`${vp.width}`, () => {
    test.use({ viewport: vp });

    test("board card clips long labels and keeps the full text", async ({ page }) => {
      await open(page, "/w/board-fixture/p/contenthub", '[data-testid="task-card"]');
      await assertContained(page, '[data-testid="task-card"]');
      await expect(page.getByTitle(HASH).first()).toHaveText(HASH);
      if (SHOTS) { mkdirSync(SHOTS, { recursive: true }); await page.screenshot({ path: `${SHOTS}/board-${vp.width}.png` }); }
    });

    test("list row clips long labels", async ({ page }) => {
      await open(page, "/w/board-fixture/p/contenthub/list", "tr > td");
      await page.getByRole("button", { name: /Columns/ }).click();
      await page.getByText("Labels", { exact: true }).click();
      await page.keyboard.press("Escape");
      await expect(page.getByTitle(HASH).first()).toBeVisible();
      await assertContained(page, "tbody tr td");
      if (SHOTS) { mkdirSync(SHOTS, { recursive: true }); await page.screenshot({ path: `${SHOTS}/list-${vp.width}.png` }); }
    });
  });
}

import { test, expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
// Card counters «total (open)»: four cards with the combinations Alex asked about, shot at 1440 and 393.
// $BOARD_SHOTS names the output directory.
const BOARD_URL = "/w/board-fixture/p/contenthub";
const API = "http://127.0.0.1:8019";
const SHOTS = process.env.BOARD_SHOTS;
const BOOT_MS = 60_000;

const CASES = [
  { title: "Subtasks, some open", fields: { subtask_count: 14, open_subtask_count: 1, dependency_count: 0, open_dependency_count: 0 }, text: /14\s*\(1\)/ },
  { title: "Subtasks, all closed", fields: { subtask_count: 14, open_subtask_count: 0, dependency_count: 0, open_dependency_count: 0 }, text: /^14$/ },
  { title: "Subtasks and dependencies", fields: { subtask_count: 14, open_subtask_count: 1, dependency_count: 5, open_dependency_count: 1 }, text: /14\s*\(1\)\s*5\s*\(1\)/ },
  { title: "Nothing", fields: { subtask_count: 0, open_subtask_count: 0, dependency_count: 0, open_dependency_count: 0 }, text: null },
];

const open = async (page: Page) => {
  await page.route(/\/projects\/[^/]+\/tasks(\?|$)/, async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    body.items = body.items.map((t: Record<string, unknown>, i: number) =>
      i < CASES.length ? { ...t, title: CASES[i]!.title, ...CASES[i]!.fields, human_gate: false } : t,
    );
    await route.fulfill({ response: res, json: body });
  });
  const tasks = page.waitForResponse((r) => r.url().startsWith(API) && /\/tasks(\?|$)/.test(r.url()), { timeout: BOOT_MS });
  await page.goto(BOARD_URL);
  await tasks;
  await expect(page.locator('[data-testid="task-card"]').first()).toBeVisible({ timeout: BOOT_MS });
  await page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}" });
  await page.waitForTimeout(1000);
};

for (const width of [1440, 393]) {
  test(`card counters ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 1440 ? 900 : 852 });
    await open(page);
    if (SHOTS) mkdirSync(SHOTS, { recursive: true });
    for (const [i, c] of CASES.entries()) {
      const card = page.locator('[data-testid="task-card"]', { hasText: c.title }).first();
      await card.scrollIntoViewIfNeeded();
      const row = card.locator("> div").first();
      if (c.text) await expect(row).toHaveText(new RegExp(c.text.source.replace(/^\^|\$$/g, "")));
      else await expect(card.locator("svg.lucide-git-branch, svg.lucide-link-2")).toHaveCount(0);
      if (c.title === "Subtasks and dependencies") {
        await expect(card.locator("svg.lucide-link-2")).toHaveCount(1);
        const reds = await card.locator("span.text-destructive").allTextContents();
        expect(reds.map((s) => s.trim())).toEqual(["(1)", "(1)"]);
        const colour = await card.locator("span.text-destructive").first().evaluate((el) => getComputedStyle(el).color);
        const muted = await card.locator("svg.lucide-git-branch").evaluate((el) => getComputedStyle(el).color);
        expect(colour).not.toBe(muted);
      }
      if (SHOTS) await card.screenshot({ path: `${SHOTS}/card-${i + 1}-${c.title.toLowerCase().replace(/\W+/g, "-")}-${width}.png` });
    }
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/board-${width}.png` });
  });
}

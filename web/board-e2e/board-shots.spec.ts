import { test, expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
// Board screenshots against the loopback fixture, for a base-vs-head pixel diff (board-e2e/visual-diff.py).
// Card content is varied on the way through (priority, labels, due dates, gates, assignee, checkout), so the
// shots exercise the card, not just the title. $BOARD_SHOTS names the output directory.
const BOARD_URL = "/w/board-fixture/p/contenthub";
const API = "http://127.0.0.1:8019";
const SHOTS = process.env.BOARD_SHOTS;
const BOOT_MS = 60_000;
const PRIORITIES = ["urgent", "high", "medium", "low", "none"];

const open = async (page: Page, theme: "light" | "dark") => {
  await page.addInitScript((t) => localStorage.setItem("theme", t), theme);
  // Vary the cards: the fixture's are all `medium`, no labels, no dates.
  await page.route(/\/projects\/[^/]+\/tasks(\?|$)/, async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    body.items = body.items.map((t: Record<string, unknown>, i: number) => ({
      ...t,
      priority: PRIORITIES[i % 5],
      labels: i % 3 === 0 ? ["frontend", "ux"] : i % 7 === 0 ? ["bug"] : [],
      due_date: i % 4 === 0 ? "2026-09-01T00:00:00Z" : i % 9 === 0 ? "2030-01-01T00:00:00Z" : null,
      assignee_type: i % 5 === 0 ? "user" : "unassigned",
      assignee_id: i % 5 === 0 ? "fixture-user" : null,
      subtask_count: i % 6 === 0 ? 3 : 0,
      comment_count: i % 8 === 0 ? 2 : 0,
      ...(i % 11 === 0 ? { human_gate: true, human_gate_class: "hard", gate_reason: "sign-off needed" } : {}),
      ...(i % 13 === 0 ? { checked_out_by: "fixture-user" } : {}),
    }));
    await route.fulfill({ response: res, json: body });
  });
  const tasks = page.waitForResponse((r) => r.url().startsWith(API) && /\/tasks(\?|$)/.test(r.url()), { timeout: BOOT_MS });
  await page.goto(BOARD_URL);
  await tasks;
  await expect(page.locator('[data-testid="task-card"]').first()).toBeVisible({ timeout: BOOT_MS });
  await page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}" });
  // Scroll containers back to the origin: the horizontal offset after load differs run to run on a narrow screen.
  await page.evaluate(() => document.querySelectorAll("*").forEach((el) => { if (el.scrollLeft) el.scrollLeft = 0; }));
  await page.mouse.move(0, 0);
  await page.waitForTimeout(1500);
};

for (const theme of ["light", "dark"] as const) {
  for (const width of [1440, 393]) {
    test(`board ${theme} ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width === 1440 ? 900 : 852 });
      await open(page, theme);
      if (SHOTS) {
        mkdirSync(SHOTS, { recursive: true });
        await page.screenshot({ path: `${SHOTS}/board-${theme}-${width}.png` });
        await page.locator('[data-testid="board-column"]').first().screenshot({ path: `${SHOTS}/column-${theme}-${width}.png` });
      }
    });
  }
}

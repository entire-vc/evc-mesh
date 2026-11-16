import { test, expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
// The task widgets (my-tasks, triage-queue, task-list) on the dashboard at their minimum width of 3 cells, against the
// loopback fixture: every row is inside its card, nothing scrolls sideways, the links are real and Move exists for an
// owner only. The mesh-default layout still draws two columns whatever the instance size, so on a wide window the
// spec narrows its grid to four columns: 3 of 12 cells, the width a 3-cell instance gets. The widgets follow their
// container (container queries), never the window. Screenshots go to $DASHBOARD_SHOTS when set. Both fixture calls
// say `dashboards: "missing"`: the widgets live on the legacy dashboard page, which a server without the dashboards API
// draws; with the API the legacy URL moves to /d/default and no triage request is made.
const DASH_URL = "/w/board-fixture/dashboard";
const API = process.env.BOARD_FIXTURE_API ?? "http://127.0.0.1:8019";
const BOOT_MS = 60_000;
const SHOTS = process.env.DASHBOARD_SHOTS;
const WIDGETS = ["my-tasks", "triage-queue", "task-list"];

const open = async (page: Page, role = "owner") => {
  await page.request.post(`${API}/__fixture`, { data: { mode: "normal", role, triage: true, resetTheme: true, resetTasks: true, dashboards: "missing" } });
  await page.request.put(`${API}/api/v1/workspaces/pagination-workspace/theme`, { data: { theme_id: "mesh-default", settings: { widgets: WIDGETS, columns: 4 } } });
  if (role !== "owner") await page.request.post(`${API}/__fixture`, { data: { mode: "normal", role, triage: true, dashboards: "missing" } });
  const triage = page.waitForResponse((r) => r.url().startsWith(API) && r.url().includes("/triage"), { timeout: BOOT_MS });
  await page.goto(DASH_URL);
  await triage;
  await page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}" });
  await expect(page.getByText("Flaky login on Safari")).toBeVisible();
  await expect(page.getByText("Assigned task 1").first()).toBeVisible();
};
const shot = async (page: Page, name: string) => {
  if (!SHOTS) return;
  mkdirSync(SHOTS, { recursive: true });
  await page.screenshot({ path: `${SHOTS}/${name}-${page.viewportSize()!.width}.png`, fullPage: true });
};

test.afterEach(async ({ request }) => {
  await request.post(`${API}/__fixture`, { data: { mode: "normal", role: "owner", resetTheme: true } });
});

for (const width of [1440, 393]) {
  test.describe(`viewport ${width}`, () => {
    test.use({ viewport: { width, height: 900 } });

    test("three task widgets at 3 cells: nothing cut, nothing scrolls sideways", async ({ page }) => {
      test.setTimeout(BOOT_MS + 30_000);
      await open(page);
      if (width >= 768) await page.addStyleTag({ content: "main .grid.gap-4{grid-template-columns:repeat(4,minmax(0,1fr))!important}" });
      const cards = page.locator(".\\@container");
      await expect(cards).toHaveCount(3);
      for (const card of await cards.all()) {
        const box = (await card.boundingBox())!;
        // the card fits the window, and no row or row part sticks out of it
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(width + 0.5);
        const overflow = await card.evaluate((el) => {
          const out: string[] = [];
          const r = el.getBoundingClientRect();
          for (const n of el.querySelectorAll("li, li *")) {
            const b = n.getBoundingClientRect();
            if (b.width && (b.left < r.left - 0.5 || b.right > r.right + 0.5)) out.push(n.textContent?.slice(0, 30) ?? n.tagName);
          }
          for (const n of [el, ...el.querySelectorAll("*")]) if (n.scrollWidth > n.clientWidth + 1 && getComputedStyle(n).overflowX !== "hidden" && !n.classList.contains("truncate")) out.push(`scroll:${n.className}`);
          return out;
        });
        expect(overflow).toEqual([]);
        // 3 cells of the 12-column grid: a quarter of the content width (the phone has one full-width column)
        if (width >= 768) expect(box.width).toBeLessThan(width / 4);
      }
      // the long title is truncated with its full text kept, and links to the task
      const long = page.getByRole("link", { name: /Customer reports that exports/ }).first();
      await expect(long).toHaveAttribute("href", "/t/triage-1");
      // triage row: project, author, age; Move for an owner
      const row = page.locator("li[data-task-row=triage-2]");
      await expect(row).toContainText("Other project");
      await expect(row).toContainText("by Grace Hopper");
      await expect(row).toContainText("2d");
      await expect(page.getByRole("button", { name: 'Move "Flaky login on Safari"' })).toBeVisible();
      // the counts are the server totals
      await expect(page.getByTestId("widget-total")).toHaveText(["5", "3", "5"]);
      await shot(page, "dashboard-tasks");
    });

    test("viewer: same rows, no Move, no editors", async ({ page }) => {
      test.setTimeout(BOOT_MS + 30_000);
      await open(page, "viewer");
      await expect(page.getByRole("button", { name: /^Move "/ })).toHaveCount(0);
      await expect(page.locator("[data-inline-edit]")).toHaveCount(0);
    });
  });
}

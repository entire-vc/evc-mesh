import { test, expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
// Dashboard "My tasks" widget on the shared TaskLink primitive, against the
// loopback fixture. Screenshots go to $DASHBOARD_SHOTS (when set) so a base and
// a head run can be compared pixel by pixel.
const DASH_URL = "/w/board-fixture/dashboard";
const API = "http://127.0.0.1:8019";
const BOOT_MS = 60_000;
const SHOTS = process.env.DASHBOARD_SHOTS;

const open = async (page: Page, mode: "normal" | "empty" | "error") => {
  await page.request.post(`${API}/__fixture`, { data: { mode } });
  const mine = page.waitForResponse((r) => r.url().startsWith(API) && r.url().includes("/me/tasks"), { timeout: BOOT_MS });
  await page.goto(DASH_URL);
  await mine;
  await page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}" });
};
const shot = async (page: Page, name: string) => {
  if (!SHOTS) return;
  mkdirSync(SHOTS, { recursive: true });
  await page.screenshot({ path: `${SHOTS}/${name}-${page.viewportSize()!.width}.png` });
};
const widget = (page: Page) => page.locator("li").filter({ has: page.locator('a[href^="/t/"]') });

// The fixture keeps its mode between tests: leave it normal for whichever spec runs next.
test.afterEach(async ({ request }) => {
  await request.post(`${API}/__fixture`, { data: { mode: "normal" } });
});

for (const width of [1440, 393]) {
  test.describe(`viewport ${width}`, () => {
    test.use({ viewport: { width, height: 900 } });

    test("my tasks: five links to /t/<id>; click opens the task", async ({ page }) => {
      test.setTimeout(BOOT_MS + 30_000);
      await open(page, "normal");
      await expect(widget(page)).toHaveCount(5);
      await expect(page.getByText("Assigned task 1")).toBeVisible();
      const href = await page.locator('a[href^="/t/"]').first().getAttribute("href");
      expect(href).toMatch(/^\/t\/fixture-task-\d+$/);
      await shot(page, "dashboard-normal");
      await page.locator('a[href^="/t/"]').first().click();
      await expect(page).toHaveURL(new RegExp(`${href!.split("/").at(-1)}$`));
    });

    test("my tasks: empty state, no links", async ({ page }) => {
      test.setTimeout(BOOT_MS + 30_000);
      await open(page, "empty");
      await expect(widget(page)).toHaveCount(0);
      await expect(page.getByText("Assigned task 1")).toHaveCount(0);
      await shot(page, "dashboard-empty");
    });

    test("my tasks: API error falls back to the empty state", async ({ page }) => {
      test.setTimeout(BOOT_MS + 30_000);
      await open(page, "error");
      await expect(widget(page)).toHaveCount(0);
      await shot(page, "dashboard-error");
    });
  });
}

import { test, expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
// Instance administration (Mesh Doc e2e/instance-admin) against the loopback fixture: real clicks only.
// The fixture is a stand for the UI; the filters and counts on the server side are covered by the Go tests.
// Screenshots (extensions table, ?ext= form, journal filtered, themes; 1440/393) go to web/themes-shots, a CI artifact.
const API = "http://127.0.0.1:8019";
const URL = "/w/board-fixture/instance";
const BOOT_MS = 60_000;
const SHOTS = process.env.THEMES_SHOTS ?? (process.env.CI ? "themes-shots" : undefined);

const reset = (page: Page) => page.request.post(`${API}/__fixture`, { data: { mode: "normal", role: "owner", resetTheme: true } });
const shot = async (page: Page, name: string) => {
  if (!SHOTS) return;
  mkdirSync(SHOTS, { recursive: true });
  // the screen scrolls inside the app shell: let every container grow so the full-page frame holds all of it
  const hide = await page.addStyleTag({
    content: "[data-sonner-toaster]{display:none!important} html,body,#root,#root *:has([data-testid=instance-admin]),[data-testid=instance-admin]{height:auto!important;max-height:none!important;overflow:visible!important}",
  });
  await page.screenshot({ path: `${SHOTS}/instance-${name}-${page.viewportSize()!.width}.png`, fullPage: true });
  await hide.evaluate((el) => (el as Element).remove());
};
const open = async (page: Page, path = URL) => {
  await page.addInitScript(() => localStorage.setItem("theme", "light"));
  await page.goto(path);
  await expect(page.getByRole("heading", { name: "Instance administration" })).toBeVisible({ timeout: BOOT_MS });
  await page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}" });
};
const rows = (page: Page) => page.locator('[data-testid^="ext-row-"]');
const journalRows = (page: Page) => page.getByTestId("journal").getByRole("listitem");

test.afterEach(async ({ page }) => {
  await reset(page);
});

for (const width of [1440, 393] as const) {
  test.describe(`instance admin ${width}`, () => {
    test.use({ viewport: { width, height: 900 } });

    test("extensions table: filter, allow in the row, scroll to the last row", async ({ page }) => {
      test.setTimeout(BOOT_MS + 60_000);
      await reset(page);
      await open(page);
      await expect(rows(page)).toHaveCount(10);
      await expect(page.getByTestId("ext-source-keep")).toHaveText("Repository");
      await expect(page.getByTestId("ext-workspaces-github")).toHaveText("6");
      await shot(page, "extensions");

      // narrow by type: only the themes are left
      await page.getByRole("combobox", { name: "Type" }).selectOption("theme");
      await expect(rows(page)).toHaveCount(2);
      await expect(page.getByText("2 of 10")).toBeVisible();
      await page.getByRole("button", { name: "Reset filters" }).first().click();
      await page.getByRole("searchbox", { name: "Search by name" }).fill("standup");
      await expect(rows(page)).toHaveCount(1);

      // allow a community plugin from its row
      const row = page.getByTestId("ext-row-standup-bot");
      await expect(row.getByTestId("allow-state-standup-bot")).toHaveText("Not allowed");
      await row.getByRole("switch", { name: "Allow Standup bot" }).click();
      await expect(row.getByTestId("allow-state-standup-bot")).toHaveText("Available");
      await expect(row.getByText("set by an administrator")).toBeVisible();

      // the page scrolls to the last row and nothing runs off the side
      await page.getByRole("button", { name: "Reset filters" }).first().click();
      const last = page.getByTestId("ext-row-forge");
      await last.scrollIntoViewIfNeeded();
      await expect(last).toBeInViewport();
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      expect(overflow, "no horizontal scroll").toBeLessThanOrEqual(0);
    });

    test("?ext= from the wizard opens that row's instance keys", async ({ page }) => {
      test.setTimeout(BOOT_MS + 30_000);
      await reset(page);
      await open(page, `${URL}/extensions?ext=gitlab`);
      const row = page.getByTestId("ext-row-gitlab");
      await expect(row.getByTestId("instance-settings")).toBeVisible();
      await expect(row.getByLabel(/Base URL/)).toHaveValue("https://git.example.com");
      await expect(row.getByTestId("env-badge")).toHaveText("Moved from environment variables 03.10");
      await expect(row).toBeInViewport();
      expect(new globalThis.URL(page.url()).pathname).toBe(URL);
      await shot(page, "ext-gitlab");
      await row.getByRole("button", { name: "Close" }).click();
      await expect(row.getByTestId("instance-settings")).toHaveCount(0);
    });

    test("journal: filter by action and person drops the theme noise", async ({ page }) => {
      test.setTimeout(BOOT_MS + 30_000);
      await reset(page);
      await open(page);
      // the tab strip is on screen and switches by a click, not only by the address
      const tab = page.getByRole("tab", { name: "Journal" });
      await expect(tab).toBeInViewport();
      expect((await tab.boundingBox())!.height, "the tab strip is not squeezed").toBeGreaterThan(24);
      await tab.click();
      await expect(page).toHaveURL(/tab=journal/);
      await expect(journalRows(page)).toHaveCount(60);
      await page.getByRole("combobox", { name: "Action" }).selectOption({ label: "Allow-list changed" });
      await expect(journalRows(page)).toHaveCount(15);
      for (const text of await journalRows(page).allTextContents()) expect(text).toMatch(/allowed/);
      await page.getByRole("combobox", { name: "Who" }).selectOption({ label: "Bob Builder" });
      await expect(page.getByText("No entries match these filters.")).toBeVisible();
      await page.getByRole("combobox", { name: "Who" }).selectOption({ label: "Ann Admin" });
      await expect(journalRows(page)).toHaveCount(15);
      await shot(page, "journal-filtered");
      await page.getByRole("button", { name: "Reset filters" }).first().click();
      await expect(journalRows(page)).toHaveCount(60);
    });

    test("themes tab: default theme and installed themes, the old address lands here", async ({ page }) => {
      test.setTimeout(BOOT_MS + 30_000);
      await reset(page);
      await open(page, "/w/board-fixture/instance/themes");
      await expect(page.getByRole("tab", { name: "Themes" })).toHaveAttribute("aria-selected", "true");
      await expect(page.getByText("Installed themes", { exact: true })).toBeVisible({ timeout: BOOT_MS });
      await expect(page.getByText("Default theme", { exact: true })).toBeVisible();
      await shot(page, "themes");
    });
  });
}

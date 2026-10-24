import { test, expect, type Locator, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
// Theme settings (Mesh Doc e2e/themes S1-S6) against the loopback fixture: real clicks only, no window.app.
// The fixture is a stand for the UI, not the backend: saving to the database is covered by the Go tests.
// Screenshots (S1 edited, S4 instance, S5 read-only; 1440/393 x light/dark) go to web/themes-shots, a CI artifact.
const API = "http://127.0.0.1:8019";
const SETTINGS_URL = "/w/board-fixture/settings";
const INSTANCE_URL = "/w/board-fixture/instance/themes";
const BOOT_MS = 60_000;
const SHOTS = process.env.THEMES_SHOTS ?? (process.env.CI ? "themes-shots" : undefined);
const LABEL = "e2e-label";

type Scheme = "light" | "dark";
const fixture = (page: Page, data: Record<string, unknown> = {}) =>
  page.request.post(`${API}/__fixture`, { data: { mode: "normal", role: "owner", resetTheme: true, ...data } });
const puts = async (page: Page): Promise<number> => ((await (await page.request.get(`${API}/__fixture`)).json()) as { themePuts: number }).themePuts;
const shot = async (page: Page, name: string) => {
  if (!SHOTS) return;
  mkdirSync(SHOTS, { recursive: true });
  const scheme = await page.evaluate(() => localStorage.getItem("theme"));
  // toasts are asserted where they appear; in the frames they would cover the bottom of the page
  const hide = await page.addStyleTag({ content: "[data-sonner-toaster]{display:none!important}" });
  await page.screenshot({ path: `${SHOTS}/${name}-${page.viewportSize()!.width}-${scheme}.png`, fullPage: true });
  await hide.evaluate((el) => (el as Element).remove());
};
// the editor on its own: a full-page frame cuts an inner scroll container off
const shotEl = async (page: Page, target: Locator, name: string) => {
  if (!SHOTS) return;
  mkdirSync(SHOTS, { recursive: true });
  const scheme = await page.evaluate(() => localStorage.getItem("theme"));
  await target.screenshot({ path: `${SHOTS}/${name}-${page.viewportSize()!.width}-${scheme}.png` });
};
// the viewport as the user sees it, after centring the row: the sticky Save bar must not cover it
const shotRow = async (page: Page, row: Locator, name: string) => {
  await row.evaluate((el) => el.scrollIntoView({ block: "center" }));
  const bar = await page.getByRole("button", { name: "Save theme" }).evaluate((el) => {
    const b = el.closest(".sticky")!.getBoundingClientRect();
    return { top: b.top };
  });
  const box = (await row.boundingBox())!;
  expect(box.y + box.height, "the row is above the sticky Save bar").toBeLessThanOrEqual(bar.top);
  if (!SHOTS) return;
  mkdirSync(SHOTS, { recursive: true });
  const scheme = await page.evaluate(() => localStorage.getItem("theme"));
  await page.screenshot({ path: `${SHOTS}/${name}-${page.viewportSize()!.width}-${scheme}.png` });
};
const prepare = async (page: Page, scheme: Scheme) => {
  await page.addInitScript((t) => localStorage.setItem("theme", t), scheme);
  // the fixture's cards carry no labels and all share one priority: give them both so a field change is visible on the board
  await page.route(/\/projects\/[^/]+\/tasks(\?|$)/, async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    body.items = body.items.map((t: Record<string, unknown>) => ({ ...t, labels: [LABEL], priority: "urgent" }));
    await route.fulfill({ response: res, json: body });
  });
};
const themeTab = (page: Page) => page.getByRole("tab", { name: "Theme" }).or(page.getByRole("button", { name: "Theme", exact: true })).first();
const openThemeTab = async (page: Page, scheme: Scheme) => {
  await prepare(page, scheme);
  await page.goto(SETTINGS_URL);
  await themeTab(page).click({ timeout: BOOT_MS });
  await page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}" });
};
const toBoard = async (page: Page) => {
  await page.getByRole("link", { name: /ContentHub fixture/ }).first().click();
  await expect(page.locator('[data-testid="task-card"]').first()).toBeVisible({ timeout: BOOT_MS });
};
const backToTheme = async (page: Page) => {
  await page.goBack();
  await themeTab(page).click({ timeout: BOOT_MS });
};

test.afterEach(async ({ request }) => {
  await request.post(`${API}/__fixture`, { data: { mode: "normal", role: "owner", resetTheme: true } });
});

// Screens for the visual review: the same three views in every viewport/scheme.
for (const [width, scheme] of [[1440, "light"], [1440, "dark"], [393, "light"], [393, "dark"]] as const) {
  test.describe(`screens ${width} ${scheme}`, () => {
    test.use({ viewport: { width, height: 900 } });

    test("S1 owner edit, S4 instance, S5 member read-only", async ({ page }) => {
      test.setTimeout(BOOT_MS + 90_000);
      await fixture(page);
      await openThemeTab(page, scheme);
      await page.getByRole("radio", { name: /Graphite/ }).click({ timeout: BOOT_MS });
      await page.getByLabel("Add to Card fields").selectOption({ label: "Priority" });
      await page.getByLabel("Accent colour", { exact: true }).fill("#336699");
      await shotRow(page, page.getByLabel("Remove: Priority"), "S1-card-fields");
      await shot(page, "S1-edited");
      await shotEl(page, page.locator("form").filter({ has: page.getByLabel("Accent colour", { exact: true }) }), "S1-editor");

      await page.goto(INSTANCE_URL);
      await expect(page.getByText("Installed themes", { exact: true })).toBeVisible({ timeout: BOOT_MS });
      await shot(page, "S4-instance");
      await page.getByRole("button", { name: /Install slate/i }).click();
      await expect(page.getByText("Theme installed")).toBeVisible();
      await page.getByRole("button", { name: "Update" }).click();
      await expect(page.getByText("Forge updated")).toBeVisible();
      await shot(page, "S4-instance-installed-updated");
      await page.getByRole("button", { name: "Remove Slate" }).click();
      await expect(page.getByRole("button", { name: "Remove", exact: true })).toBeVisible();
      await shot(page, "S4-remove-dialog");
      await page.getByRole("button", { name: "Remove", exact: true }).click();
      await expect(page.getByRole("button", { name: "Remove Slate" })).toHaveCount(0);

      await fixture(page, { role: "member" });
      await page.goto(SETTINGS_URL);
      await themeTab(page).click({ timeout: BOOT_MS });
      await expect(page.getByText(/Only workspace owners and admins/)).toBeVisible({ timeout: BOOT_MS });
      await shot(page, "S5-readonly");
    });
  });
}

test.describe("behaviour (1440 light)", () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test("S1+S2 pick a theme, change card_fields, the board shows them, reset", async ({ page }) => {
    test.setTimeout(BOOT_MS + 90_000);
    await fixture(page);
    // the pre-paint script asks the instance theme through the dev proxy: it must reach the fixture, not a dead port
    const prepaint = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/v1/theme" && r.ok(), { timeout: BOOT_MS });
    await openThemeTab(page, "light");
    await prepaint;
    await expect(page.getByRole("button", { name: "Save theme" })).toBeDisabled();
    await page.getByRole("radio", { name: /Graphite/ }).click({ timeout: BOOT_MS });

    // card_fields: Priority, then Labels, then move Labels up so the order is Labels, Priority
    const add = page.getByLabel("Add to Card fields");
    await add.selectOption({ label: "Priority" });
    await expect(page.getByLabel("Remove: Priority")).toBeVisible();
    await add.selectOption({ label: "Labels" });
    await page.getByLabel("Move up: Labels").click();
    await expect(page.getByLabel("Move up: Labels")).toBeDisabled(); // Labels is first now
    await page.getByLabel("Remove: Labels").click();
    await expect(page.getByLabel("Remove: Labels")).toHaveCount(0);

    // a malformed colour blocks the save, whatever the box kept of it; the range check of "H S% L%" is unit-tested: no request leaves the page
    const accent = page.getByLabel("Accent colour", { exact: true }); // the colour picker next to it is a separate control
    for (const bad of ["#12", "#gggggg", "999 999% 999%"]) {
      await accent.fill(bad);
      // the box has maxlength=7 (a hex colour): record what it really holds instead of assuming the whole string went in
      const shown = await accent.inputValue();
      test.info().annotations.push({ type: "colour attempt", description: `typed "${bad}", the field holds "${shown}"` });
      console.log(`colour attempt: typed "${bad}", the field holds "${shown}"`);
      expect(shown).not.toBe("");
      await page.getByRole("button", { name: "Save theme" }).click();
      await expect(page.getByText("Use #rgb or #rrggbb")).toBeVisible();
    }
    expect(await puts(page)).toBe(0);

    // a valid colour is saved; the contrast warning is shown and does not block
    await accent.fill("#ff0000");
    await page.getByRole("button", { name: "Save theme" }).click();
    await expect(page.getByText("Theme saved")).toBeVisible();
    await expect(page.getByText("Low contrast in this theme")).toBeVisible();
    expect(await puts(page)).toBe(1);

    // no reload: the board has to pick the new fields up from the re-applied theme
    await toBoard(page);
    await expect(page.locator('[data-testid="task-card"] [aria-label="Urgent"]').first()).toBeVisible(); // the Priority flag
    await expect(page.getByText(LABEL)).toHaveCount(0);

    await backToTheme(page);
    await page.getByRole("button", { name: "Reset to the instance theme" }).click();
    await expect(page.getByText("The workspace now uses the instance theme")).toBeVisible();
    await toBoard(page);
    await expect(page.getByText(LABEL).first()).toBeVisible({ timeout: BOOT_MS });
  });

  test("S3 the workspace icon is uploaded from the Theme tab", async ({ page }) => {
    test.setTimeout(BOOT_MS + 30_000);
    await fixture(page);
    await openThemeTab(page, "light");
    const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
    await page.locator('input[type="file"][accept="image/png"]').setInputFiles({ name: "icon.png", mimeType: "image/png", buffer: png });
    await expect(page.getByText("Icon updated.")).toBeVisible();
    await expect(page.locator('img[src^="data:image/png"]').first()).toBeVisible();
  });

  test("S4 instance: install a built-in theme, update a repository theme, set the default theme", async ({ page }) => {
    test.setTimeout(BOOT_MS + 60_000);
    await fixture(page);
    await prepare(page, "light");
    await page.goto(INSTANCE_URL);
    await expect(page.getByText("Installed themes", { exact: true })).toBeVisible({ timeout: BOOT_MS });
    await page.getByRole("button", { name: /Install slate/i }).click();
    await expect(page.getByText("Theme installed")).toBeVisible();
    await page.getByRole("button", { name: "Update" }).click();
    await expect(page.getByText("Forge updated")).toBeVisible();
    await page.getByRole("radio", { name: /Slate/ }).first().click();
    await page.getByRole("button", { name: "Save theme" }).click();
    await expect(page.getByText("Theme saved")).toBeVisible();
  });

  test("S5 member: read-only, no request is sent", async ({ page }) => {
    test.setTimeout(BOOT_MS + 30_000);
    await fixture(page, { role: "member" });
    await openThemeTab(page, "light");
    await expect(page.getByText(/Only workspace owners and admins/)).toBeVisible({ timeout: BOOT_MS });
    await expect(page.getByRole("radio", { name: /Graphite/ })).toBeDisabled();
    await expect(page.getByRole("button", { name: "Save theme" })).toHaveCount(0);
    expect(await puts(page)).toBe(0);
  });

  test("S6 loading, error with retry, empty", async ({ page }) => {
    test.setTimeout(BOOT_MS + 60_000);
    // loading -> ready
    await fixture(page, { themesDelay: 2500 });
    await prepare(page, "light");
    await page.goto(SETTINGS_URL);
    await themeTab(page).click({ timeout: BOOT_MS });
    await expect(page.getByLabel("Loading themes")).toBeVisible();
    await expect(page.getByRole("radio", { name: /Graphite/ })).toBeVisible({ timeout: BOOT_MS });

    // error -> retry -> ready
    await fixture(page, { failThemes: 1 });
    await page.reload();
    await themeTab(page).click({ timeout: BOOT_MS });
    await expect(page.getByText("Couldn't load themes.")).toBeVisible({ timeout: BOOT_MS });
    await page.getByRole("button", { name: "Try again" }).click();
    await expect(page.getByRole("radio", { name: /Graphite/ })).toBeVisible();

    // empty
    await fixture(page, { installedEmpty: true });
    await page.goto(INSTANCE_URL);
    await expect(page.getByText("No themes installed")).toBeVisible({ timeout: BOOT_MS });
  });
});

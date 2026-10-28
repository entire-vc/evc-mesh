import { test, expect, type Locator, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
// Theme settings (Mesh Doc e2e/themes S1-S6) against the loopback fixture: real clicks only, no window.app.
// Since U4 a theme lives in Extensions like any extension: its own catalog section, the Installed switch, and its
// settings in the same Settings window; Workspace Settings → Theme only links there.
// The fixture is a stand for the UI, not the backend: saving to the database is covered by the Go tests.
// Screenshots (S1 edited, S4 instance, S5 read-only; 1440/393 x light/dark) go to web/themes-shots, a CI artifact.
// A merge request that changes this file repeats the 393 screens and the in-flight test 20 times in authed-e2e.
const API = "http://127.0.0.1:8019";
const SETTINGS_URL = "/w/board-fixture/settings";
const CATALOG_URL = "/w/board-fixture/extensions";
const INSTALLED_URL = "/w/board-fixture/extensions?tab=installed";
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
const freeze = (page: Page) =>
  page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}" });
const installedRow = (page: Page, id: string) => page.locator(`[data-ext="${id}"]`);
// the theme's Settings window, opened the way a user does it: Extensions → Installed → Settings on the theme's row
const openThemeSettings = async (page: Page, scheme: Scheme, id = "graphite") => {
  await prepare(page, scheme);
  await page.goto(INSTALLED_URL);
  await installedRow(page, id).getByRole("button", { name: "Settings" }).click({ timeout: BOOT_MS });
  await freeze(page);
  return page.getByRole("dialog");
};
const themeTab = (page: Page) => page.getByRole("tab", { name: "Theme" }).or(page.getByRole("button", { name: "Theme", exact: true })).first();
const openThemeTab = async (page: Page, scheme: Scheme) => {
  await prepare(page, scheme);
  await page.goto(SETTINGS_URL);
  await themeTab(page).click({ timeout: BOOT_MS });
  await page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}" });
};
const closeDialog = async (page: Page) => {
  await page.getByRole("dialog").getByRole("button", { name: "Close", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
};
const toBoard = async (page: Page) => {
  await page.getByRole("link", { name: /ContentHub fixture/ }).first().click();
  await expect(page.locator('[data-testid="task-card"]').first()).toBeVisible({ timeout: BOOT_MS });
};
// A toast opened under the pointer stays open while hovered (sonner pauses its timer), and at 393 the toasts cover the
// buttons at the bottom of the page. Move the pointer away, as a user would, and let them close before the next click.
const clearToasts = async (page: Page) => {
  await page.mouse.move(0, 0);
  await expect(page.locator("[data-sonner-toast]")).toHaveCount(0, { timeout: 10_000 });
};
const backToTheme = async (page: Page) => {
  await page.goBack();
  await installedRow(page, "graphite").getByRole("button", { name: "Settings" }).click({ timeout: BOOT_MS });
};

// The board refetches /tasks after the last assertion of a test can already pass. Teardown closes the context, which
// disposes the route.fetch() response still being read in the handler ("Response has been disposed" at res.json()).
// Waiting for those handlers is not enough: with two requests in flight, the first to finish sees no routes left and
// Playwright drops the interception, which settles the second one under its handler ("Route is already handled!").
// What a handler does after the test has ended is not part of the test: unroute and ignore its errors.
test.afterEach(async ({ page, request }) => {
  await page.unrouteAll({ behavior: "ignoreErrors" });
  await request.post(`${API}/__fixture`, { data: { mode: "normal", role: "owner", resetTheme: true } });
});

// Screens for the visual review: the same three views in every viewport/scheme.
for (const [width, scheme] of [[1440, "light"], [1440, "dark"], [393, "light"], [393, "dark"]] as const) {
  test.describe(`screens ${width} ${scheme}`, () => {
    test.use({ viewport: { width, height: 900 } });

    test("U4 catalog and installed, S1 owner edit, S4 instance, S5 member read-only", async ({ page }) => {
      test.setTimeout(BOOT_MS + 120_000);
      await fixture(page);
      await prepare(page, scheme);
      await page.goto(CATALOG_URL);
      await expect(page.getByTestId("section-themes")).toBeVisible({ timeout: BOOT_MS });
      await freeze(page);
      await shot(page, "U4-catalog");
      await page.goto(INSTALLED_URL);
      await expect(installedRow(page, "graphite")).toBeVisible({ timeout: BOOT_MS });
      await shot(page, "U4-installed");

      const dialog = await openThemeSettings(page, scheme);
      await dialog.getByRole("radio", { name: /Graphite/ }).click({ timeout: BOOT_MS });
      // an unset accent: the swatch shows the colour that applies (the one in the placeholder), not black
      const accent = dialog.getByLabel("Accent colour", { exact: true });
      await expect(accent).toHaveValue("");
      expect(await dialog.getByLabel("Pick a colour: Accent colour").inputValue()).toBe(await accent.getAttribute("placeholder"));
      // Save / Reset sit below the scroll area, so they cannot cover a theme card
      const body = (await dialog.getByTestId("theme-settings-body").boundingBox())!;
      expect((await dialog.getByRole("button", { name: "Save theme" }).boundingBox())!.y).toBeGreaterThanOrEqual(body.y + body.height);
      await shotEl(page, dialog, "S1-settings-window-unset");
      await dialog.getByLabel("Add to Card fields").selectOption({ label: "Priority" });
      await dialog.getByLabel("Accent colour", { exact: true }).fill("#336699");
      await shot(page, "S1-edited");
      await shotEl(page, dialog, "S1-settings-window");
      await closeDialog(page);

      await page.goto(SETTINGS_URL);
      await themeTab(page).click({ timeout: BOOT_MS });
      await expect(page.getByRole("link", { name: /Theme is in Extensions/ })).toBeVisible({ timeout: BOOT_MS });
      await shot(page, "U4-workspace-settings");

      await page.goto(INSTANCE_URL);
      await expect(page.getByText("Installed themes", { exact: true })).toBeVisible({ timeout: BOOT_MS });
      // nothing edited yet: the form's defaults and a second load of the saved theme are not an edit
      await expect(page.getByRole("button", { name: "Save theme" })).toBeDisabled();
      await expect(page.getByText("Unsaved changes", { exact: true })).toHaveCount(0);
      await shot(page, "S4-instance");
      await page.getByRole("button", { name: /Install slate/i }).click();
      await expect(page.getByText("Theme installed")).toBeVisible();
      await page.getByRole("button", { name: "Update" }).click();
      await expect(page.getByText("Forge updated")).toBeVisible();
      await shot(page, "S4-instance-installed-updated");
      await clearToasts(page);
      await page.getByRole("button", { name: "Remove Slate" }).click();
      await expect(page.getByRole("button", { name: "Remove", exact: true })).toBeVisible();
      await shot(page, "S4-remove-dialog");
      await page.getByRole("button", { name: "Remove", exact: true }).click();
      await expect(page.getByRole("button", { name: "Remove Slate" })).toHaveCount(0);

      await fixture(page, { role: "member" });
      await page.goto(INSTALLED_URL);
      await expect(installedRow(page, "graphite").getByRole("button", { name: "Settings" })).toBeDisabled({ timeout: BOOT_MS });
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
    await openThemeSettings(page, "light");
    await prepaint;
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
    await closeDialog(page);
    await toBoard(page);
    await expect(page.locator('[data-testid="task-card"] [aria-label="Urgent"]').first()).toBeVisible(); // the Priority flag
    await expect(page.getByText(LABEL)).toHaveCount(0);

    await backToTheme(page);
    await page.getByRole("button", { name: "Reset to the instance theme" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Reset", exact: true }).click();
    await expect(page.getByText("The workspace now uses the instance theme")).toBeVisible();
    await closeDialog(page);
    await toBoard(page);
    await expect(page.getByText(LABEL).first()).toBeVisible({ timeout: BOOT_MS });
  });

  test("U4 a theme is listed in its own section and switched on like an extension", async ({ page }) => {
    test.setTimeout(BOOT_MS + 60_000);
    await fixture(page);
    await prepare(page, "light");
    await page.goto(CATALOG_URL);
    const themes = page.getByTestId("section-themes");
    await expect(themes).toBeVisible({ timeout: BOOT_MS });
    await expect(themes.getByRole("link", { name: /Graphite/ })).toBeVisible();
    await expect(themes.getByRole("link", { name: /GitLab/ })).toHaveCount(0);
    await expect(themes.locator('[data-kind="theme"]').first()).toBeVisible();

    // the old connect route of a theme lands in its Settings window
    await page.goto("/w/board-fixture/extensions/graphite/connect");
    await expect(page.getByRole("dialog", { name: "Graphite" })).toBeVisible({ timeout: BOOT_MS });
    await closeDialog(page);

    const sw = installedRow(page, "graphite").getByRole("switch");
    await expect(sw).toHaveAttribute("aria-checked", "false");
    await sw.click();
    await expect(page.getByText("Graphite is on in this workspace.")).toBeVisible();
    await expect(sw).toHaveAttribute("aria-checked", "true");
    expect(await puts(page)).toBe(1);

    // Workspace Settings → Theme points to it, and no longer holds a theme picker
    await page.goto(SETTINGS_URL);
    await themeTab(page).click({ timeout: BOOT_MS });
    await expect(page.getByRole("link", { name: "Theme is in Extensions → Graphite" })).toBeVisible({ timeout: BOOT_MS });
    await expect(page.getByRole("radio", { name: /Graphite/ })).toHaveCount(0);
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
    await clearToasts(page);
    await page.getByRole("radio", { name: /Slate/ }).first().click();
    await page.getByRole("button", { name: "Save theme" }).click();
    await expect(page.getByText("Theme saved")).toBeVisible();
  });

  test("S5 member: read-only, no request is sent", async ({ page }) => {
    test.setTimeout(BOOT_MS + 30_000);
    await fixture(page, { role: "member" });
    await prepare(page, "light");
    await page.goto(INSTALLED_URL);
    const row = installedRow(page, "graphite");
    await expect(row.getByRole("button", { name: "Settings" })).toBeDisabled({ timeout: BOOT_MS });
    await expect(row.getByRole("switch")).toBeDisabled();
    expect(await puts(page)).toBe(0);
  });

  // The teardown race of the /tasks handler, made deterministic: the themes list is held by the fixture, so the test
  // ends while route.fetch() calls are still in flight. With unrouteAll "wait" in afterEach this failed 2 of 20.
  test("a route handler in flight at the end of a test does not fail it", async ({ page }) => {
    test.setTimeout(BOOT_MS + 30_000);
    await fixture(page, { themesDelay: 3000 });
    await page.route(/\/api\/v1\/themes(\?|$)/, async (route) => {
      const res = await route.fetch();
      await route.fulfill({ response: res, json: await res.json() });
    });
    const sent = page.waitForRequest((r) => new URL(r.url()).pathname === "/api/v1/themes", { timeout: BOOT_MS });
    await page.goto(SETTINGS_URL);
    await themeTab(page).click({ timeout: BOOT_MS });
    await sent;
  });

  test("S6 loading, error with retry, empty", async ({ page }) => {
    test.setTimeout(BOOT_MS + 60_000);
    // loading -> ready
    await fixture(page, { themesDelay: 2500 });
    await openThemeSettings(page, "light");
    await expect(page.getByLabel("Loading themes")).toBeVisible();
    await expect(page.getByRole("radio", { name: /Graphite/ })).toBeVisible({ timeout: BOOT_MS });

    // error -> retry -> ready
    await fixture(page, { failThemes: 1 });
    await page.goto(`${INSTALLED_URL}&settings=graphite`);
    await expect(page.getByText("Couldn't load themes.")).toBeVisible({ timeout: BOOT_MS });
    await page.getByRole("button", { name: "Try again" }).click();
    await expect(page.getByRole("radio", { name: /Graphite/ })).toBeVisible();

    // empty
    await fixture(page, { installedEmpty: true });
    await page.goto(INSTANCE_URL);
    await expect(page.getByText("No themes installed")).toBeVisible({ timeout: BOOT_MS });
  });
});

import { test, expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";

// The project tab strip shows as many tabs as fit and moves the rest into the "…"
// menu, Calendar first. At 1440 every built-in tab is in the strip; at 393 Calendar
// is in the menu and opens from there. Screenshots of the strip from this branch
// and from the merge-request base go to e2e-tabs/shots/ for the review.

const TABS = ["Board", "List", "Dependencies", "Calendar", "Docs", "Updates"];
// TABS_IMPL=before runs the two behaviour tests against the base strip: the red run.
const IMPL = process.env.TABS_IMPL ?? "after";
const SHOTS = "e2e-tabs/shots";
mkdirSync(SHOTS, { recursive: true });

const open = async (page: Page, width: number, theme = "light", impl = IMPL, extra = "") => {
  await page.setViewportSize({ width, height: 400 });
  await page.goto(`/e2e-tabs/harness.html?theme=${theme}&impl=${impl}${extra}`);
};
const moreViews = (page: Page) => page.getByRole("button", { name: /more views/i });

test("1440: every built-in tab is in the strip and none is in the menu", async ({ page }) => {
  await open(page, 1440);
  for (const name of TABS) await expect(page.getByRole("button", { name, exact: true })).toBeVisible();
  await moreViews(page).click();
  await expect(page.getByRole("menuitem", { name: "Calendar" })).toHaveCount(0);
  // Dependencies and Updates are tabs now: the base strip also listed them under Formats.
  await expect(page.getByRole("menuitem", { name: "Dependencies" })).toHaveCount(0);
  await expect(page.getByRole("menuitem", { name: "Updates" })).toHaveCount(0);
});

test("1440: a tab supplied as data appears in the strip", async ({ page }) => {
  await open(page, 1440, "light", IMPL, "&extra=1");
  // Two tabs share the Docs label here, so a strip that renders the list has two.
  await expect(page.getByRole("button", { name: "Docs", exact: true })).toHaveCount(2);
});

// Chat has no server-side saved-view type, like Docs: "Save current view" must not be offered on it.
test("393: the chat tab does not offer to save the view", async ({ page }) => {
  await open(page, 393, "light", IMPL, "&chat=1");
  await moreViews(page).click();
  await expect(page.getByRole("menuitem").first()).toBeVisible();
  await expect(page.getByRole("menuitem", { name: /save current view/i })).toHaveCount(0);
});

test("393: Calendar is first in the menu, opens from there, and the trigger carries the active state", async ({ page }) => {
  await open(page, 393);
  await expect(page.getByRole("button", { name: "Board", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Calendar", exact: true })).toHaveCount(0);

  await moreViews(page).click();
  await expect(page.getByRole("menuitem").first()).toHaveText("Calendar");
  await page.getByRole("menuitem").first().click();
  await expect(page.getByTestId("path")).toHaveText("/w/w/p/p/calendar");

  await moreViews(page).click();
  await expect(page.getByRole("menuitem", { name: "Calendar" })).toHaveAttribute("aria-current", "page");
});

// A project with Chat turned on: Chat is the tab right after Docs, at every width. The hidden
// measuring copy holds the full order; at 393 Chat itself may be in the menu, after Docs there too.
const order = (page: Page) =>
  page.getByTestId("project-tabs-order").locator("[data-tab-id]").evaluateAll((els) => els.map((el) => el.getAttribute("data-tab-id")));
for (const width of [1440, 393]) {
  test(`${width}: Chat stands right after Docs`, async ({ page }) => {
    await open(page, width, "light", IMPL, "&chat=1");
    await expect(page.getByRole("button", { name: "Board", exact: true })).toBeVisible();
    const ids = await order(page);
    expect(ids.indexOf("chat"), ids.join(",")).toBe(ids.indexOf("docs") + 1);
    const shown = await page.locator('button[data-tab-id]').evaluateAll((els) => els.map((el) => el.getAttribute("data-tab-id")));
    if (shown.includes("chat")) expect(shown.indexOf("chat")).toBe(shown.indexOf("docs") + 1);
    else {
      await moreViews(page).click();
      const menu = await page.locator('[role="menuitem"][data-tab-id]').evaluateAll((els) => els.map((el) => el.getAttribute("data-tab-id")));
      expect(menu, "Chat not in the strip is in the menu").toContain("chat");
      if (menu.includes("docs")) expect(menu.indexOf("chat")).toBe(menu.indexOf("docs") + 1);
    }
  });
}

// The harness wraps the strip in the real mobile header class, so the "…" trigger
// takes its 44px touch target. At every width the shown tabs and the trigger must
// sit side by side inside the strip: a tab that does not fit belongs in the menu.
test("393 and around: shown tabs never overlap or overflow past the trigger", async ({ page }) => {
  for (let width = 260; width <= 460; width += 4) {
    await open(page, width);
    // The harness renders after load: measure only once the trigger exists.
    await expect(moreViews(page)).toBeVisible();
    const geo = await page.evaluate(() => {
      const trigger = document.querySelector('button[title*="ore views" i]')!;
      const strip = trigger.closest(".relative")!;
      const tabs = Array.from(strip.children)
        .filter((el) => el.tagName === "BUTTON" && !el.contains(trigger))
        .map((el) => el.getBoundingClientRect().right);
      const t = trigger.getBoundingClientRect();
      return { lastTab: Math.max(0, ...tabs), triggerLeft: t.left, triggerRight: t.right, stripRight: strip.getBoundingClientRect().right };
    });
    expect(geo.lastTab, `width ${width}`).toBeLessThanOrEqual(geo.triggerLeft + 0.5);
    expect(geo.triggerRight, `width ${width}`).toBeLessThanOrEqual(geo.stripRight + 0.5);
  }
});

for (const impl of ["before", "after"]) {
  for (const width of [1440, 393]) {
    for (const theme of ["light", "dark"]) {
      test(`shot ${impl} ${width} ${theme}`, async ({ page }) => {
        await open(page, width, theme, impl);
        await expect(page.getByRole("button", { name: "Board", exact: true })).toBeVisible();
        await page.screenshot({ path: `${SHOTS}/${impl}-${width}-${theme}.png`, clip: { x: 0, y: 0, width, height: 90 } });
      });
    }
  }
}

// The strip of a project with Chat, as the review sees it: at 393 the menu is open when Chat sits there.
for (const width of [1440, 393]) {
  for (const theme of ["light", "dark"]) {
    test(`shot chat ${width} ${theme}`, async ({ page }) => {
      await open(page, width, theme, IMPL, "&chat=1");
      await expect(page.getByRole("button", { name: "Board", exact: true })).toBeVisible();
      if (!(await page.locator('button[data-tab-id="chat"]').isVisible())) {
        await moreViews(page).click();
        await expect(page.locator('[role="menuitem"][data-tab-id="chat"]')).toBeVisible();
      }
      await page.screenshot({ path: `${SHOTS}/chat-${width}-${theme}.png`, clip: { x: 0, y: 0, width, height: width === 393 ? 400 : 90 } });
    });
  }
}

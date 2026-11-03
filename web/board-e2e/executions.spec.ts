import { test, expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
// Agent executions (card an internal ticket): configured route vs observed attempts in the org chart and the agent detail
// "Route & runs" tab, against the loopback fixture (/__fixture {exec}). The fixture stands in for the API: this proves the
// UI, not a live route switch. Screenshots (1440/393 x light/dark) go to web/executions-shots, a CI artifact.
const API = "http://127.0.0.1:8019";
const CHART = "/w/board-fixture/org-chart";
test.describe.configure({ timeout: 60_000 });
test.use({ actionTimeout: 10_000 });
// A failed story prints its error and what the page showed right away, so a red run is readable from the job log.
test.afterEach(async ({ page }, info) => {
  if (info.status === info.expectedStatus) return;
  const text = await page.locator("body").innerText({ timeout: 3_000 }).catch(() => "<no page>");
  console.log(`FAILED ${info.title}\n${(info.error?.message ?? "").slice(0, 700)}\nPAGE: ${text.replace(/\s+/g, " ").slice(0, 500)}`);
});
const SHOTS = process.env.EXECUTIONS_SHOTS ?? (process.env.CI ? "executions-shots" : undefined);

type Scheme = "light" | "dark";
const SIZES = [{ w: 1440, h: 900 }, { w: 393, h: 852 }] as const;

const fixture = (page: Page, exec: string) => page.request.post(`${API}/__fixture`, { data: { mode: "normal", role: "owner", exec } });
const shot = async (page: Page, name: string) => {
  if (!SHOTS) return;
  mkdirSync(SHOTS, { recursive: true });
  const scheme = await page.evaluate(() => localStorage.getItem("theme"));
  await page.screenshot({ path: `${SHOTS}/${name}-${page.viewportSize()!.width}-${scheme}.png`, fullPage: true });
};
const freeze = (page: Page) =>
  page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}[data-sonner-toaster]{display:none!important}" });

// Console errors and failing API calls of one walk; the error scenario expects its own 503 and opts out.
const watch = (page: Page) => {
  const bad: string[] = [];
  page.on("console", (m) => { if (m.type() === "error") bad.push(`console: ${m.text()}`); });
  page.on("pageerror", (e) => bad.push(`pageerror: ${e.message}`));
  page.on("response", (r) => { if (r.status() >= 500) bad.push(`${r.status()} ${r.url()}`); });
  return bad;
};
const open = async (page: Page, scheme: Scheme, size: { w: number; h: number }, exec: string) => {
  await fixture(page, exec);
  await page.setViewportSize({ width: size.w, height: size.h });
  await page.addInitScript((t) => localStorage.setItem("theme", t), scheme);
  await page.goto(CHART);
  await expect(page.getByText("Taylor", { exact: true }).first()).toBeVisible({ timeout: 20_000 });
  await freeze(page);
};
// The app's Dialog is a plain overlay without role="dialog": find the one that holds the agent tabs.
const detailOf = (page: Page) => page.locator("div.fixed.inset-0.z-50").filter({ has: page.getByRole("tab", { name: "Route & runs" }) });
const openDetailRoute = async (page: Page) => {
  await page.getByText("Taylor", { exact: true }).first().click();
  await page.getByRole("tab", { name: "Route & runs" }).click();
};

for (const scheme of ["light", "dark"] as const) {
  for (const size of SIZES) {
    test.describe(`${size.w} ${scheme}`, () => {
      test("S1 project selection: configured policy and all concurrent attempts, detail agrees with the chart", async ({ page }) => {
        const bad = watch(page);
        await open(page, scheme, size, "main");
        const configured = page.getByTestId("exec-configured").first();
        await expect(configured).toContainText("r17");
        await expect(configured).toContainText("gpt-6.1-sol");
        const observed = page.getByTestId("exec-observed").first();
        await expect(observed).toContainText("3 runs");
        await shot(page, "s1-chart-collapsed");
        // selecting the other project changes the configured policy, not the observed block of the first
        await page.locator('select[aria-label="Project"]').selectOption({ label: "Other project" });
        await expect(page.getByTestId("exec-configured").first()).toContainText("r18");
        await expect(page.getByTestId("exec-configured").first()).toContainText("gpt-6.2");
        await shot(page, "s1-chart-other-project");
        await page.locator('select[aria-label="Project"]').selectOption({ label: "All projects" });
        await observed.getByRole("button", { name: /3 runs/ }).click();
        const attempts = page.getByTestId("exec-attempts");
        await expect(attempts.locator("li")).toHaveCount(3);
        await expect(attempts).toContainText("gpt-6.1-sol");
        await expect(attempts).toContainText("opus-5");
        await expect(attempts).toContainText("by adapter");
        await shot(page, "s1-chart-expanded");
        await openDetailRoute(page);
        const rows = page.getByTestId("exec-attempt");
        await expect(rows).toHaveCount(3);
        await expect(detailOf(page)).toContainText("gpt-6.1-sol");
        await expect(detailOf(page)).toContainText("opus-5");
        await expect(detailOf(page)).toContainText("Policy r17");
        await expect(detailOf(page)).toContainText("by adapter");
        await shot(page, "s1-detail");
        expect(bad, bad.join("\n")).toEqual([]);
      });

      test("S2 unknown model and no usage are shown as unknown, source and age kept", async ({ page }) => {
        const bad = watch(page);
        await open(page, scheme, size, "unknown");
        const observed = page.getByTestId("exec-observed").first();
        await expect(observed).toContainText("Model unknown");
        await expect(observed).toContainText("by adapter");
        await expect(observed).not.toContainText("gpt-6.1-sol");
        await shot(page, "s2-unknown-chart");
        await openDetailRoute(page);
        await expect(detailOf(page)).toContainText("Model unknown");
        // a project without runs under the filter says so, instead of borrowing a model from the profile or the policy
        await detailOf(page).getByLabel("Project").selectOption({ label: "Other project" });
        await expect(detailOf(page)).toContainText("No runs match the filters");
        await shot(page, "s2-unknown-detail");
        expect(bad, bad.join("\n")).toEqual([]);
      });

      test("S2b no usage at all: no run is invented, the model stays unknown", async ({ page }) => {
        const bad = watch(page);
        await open(page, scheme, size, "nousage");
        await expect(page.getByTestId("exec-observed").first()).toContainText("No active runs");
        await expect(page.getByTestId("exec-observed").first()).not.toContainText("gpt-6.1-sol");
        await shot(page, "s2b-nousage-chart");
        await openDetailRoute(page);
        await expect(detailOf(page)).toContainText("No runs reported for this project yet");
        await expect(detailOf(page)).toContainText("Model stays unknown until a run is observed");
        await shot(page, "s2b-nousage-detail");
        expect(bad, bad.join("\n")).toEqual([]);
      });

      test("S3 stale and offline are marked", async ({ page }) => {
        const bad = watch(page);
        await open(page, scheme, size, "stale");
        await expect(page.getByTestId("exec-observed").first()).toContainText("stale");
        await shot(page, "s3-stale-chart");
        await openDetailRoute(page);
        await expect(detailOf(page)).toContainText("stale");
        await expect(detailOf(page)).toContainText("offline");
        await shot(page, "s3-stale-detail");
        expect(bad, bad.join("\n")).toEqual([]);
      });

      test("S4 unmanaged and disabled show the profile with its source; profile edit promises no route change", async ({ page }) => {
        const bad = watch(page);
        await open(page, scheme, size, "unmanaged");
        await expect(page.getByTestId("exec-unmanaged").first()).toContainText("profile");
        await expect(page.getByTestId("exec-configured")).toHaveCount(0);
        await shot(page, "s4-unmanaged-chart");
        await openDetailRoute(page);
        await expect(detailOf(page)).toContainText("Not managed by a Runner in this project");
        await expect(detailOf(page)).toContainText("Runner is connected but disabled for this project");
        await expect(detailOf(page)).toContainText("claude · sonnet (agent_profile)");
        await shot(page, "s4-unmanaged-detail");
        await page.getByRole("tab", { name: "Profile" }).click();
        await expect(detailOf(page)).toContainText("Editing the profile does not change a managed route");
        await shot(page, "s4-profile-note");
        expect(bad, bad.join("\n")).toEqual([]);
      });

      test("S5 an API error is an error, never unmanaged", async ({ page }) => {
        await open(page, scheme, size, "error");
        await expect(page.getByTestId("exec-error").first()).toContainText("Run data unavailable");
        await expect(page.getByTestId("exec-unmanaged")).toHaveCount(0);
        await shot(page, "s5-error-chart");
        await openDetailRoute(page);
        await expect(detailOf(page).getByTestId("exec-error")).toContainText("Run data unavailable");
        await shot(page, "s5-error-detail");
      });

      test("S6 more than 20 rows: first page of 20, show more reaches the last, long id wraps, filters narrow", async ({ page }) => {
        const bad = watch(page);
        await open(page, scheme, size, "many");
        await openDetailRoute(page);
        const rows = page.getByTestId("exec-attempt");
        await expect(rows).toHaveCount(20);
        await shot(page, "s6-many-first-page");
        await page.getByRole("button", { name: /Show 5 more/ }).click();
        await expect(rows).toHaveCount(25);
        await rows.last().scrollIntoViewIfNeeded();
        await expect(rows.last()).toBeVisible();
        // the long id wraps inside the dialog instead of widening the page
        const dialog = detailOf(page).locator("div.relative.z-50").first();
        const overflow = await dialog.evaluate((el) => el.scrollWidth - el.clientWidth);
        expect(overflow, "the dialog scrolls sideways").toBeLessThanOrEqual(1);
        await shot(page, "s6-many-last");
        await page.getByPlaceholder(/Search by run/).fill("att-many-07");
        await expect(rows).toHaveCount(1);
        await shot(page, "s6-many-filtered");
        expect(bad, bad.join("\n")).toEqual([]);
      });
    });
  }
}

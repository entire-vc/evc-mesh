import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { loginWithRetry, pinUiLocale } from "./auth-helper";
import { rateLimitAware } from "./api-retry";

/**
 * E2E: "Print dashboard" puts the whole Analytics dashboard on paper (PRD §11.6 / §2.1).
 *
 * The app shell is one screen tall with its own scroller (h-screen, overflow-hidden, main overflow-y-auto),
 * so print used to get exactly that box: one A4 sheet, the bottom widgets (Events by type, Cost by project)
 * missing, and the tiles squeezed into a portrait sheet with their headings cut. jsdom applies no stylesheet,
 * so only a real browser on a real dashboard can show it.
 *
 * The dashboard is made from the analytics template through the API in the sandbox workspace and deleted in
 * afterAll. PDFs go to DASHBOARD_PRINT_PDF=<dir> on request; the assertions are the check.
 */

const SANDBOX_WS_SLUG = "e2e-ci-sandbox";
const OUT = process.env.DASHBOARD_PRINT_PDF;
const RUN = `e2e-print-${Date.now().toString(36)}`;
/** The analytics template (internal/extensions/dashboards/templates.go). */
const WIDGETS = 11;
const BOTTOM_TITLES = ["Events by type", "Cost by project"];
const PAPER = { width: 1047, height: 718 };

test.describe.serial("Print dashboard: the whole Analytics layout on paper", () => {
  let page: Page;
  let api: APIRequestContext;
  let accessToken = "";
  let wsId = "";
  let dashboardId = "";
  const auth = () => ({ Authorization: `Bearer ${accessToken}` });

  test.beforeAll(async ({ browser }) => {
    test.setTimeout(120_000);
    const context = await browser.newContext();
    await pinUiLocale(context, "en");
    ({ accessToken } = await loginWithRetry(context.request, process.env.E2E_USER_EMAIL!, process.env.E2E_USER_PASSWORD!));
    api = rateLimitAware(context.request);
    page = await context.newPage();

    const wsRes = await api.get("/api/v1/workspaces", { headers: auth() });
    expect(wsRes.ok(), `GET /api/v1/workspaces failed: ${wsRes.status()}`).toBeTruthy();
    const ws = ((await wsRes.json()) as { id: string; slug: string }[]).find((w) => w.slug === SANDBOX_WS_SLUG);
    expect(ws, `the E2E credential must be a member of ${SANDBOX_WS_SLUG}`).toBeTruthy();
    wsId = ws!.id;

    const res = await api.post(`/api/v1/workspaces/${wsId}/dashboards`, {
      headers: auth(),
      data: { name: `[e2e] print ${RUN}`, placement: "bottom", template_id: "analytics", scope: "personal" },
    });
    expect(res.status(), `POST .../dashboards failed: ${await res.text()}`).toBeLessThan(300);
    const body = (await res.json()) as { id?: string; widgets?: unknown[] };
    dashboardId = body.id ?? "";
    expect(dashboardId, "created dashboard id").toBeTruthy();
    expect(body.widgets?.length, "the analytics template's widget count").toBe(WIDGETS);
  });

  test.afterAll(async () => {
    if (dashboardId) {
      const res = await api.delete(`/api/v1/workspaces/${wsId}/dashboards/${dashboardId}`, { headers: auth() });
      expect(res.ok(), `DELETE dashboard ${dashboardId} failed: ${res.status()}`).toBeTruthy();
    }
    await page?.context().close();
  });

  for (const [width, height] of [[1440, 900], [393, 852]] as const) {
    test(`${width}: every widget on paper, whole, with its heading readable`, async () => {
      await page.setViewportSize({ width, height });
      await page.emulateMedia({ media: "screen" });
      await page.goto(`/w/${SANDBOX_WS_SLUG}/d/${dashboardId}`, { waitUntil: "domcontentloaded" });
      const cells = page.locator("[data-widget-instance]");
      await expect(cells).toHaveCount(WIDGETS, { timeout: 60_000 });
      for (const t of BOTTOM_TITLES) await expect(page.getByRole("heading", { name: t, exact: true })).toBeAttached({ timeout: 30_000 });

      // Chrome lays paper out at the sheet's width whatever the window: the PDF is taken in the window's own state.
      // page.pdf() renders with the emulated media: left on "screen" it prints the screen CSS (one sheet whatever
      // the fix), so the print media goes on first.
      await page.emulateMedia({ media: "print" });
      const pdf = await page.pdf({ format: "A4", preferCSSPageSize: true, printBackground: true });
      if (OUT) {
        mkdirSync(OUT, { recursive: true });
        writeFileSync(`${OUT}/analytics-${width}.pdf`, pdf);
      }
      // The template is 18 rows: more than one landscape sheet. One sheet is the old viewport-only print.
      const pages = (pdf.toString("latin1").match(/\/Type\s*\/Page[^s]/g) ?? []).length;
      expect(pages, "the printed dashboard runs onto more than one sheet").toBeGreaterThan(1);

      // The print rules measured at the sheet's width: A4 landscape less 10 mm margins is 277 × 190 mm.
      await page.setViewportSize(PAPER);
      const paper = await page.evaluate(() => {
        const clips = (el: Element) => {
          const s = getComputedStyle(el);
          return s.overflowX !== "visible" || s.overflowY !== "visible";
        };
        const grid = document.querySelector("[data-print-grid]")!;
        const out = [...document.querySelectorAll<HTMLElement>("[data-widget-instance]")].map((cell) => {
          const r = cell.getBoundingClientRect();
          // A clipping ancestor of the grid (the old shell's scroller) cuts what lies outside its box.
          let cut = "";
          for (let a = grid.parentElement; a && a !== document.documentElement; a = a.parentElement) {
            if (!clips(a)) continue;
            const b = a.getBoundingClientRect();
            if (r.bottom > b.bottom + 1 || r.right > b.right + 1) cut = `${a.tagName.toLowerCase()}.${a.className}`;
          }
          const h = cell.querySelector("h3");
          return {
            title: h?.textContent ?? "",
            cut,
            headingCut: h ? h.scrollWidth > h.clientWidth + 1 : false,
            inDocument: r.bottom <= document.documentElement.scrollHeight + 1,
          };
        });
        return {
          cells: out,
          columns: getComputedStyle(grid).gridTemplateColumns.split(" ").length,
          sidebar: getComputedStyle(document.querySelector("[data-testid=app-sidebar]")!).display,
        };
      });
      expect(paper.sidebar, "the sidebar stays off paper").toBe("none");
      expect(paper.columns, "paper gets the 12-column desktop layout whatever the window").toBe(12);
      for (const c of paper.cells) {
        expect(c.cut, `"${c.title}" is cut by a clipping box`).toBe("");
        expect(c.inDocument, `"${c.title}" lies inside the printed document`).toBe(true);
        expect(c.headingCut, `"${c.title}" heading is not cut`).toBe(false);
      }
      for (const t of BOTTOM_TITLES) expect(paper.cells.map((c) => c.title)).toContain(t);
      await page.emulateMedia({ media: "screen" });
    });
  }
});

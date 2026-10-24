import { test, expect, type Page } from "@playwright/test";

// This suite writes only to scripts/local-stack.sh's disposable database.
// Regression for the page jumping to the top when "comment" is pressed on a
// selection deep in a long document, plus the scroll-to-mark behaviour of
// an internal ticket that the fix must leave alone.
const WORDS = "lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor";


// The document scrolls inside an overflow container, not the window.
declare global { interface Window { scroller: () => HTMLElement; scrollerTop: () => number } }
const SCROLLER = `
  window.scroller = () => {
    let el = document.querySelector(".mesh-doc-prose");
    while (el && el.parentElement) {
      el = el.parentElement;
      const o = getComputedStyle(el).overflowY;
      if ((o === "auto" || o === "scroll") && el.scrollHeight > el.clientHeight) return el;
    }
    return document.scrollingElement;
  };
  window.scrollerTop = () => window.window.scroller().scrollTop;
`;

for (const vp of [
  { name: "1440", width: 1440, height: 900 },
  { name: "393", width: 393, height: 852 },
]) {
  test(`doc comment on a selection in the bottom third keeps the page in place (${vp.name})`, async ({ page, context }) => {
    await page.addInitScript(SCROLLER);
    await page.setViewportSize({ width: vp.width, height: vp.height });
    const doc = await seedLongDoc(page, context);
    await page.goto(doc.url);
    const paras = page.locator(".mesh-doc-prose > p");
    await expect(paras).toHaveCount(60);

    // A paragraph in the bottom third, brought to the middle of the viewport.
    const target = paras.nth(48);
    await target.evaluate((el) => el.scrollIntoView({ block: "center" }));
    await page.waitForTimeout(300);
    const word = `w48x`;
    await target.getByText(word).first().dblclick();
    const affordance = page.getByTestId("doc-comment-affordance");
    await expect(affordance).toBeVisible();
    const before = await page.evaluate(() => window.scrollerTop());
    expect(before).toBeGreaterThan(1000);

    const minY = { v: before };
    await page.evaluate(() => {
      (window as unknown as { __minY: number }).__minY = window.scrollerTop();
      window.scroller().addEventListener("scroll", () => {
        const w = window as unknown as { __minY: number };
        w.__minY = Math.min(w.__minY, window.scrollerTop());
      });
    });
    await affordance.click();
    const textarea = page.locator("textarea:focus");
    await expect(textarea).toBeVisible();
    await page.waitForTimeout(800);

    minY.v = await page.evaluate(() => (window as unknown as { __minY: number }).__minY);
    const after = await page.evaluate(() => window.scrollerTop());
    // Beside the text (1440) the form opens level with the selection, so the
    // page must stay put. At 393 the draft sits under the document: the page
    // moves DOWN to it, never up to the top.
    expect(minY.v, "page scrolled up while the form opened").toBeGreaterThan(before - 150);
    expect(after).toBeGreaterThan(before - 150);
    if (vp.width >= 1024) await expectInViewport(page, target);
    await expectInViewport(page, textarea);
    await page.screenshot({ path: `/tmp/linus-2336-draft-${vp.name}.png` });
  });
}

test("clicking a comment card in the rail still scrolls the document to its mark", async ({ page, context }) => {
  await page.addInitScript(SCROLLER);
  await page.setViewportSize({ width: 1440, height: 900 });
  const doc = await seedLongDoc(page, context, { commentOn: 48 });
  await page.goto(doc.url);
  await expect(page.locator(".mesh-doc-prose > p")).toHaveCount(60);
  await page.evaluate(() => window.scroller().scrollTo(0, 0));
  const card = page.getByTestId("doc-comment-thread").first();
  await expect(card).toBeVisible();
  await card.locator("p, span").first().click();
  await expect.poll(() => page.evaluate(() => window.scrollerTop()), { timeout: 5000 }).toBeGreaterThan(1000);
});

async function expectInViewport(page: Page, loc: ReturnType<Page["locator"]>) {
  const box = await loc.boundingBox();
  expect(box, "element has no box").not.toBeNull();
  const vh = page.viewportSize()!.height;
  expect(box!.y).toBeGreaterThanOrEqual(0);
  expect(box!.y + box!.height).toBeLessThanOrEqual(vh);
}

async function seedLongDoc(page: Page, context: import("@playwright/test").BrowserContext, opts: { commentOn?: number } = {}) {
  const login = await context.request.post("/api/v1/auth/login", { data: {
    email: "local-stack@example.test", password: "LocalStack1", // pragma: allowlist secret
  } });
  expect(login.status()).toBe(200);
  const auth = { Authorization: `Bearer ${(await login.json()).tokens.access_token}` };
  const ws = await (await context.request.post("/api/v1/workspaces", { headers: auth, data: { name: `Scroll ${Date.now()}` } })).json();
  const project = await (await context.request.post(`/api/v1/workspaces/${ws.id}/projects`, { headers: auth, data: { name: "Scroll demo" } })).json();
  const body = Array.from({ length: 60 }, (_, i) => `Paragraph ${i} w${i}x ${WORDS} ${WORDS} ${WORDS} ${WORDS}.`).join("\n\n");
  const doc = await (await context.request.post(`/api/v1/projects/${project.id}/documents`, { headers: auth, data: { title: "Long doc", body } })).json();
  if (opts.commentOn !== undefined) {
    const i = opts.commentOn;
    const res = await context.request.post(`/api/v1/documents/${doc.id}/comments`, { headers: auth, data: {
      body: "anchored deep",
      anchor: { exact: `w${i}x`, prefix: `Paragraph ${i} `, suffix: ` ${WORDS.slice(0, 20)}` },
    } });
    expect(res.status(), await res.text()).toBe(201);
  }
  return { url: `/w/${ws.slug}/p/${project.slug}/docs/${doc.id}` };
}

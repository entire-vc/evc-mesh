#!/usr/bin/env node
/**
 * Theme runtime in a real browser (themes T5, an internal ticket), against the
 * BUILT app (web/dist) with a mocked API: two workspaces with two themes.
 *
 *  1. A views bundle (real ES module, hashed asset) imports `react` through
 *     the import map and receives the core's single copy (same useState).
 *  2. Switching A -> B -> A inside the app: every frame carries a theme's
 *     tokens, never mesh-default; the return to A is painted from the cache
 *     on the very first frame after the route change, before /theme answers.
 *  3. A reload on /w/b/...: the first frame is already B (pre-paint script).
 *  4. A bundle that never loads (404) keeps tokens, no crash, a logged issue.
 *  Set THEME_RUNTIME_DUMP=<dir> to save a frame strip of the switch.
 *
 *   cd web && pnpm exec vite build && cd .. && node scripts/assert-theme-runtime.mjs
 */
import { createServer } from "node:http";
import { createHash } from "node:crypto";
import { existsSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { launchRailGateChromium } from "./rail-visual-gate-chromium.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const WEB = path.join(HERE, "..", "web");
const DIST = path.join(WEB, "dist");
const fail = (m) => { console.error(`\n❌ ${m}\n`); process.exit(1); };
if (!existsSync(path.join(DIST, "index.html"))) fail(`No built app at ${DIST}; build first (a missing build is a failure).`);

const BG = { a: "300 60% 90%", b: "150 60% 85%" };
const BUNDLE_JS = `
import { useState } from "react";
import { jsx } from "react/jsx-runtime";
const shared = window.__MESH_SHARED__.react;
window.__bundleSawSharedReact = useState === shared.useState;
export default { views: { "dashboard.layout": { contract: "^1", view: () => jsx("div", { children: "themed" }) } } };
`;
const sha = (s) => createHash("sha256").update(s).digest("hex");
const BUNDLE_HASH = sha(BUNDLE_JS);
const MISSING_HASH = "0".repeat(64);
const themeFor = (slug) => ({
  theme_id: slug === "a" ? "theme-a" : "theme-b",
  source: "workspace",
  tokens_css: `:root{--background:${BG[slug]};}`,
  theme_css_url: "",
  views_url: `/api/v1/theme/assets/${slug === "b" ? BUNDLE_HASH : MISSING_HASH}.js`,
  settings: {},
  brand: { theme_color: "#fff", theme_color_dark: "#000" },
  hash: `h-${slug}`,
});
const ws = (slug) => ({
  id: `00000000-0000-0000-0000-00000000000${slug === "a" ? 1 : 2}`, name: slug, slug, owner_id: "u", settings: {},
  billing_plan_id: "", billing_customer_id: "", icon_url: null, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
});
let themeDelayMs = 0;
const MIME = { ".html": "text/html; charset=utf-8", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".json": "application/json" };
const json = (res, body, code = 200) => { res.writeHead(code, { "content-type": "application/json" }); res.end(JSON.stringify(body)); };

const server = createServer((req, res) => {
  const url = new URL(req.url, "http://x");
  const p = url.pathname;
  if (p === `/api/v1/theme/assets/${BUNDLE_HASH}.js`) { res.writeHead(200, { "content-type": "text/javascript" }); return res.end(BUNDLE_JS); }
  if (p.startsWith("/api/v1/theme/assets/")) return json(res, { error: "not found" }, 404);
  if (p === "/api/v1/theme") {
    const slug = url.searchParams.get("workspace");
    const body = themeFor(slug ?? "a");
    return setTimeout(() => json(res, body), slug ? themeDelayMs : 0);
  }
  if (p.endsWith("/auth/refresh")) return json(res, { tokens: { access_token: "t", refresh_token: "r", expires_in: 900 } });
  if (p.endsWith("/auth/me")) return json(res, { id: "u", email: "e@x.y", name: "E", avatar_url: "", is_active: true, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" });
  if (p === "/api/v1/workspaces") return json(res, [ws("a"), ws("b")]);
  if (p.startsWith("/api/")) return json(res, []);
  let file = path.join(DIST, decodeURIComponent(p));
  if (!file.startsWith(DIST) || !existsSync(file) || statSync(file).isDirectory()) file = path.join(DIST, "index.html");
  res.writeHead(200, { "content-type": MIME[path.extname(file)] ?? "application/octet-stream" });
  res.end(readFileSync(file));
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const BASE = `http://127.0.0.1:${server.address().port}`;

const browser = await launchRailGateChromium(WEB, fail);
let ok = true;
const check = (pass, what) => { ok &&= pass; console.log(`  ${pass ? "✅" : "❌"} ${what}`); };

const newPage = async () => {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, serviceWorkers: "block" });
  await ctx.route(/fonts\.(googleapis|gstatic)\.com/, (r) => r.abort());
  // Sample the --background token on every frame from the first one.
  await ctx.addInitScript(() => {
    window.__frames = [];
    const tick = () => {
      window.__frames.push(getComputedStyle(document.documentElement).getPropertyValue("--background").trim());
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });
  const page = await ctx.newPage();
  return { ctx, page };
};
const tokenNow = (page) => page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--background").trim());
// React's own node (the static shell's nodes carry no fiber); then wait for the signed-in layout.
const mounted = () => {
  const el = document.getElementById("root")?.firstElementChild;
  return !!el && Object.keys(el).some((k) => k.startsWith("__reactFiber")) && !!document.querySelector("aside, nav");
};
const spaNav = (page, to) => page.evaluate((t) => { history.pushState({}, "", t); dispatchEvent(new PopStateEvent("popstate")); }, to);

// the default value, to recognise a flash of it
const DEFAULT_BG = await (async () => {
  const { ctx, page } = await newPage();
  await page.goto(BASE + "/login");
  await page.evaluate(() => { document.getElementById("mesh-instance-theme")?.remove(); });
  const v = await tokenNow(page);
  await ctx.close();
  return v;
})();
if (!DEFAULT_BG || Object.values(BG).includes(DEFAULT_BG)) fail(`could not read the mesh-default --background (${JSON.stringify(DEFAULT_BG)})`);

console.log(`\n1+2. Switching workspaces A -> B -> A (default is "${DEFAULT_BG}")`);
{
  const { ctx, page } = await newPage();
  await page.goto(BASE + "/w/a/dashboard");
  await page.waitForFunction(mounted, null, { timeout: 15_000 });
  await page.waitForFunction((v) => getComputedStyle(document.documentElement).getPropertyValue("--background").trim() === v, BG.a, { timeout: 10_000 });
  themeDelayMs = 400;
  await page.evaluate(() => { window.__frames.length = 0; });
  await spaNav(page, "/w/b/dashboard");
  await page.waitForFunction((v) => getComputedStyle(document.documentElement).getPropertyValue("--background").trim() === v, BG.b, { timeout: 10_000 });
  await page.waitForFunction(() => window.__bundleSawSharedReact !== undefined, null, { timeout: 10_000 });
  check(await page.evaluate(() => window.__bundleSawSharedReact === true), `bundle's \`import "react"\` is the core's copy (one React)`);
  const toB = await page.evaluate(() => window.__frames.slice());
  check(toB.length > 0 && toB.every((v) => v === BG.a || v === BG.b), `A -> B (first visit, /theme delayed ${themeDelayMs} ms): ${toB.length} frames, none in mesh-default`);

  await page.evaluate(() => { window.__frames.length = 0; });
  await spaNav(page, "/w/a/dashboard");
  await page.waitForTimeout(700);
  const toA = await page.evaluate(() => window.__frames.slice());
  check(toA.length > 0 && toA.every((v) => v === BG.a || v === BG.b), `B -> A: ${toA.length} frames, none in mesh-default`);
  const firstA = toA.findIndex((v) => v === BG.a);
  check(firstA >= 0 && firstA <= 3, `B -> A: A's tokens from the cache on frame ${firstA}, before /theme (delayed ${themeDelayMs} ms) answered`);
  if (process.env.THEME_RUNTIME_DUMP) writeFileSync(path.join(process.env.THEME_RUNTIME_DUMP, "frames-b-to-a.json"), JSON.stringify(toA));
  await ctx.close();
}

console.log(`\n3. Reload inside workspace B: first frame already in B`);
{
  const { ctx, page } = await newPage();
  themeDelayMs = 0;
  await page.goto(BASE + "/w/b/dashboard");
  await page.waitForFunction(mounted, null, { timeout: 15_000 });
  await page.waitForTimeout(300);
  themeDelayMs = 1500; // the answer comes long after the first frames
  await page.reload();
  await page.waitForTimeout(400);
  const frames = await page.evaluate(() => window.__frames.slice());
  check(frames.length > 0 && frames.every((v) => v === BG.b), `reload with a slow /theme: ${frames.length} frames, all B (cache painted before React)${frames.every((v) => v === BG.b) ? "" : " got " + JSON.stringify([...new Set(frames)])}`);
  await ctx.close();
}

console.log(`\n4. A bundle that does not load keeps tokens, page stays alive, issue is logged`);
{
  const { ctx, page } = await newPage();
  themeDelayMs = 0;
  await page.goto(BASE + "/w/a/dashboard");
  await page.waitForFunction(mounted, null, { timeout: 15_000 });
  await page.waitForFunction(() => (window.__meshThemeIssues ?? []).some((i) => i.code === "bundle-load-failed"), null, { timeout: 15_000 }).catch(() => {});
  const issues = await page.evaluate(() => (window.__meshThemeIssues ?? []).map((i) => i.code));
  check(issues.includes("bundle-load-failed"), `bundle-load-failed logged (${issues.join(",") || "nothing"})`);
  check((await tokenNow(page)) === BG.a, `tokens of A still applied`);
  check(await page.evaluate(mounted), `app still mounted`);
  await ctx.close();
}

server.close();
await browser.close();
if (ok) console.log(`\n✅ theme runtime holds.\n`);
else console.error(`\n❌ theme runtime check failed (see ❌ above).\n`);
process.exit(ok ? 0 : 1);

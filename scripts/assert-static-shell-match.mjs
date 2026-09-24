#!/usr/bin/env node
/**
 * "Static = live" gate for the index.html shell (perf·Б2, `#2417383b`).
 *
 * WHY THIS EXISTS
 * web/index.html paints a hand-written copy of React's first frame into
 * #root so the screen appears at TTFB + CSS instead of after the whole
 * bundle. That copy is duplicated markup: the day someone changes a class in
 * pages/login.tsx or the auth-loading skeleton in app-layout.tsx, the shell
 * silently drifts, and every page load starts with a jump from the stale
 * copy to the real thing. This gate is what keeps the two from drifting.
 *
 * WHAT IT CHECKS (against the BUILT app, web/dist)
 *  1. Pixels: for /login and an app route, × 1440/393 × light/dark, a
 *     screenshot of the shell alone (bundle JS blocked) and of the mounted
 *     React screen must differ in ≤ MAX_DIFF_PX pixels.
 *  2. Hand-off: text typed into the shell form while the bundle is still
 *     loading ends up in the React form, and focus stays in the same field.
 *     Losing a typed password on mount is the failure this rules out.
 *  3. Layout shift across the swap, both routes: CLS ≤ 0.01.
 *  4. Kill switch: ?static-shell=off paints no shell, ?static-shell=on
 *     brings it back.
 *
 * The local server holds every /api request open, so React stays on its
 * first frame (auth still loading, registration link at its initial
 * value) — the frame the shell imitates. Google Fonts are blocked in both
 * captures so the comparison does not depend on a CDN.
 *
 * NEGATIVE CONTROL
 * `--selftest` re-captures each shell with a 4px spacing change injected
 * and requires every such case to come out RED.
 *
 * USAGE
 *   cd web && pnpm build && cd .. && node scripts/assert-static-shell-match.mjs [--selftest]
 * External Chromium: RAIL_CONTRAST_CHROMIUM=/path/to/chrome
 * Save each shell/live screenshot pair: STATIC_SHELL_DUMP=/some/dir
 */

import { createServer } from "node:http";
import { existsSync, readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { launchRailGateChromium } from "./rail-visual-gate-chromium.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const WEB = path.join(HERE, "..", "web");
const DIST = path.join(WEB, "dist");

// Pixels allowed to differ — an ABSOLUTE count, not a share of the screen.
// Measured 24.09: text anti-aliasing noise between shell and React is ≤ 35 px;
// the smallest real drift (the app skeleton moved 4px) is 656 px. As a share,
// that drift is 0.05% of a 1440 screen and slipped under a 0.1% threshold —
// small shells need an absolute bound.
const MAX_DIFF_PX = 150;
// Per-channel delta below which two pixels count as equal. Kept low on
// purpose: bg-muted on bg-background differs by only a few levels, and at a
// tolerance of 24 the skeleton was invisible to this gate (0 px either way).
const CHANNEL_TOL = 4;
const MAX_CLS = 0.01;

const ROUTES = [
  { name: "login", path: "/login" },
  { name: "app", path: "/w/gate/activity" },
];
const VIEWPORTS = [
  { name: "1440", width: 1440, height: 900 },
  { name: "393", width: 393, height: 852 },
];
const THEMES = ["light", "dark"];

function fail(msg) {
  console.error(`\n❌ ${msg}\n`);
  process.exit(1);
}

if (!existsSync(path.join(DIST, "index.html"))) {
  fail(
    `No built app at ${DIST}\nBuild it first:  cd web && pnpm build\n` +
      `(A missing build is a FAILURE here, never a skip.)`,
  );
}

// ── local server: dist + SPA fallback, /api held open ────────────────────
const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript",
  ".css": "text/css",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".json": "application/json",
};
const held = new Set();
const server = createServer((req, res) => {
  const url = new URL(req.url, "http://x");
  if (url.pathname.startsWith("/api/")) {
    held.add(res);
    res.on("close", () => held.delete(res));
    return; // never answered — React stays on its first frame
  }
  let file = path.join(DIST, decodeURIComponent(url.pathname));
  if (!file.startsWith(DIST) || !existsSync(file) || statSync(file).isDirectory()) {
    file = path.join(DIST, "index.html");
  }
  res.writeHead(200, {
    "content-type": MIME[path.extname(file)] ?? "application/octet-stream",
  });
  res.end(readFileSync(file));
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const BASE = `http://127.0.0.1:${server.address().port}`;

// ── helpers ──────────────────────────────────────────────────────────────
const isBundleJs = (u) => /\/assets\/[^/]+\.js(\?|$)/.test(u);

async function newContext(browser, vp, theme) {
  const ctx = await browser.newContext({
    viewport: { width: vp.width, height: vp.height },
    colorScheme: theme,
    serviceWorkers: "block",
  });
  await ctx.addInitScript((t) => {
    try {
      localStorage.setItem("theme", t);
    } catch {}
  }, theme);
  await ctx.route(/fonts\.(googleapis|gstatic)\.com/, (r) => r.abort());
  return ctx;
}

const reactMounted = () => {
  const el = document.getElementById("root")?.firstElementChild;
  return !!el && Object.keys(el).some((k) => k.startsWith("__reactFiber"));
};

async function settle(page) {
  await page.evaluate(
    () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
  );
}

async function shot(page) {
  return page.screenshot({ animations: "disabled", caret: "hide" });
}

async function captureShell(browser, route, vp, theme, perturb = false) {
  const ctx = await newContext(browser, vp, theme);
  await ctx.route((u) => isBundleJs(u.href), (r) => r.abort());
  const page = await ctx.newPage();
  await page.goto(BASE + route.path, { waitUntil: "load" });
  const shell = await page.evaluate(
    () => document.getElementById("root").getAttribute("data-static-shell"),
  );
  if (perturb) {
    // Negative control: 4px of extra padding inside the shell. Padding, not
    // margin: a margin here collapses into the space-y-* margin of the
    // sibling above and moves nothing — the first version of this control
    // did exactly that and "passed" a perturbation that never happened.
    const moved = await page.evaluate(() => {
      const root = document.getElementById("root");
      const target =
        root.querySelector("form > .space-y-4") ?? // login: form fields block
        root.querySelector(".space-y-4.text-center"); // app: skeleton stack
      const probe = target.lastElementChild;
      const before = probe.getBoundingClientRect().top;
      target.style.paddingTop = `${parseFloat(getComputedStyle(target).paddingTop) + 4}px`;
      return probe.getBoundingClientRect().top - before;
    });
    if (Math.abs(moved) < 1) {
      fail(`negative control did not move anything on ${route.path} — it proves nothing`);
    }
  }
  await settle(page);
  const png = await shot(page);
  await ctx.close();
  return { png, shell };
}

async function captureLive(browser, route, vp, theme) {
  const ctx = await newContext(browser, vp, theme);
  const page = await ctx.newPage();
  await page.goto(BASE + route.path, { waitUntil: "load" });
  await page.waitForFunction(reactMounted, null, { timeout: 15_000 });
  await settle(page);
  const png = await shot(page);
  await ctx.close();
  return png;
}

let diffPage;
async function pixelDiff(a, b) {
  return diffPage.evaluate(
    async ([a64, b64, tol]) => {
      const load = (s) =>
        new Promise((res, rej) => {
          const i = new Image();
          i.onload = () => res(i);
          i.onerror = rej;
          i.src = "data:image/png;base64," + s;
        });
      const [ia, ib] = await Promise.all([load(a64), load(b64)]);
      if (ia.width !== ib.width || ia.height !== ib.height) {
        return { ratio: 1, diff: -1, total: 0, size: "mismatch" };
      }
      const px = (img) => {
        const c = document.createElement("canvas");
        c.width = img.width;
        c.height = img.height;
        const g = c.getContext("2d");
        g.drawImage(img, 0, 0);
        return g.getImageData(0, 0, img.width, img.height).data;
      };
      const da = px(ia);
      const db = px(ib);
      let diff = 0;
      for (let i = 0; i < da.length; i += 4) {
        if (
          Math.abs(da[i] - db[i]) > tol ||
          Math.abs(da[i + 1] - db[i + 1]) > tol ||
          Math.abs(da[i + 2] - db[i + 2]) > tol
        )
          diff++;
      }
      const total = da.length / 4;
      return { ratio: diff / total, diff, total };
    },
    [a.toString("base64"), b.toString("base64"), CHANNEL_TOL],
  );
}

// ── checks ───────────────────────────────────────────────────────────────
const selftest = process.argv.includes("--selftest");
const browser = await launchRailGateChromium(WEB, fail);
diffPage = await browser.newPage();
let ok = true;
const mark = (pass) => (pass ? "✅" : "❌");

console.log(`\n1. Static shell vs mounted React (≤ ${MAX_DIFF_PX} px differ)`);
const selftestResults = [];
for (const route of ROUTES) {
  for (const vp of VIEWPORTS) {
    for (const theme of THEMES) {
      const label = `${route.name} ${vp.name} ${theme}`;
      const { png: shellPng, shell } = await captureShell(browser, route, vp, theme);
      if (!shell) {
        console.log(`  ❌ ${label}: index.html painted no shell (kill switch on, or the inline script broke)`);
        ok = false;
        continue;
      }
      const livePng = await captureLive(browser, route, vp, theme);
      const d = await pixelDiff(shellPng, livePng);
      const pass = d.diff >= 0 && d.diff <= MAX_DIFF_PX;
      if (process.env.STATIC_SHELL_DUMP) {
        const { writeFileSync } = await import("node:fs");
        const base = path.join(process.env.STATIC_SHELL_DUMP, label.replaceAll(" ", "-"));
        writeFileSync(`${base}-shell.png`, shellPng);
        writeFileSync(`${base}-live.png`, livePng);
      }
      ok &&= pass;
      console.log(
        `  ${mark(pass)} ${label}: ${d.diff}/${d.total} px differ (${(d.ratio * 100).toFixed(3)}%)`,
      );
      if (selftest) {
        const { png: bent } = await captureShell(browser, route, vp, theme, true);
        const dn = await pixelDiff(bent, livePng);
        selftestResults.push({ label, dn });
      }
    }
  }
}

console.log(`\n2. Input typed into the shell survives the hand-off to React`);
{
  const ctx = await newContext(browser, VIEWPORTS[0], "light");
  let release;
  const gate = new Promise((r) => (release = r));
  await ctx.route(
    (u) => isBundleJs(u.href),
    async (r) => {
      await gate;
      await r.continue();
    },
  );
  const page = await ctx.newPage();
  // "commit", not "domcontentloaded": DOMContentLoaded waits for the module
  // script, which is exactly what is being held back here.
  await page.goto(BASE + "/login", { waitUntil: "commit" });
  await page.waitForSelector("#email");
  await page.fill("#email", "shell@example.com");
  await page.fill("#password", "typed-before-js");
  await page.focus("#password");
  const beforeMount = await page.evaluate(reactMounted);
  release();
  await page.waitForFunction(reactMounted, null, { timeout: 15_000 });
  await settle(page);
  const after = await page.evaluate(() => ({
    email: document.querySelector("#email").value,
    password: document.querySelector("#password").value,
    focused: document.activeElement?.id ?? null,
    fromReact: Object.keys(document.querySelector("#password")).some((k) =>
      k.startsWith("__reactFiber"),
    ),
  }));
  const checks = [
    [beforeMount === false, `typed while React was NOT mounted yet`],
    [after.fromReact, `the field read back is React's, not the shell's`],
    [after.email === "shell@example.com", `email carried over (${JSON.stringify(after.email)})`],
    [after.password === "typed-before-js", `password carried over (${after.password ? "non-empty" : "EMPTY"})`],
    [after.focused === "password", `focus kept in the password field (${after.focused})`],
  ];
  for (const [pass, what] of checks) {
    ok &&= pass;
    console.log(`  ${mark(pass)} ${what}`);
  }
  await ctx.close();
}

console.log(`\n3. Layout shift across the swap (CLS ≤ ${MAX_CLS})`);
for (const [route, vp] of ROUTES.flatMap((r) => VIEWPORTS.map((v) => [r, v]))) {
  const ctx = await newContext(browser, vp, "light");
  await ctx.addInitScript(() => {
    window.__cls = 0;
    new PerformanceObserver((l) => {
      for (const e of l.getEntries()) if (!e.hadRecentInput) window.__cls += e.value;
    }).observe({ type: "layout-shift", buffered: true });
  });
  const page = await ctx.newPage();
  await page.goto(BASE + route.path, { waitUntil: "load" });
  await page.waitForFunction(reactMounted, null, { timeout: 15_000 });
  await page.waitForTimeout(500);
  const cls = await page.evaluate(() => window.__cls);
  const pass = cls <= MAX_CLS;
  ok &&= pass;
  console.log(`  ${mark(pass)} ${route.name} ${vp.name}: CLS ${cls.toFixed(4)}`);
  await ctx.close();
}

console.log(`\n4. Kill switch`);
{
  const ctx = await newContext(browser, VIEWPORTS[0], "light");
  await ctx.route((u) => isBundleJs(u.href), (r) => r.abort());
  const page = await ctx.newPage();
  const count = () => page.evaluate(() => document.getElementById("root").childElementCount);
  await page.goto(BASE + "/login?static-shell=off", { waitUntil: "load" });
  const off = await count();
  await page.goto(BASE + "/login", { waitUntil: "load" });
  const stillOff = await count();
  await page.goto(BASE + "/login?static-shell=on", { waitUntil: "load" });
  const on = await count();
  const checks = [
    [off === 0, `?static-shell=off paints nothing (${off} nodes)`],
    [stillOff === 0, `…and stays off on the next load (${stillOff} nodes)`],
    [on > 0, `?static-shell=on brings it back (${on} nodes)`],
  ];
  for (const [pass, what] of checks) {
    ok &&= pass;
    console.log(`  ${mark(pass)} ${what}`);
  }
  await ctx.close();
}

if (selftest) {
  console.log(`\nNEGATIVE CONTROL: shell with a 4px spacing change must be rejected`);
  let allRed = selftestResults.length > 0;
  for (const { label, dn } of selftestResults) {
    const red = dn.diff < 0 || dn.diff > MAX_DIFF_PX;
    allRed &&= red;
    console.log(
      `  ${red ? "✅ rejected" : "❌ ACCEPTED"} ${label}: ${dn.diff} px differ (${(dn.ratio * 100).toFixed(3)}%)`,
    );
  }
  if (!allRed) {
    console.error(`\n❌ negative control passed somewhere — this gate cannot see that drift.`);
    ok = false;
  }
}

for (const res of held) res.destroy();
server.close();
await browser.close();
if (ok) console.log(`\n✅ static shell matches the live first frame.\n`);
else console.error(`\n❌ static shell drifted from the live app (see ❌ above).\n`);
process.exit(ok ? 0 : 1);

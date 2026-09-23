#!/usr/bin/env node
/**
 * Geometry guard for the collapsed rail's header strip (`#382ff590`).
 *
 * WHY THIS EXISTS
 * The two older rail gates (`assert-rail-icon-contrast.mjs`,
 * `assert-rail-icon-size.mjs`) render a single tile in isolation. That is the
 * right fixture for what they check, and exactly the wrong one for this
 * defect: the workspace mark's tile was correct in both branches, and the
 * breakage came from the COLUMN around it. With a project list taller than
 * the window, the rail's nav (a plain flex child) took its missing height
 * from the header strip, which shrank from 56px to 33px — the mark plus its
 * border — and the mark sat glued to the top edge. Live on prod, 23.09:
 * header 47x33, mark at y=0, nav 1050px in a 900px column.
 *
 * It was reported as "fixed the no-logo branch, broke the logo branch"
 * because the one workspace with an uploaded logo also had 14 projects. So
 * this guard checks BOTH branches, with a list long enough to overflow, and
 * requires them to produce identical container geometry — the check whose
 * absence let one fix look like it broke the other branch.
 *
 * WHAT IT MEASURES
 * The real shell classes (`RAIL_COLLAPSED_ASIDE/HEADER/NAV`) and the real
 * merged tile classes, imported from `rail-icon-classes.ts`, over the real
 * compiled stylesheet, in a 48px column 600px tall holding 20 project tiles:
 *   - header height is its declared 56px, not whatever is left over;
 *   - the mark is centred in the header on both axes (y = (56-32)/2);
 *   - the mark is no larger than a project tile, and shares its centre X;
 *   - loaded-image and fallback branches give the same container box;
 *   - the nav stays inside the column (it scrolls instead of overflowing).
 *
 * NEGATIVE CONTROL
 * `--selftest` additionally renders the pre-fix shell (frozen literals below)
 * and requires it to come out RED. The pre-fix classes are all still used
 * elsewhere in the app, so they are present in the compiled CSS — the
 * control fails because of the layout, not because a rule is missing.
 *
 * USAGE
 *   node scripts/assert-rail-header-geometry.mjs [--selftest]
 * Requires the built frontend (cd web && pnpm build). External Chromium:
 *   RAIL_CONTRAST_CHROMIUM=/path/to/chrome node scripts/assert-rail-header-geometry.mjs
 */

import { readFileSync, readdirSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { launchRailGateChromium } from "./rail-visual-gate-chromium.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, "..");
const WEB = path.join(REPO, "web");

const { cn } = await import(path.join(WEB, "src/lib/cn.ts"));
const recipes = await import(
  path.join(WEB, "src/components/layout/rail-icon-classes.ts")
);

/** Shell as it shipped before `#382ff590` — negative-control input only. */
const PRE_FIX_SHELL = {
  aside:
    "flex h-full w-12 flex-col items-center border-r border-sidebar-border bg-sidebar",
  header:
    "flex h-14 w-full items-center justify-center border-b border-sidebar-border",
  nav: "flex flex-col items-center gap-2 py-3",
};

const VIEWPORT = { width: 1280, height: 600 };
const PROJECTS = 20;
const HEADER_PX = 56; // h-14
const TOL = 0.5;

function fail(msg) {
  console.error(`\n❌ ${msg}\n`);
  process.exit(1);
}

function findBuiltCss() {
  const assets = path.join(WEB, "dist", "assets");
  if (!existsSync(assets)) {
    fail(
      `No built stylesheet at ${assets}\nBuild it first:  cd web && pnpm build\n` +
        `(A missing baseline is a FAILURE here, never a skip.)`,
    );
  }
  const css = readdirSync(assets).filter((f) => f.endsWith(".css"));
  if (css.length === 0) fail(`No .css in ${assets} — rebuild the frontend.`);
  const chosen = css
    .map((f) => ({ f, size: readFileSync(path.join(assets, f)).length }))
    .sort((a, b) => b.size - a.size)[0];
  return readFileSync(path.join(assets, chosen.f), "utf8");
}

// A square image with an opaque fill, so the loaded branch has real pixels.
const LOGO_SRC =
  "data:image/svg+xml;utf8," +
  encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="200"><rect width="200" height="200" fill="#123"/></svg>`,
  );

function markup(shell, branch) {
  const isLoaded = branch === "loaded";
  const tile = cn(
    recipes.workspaceLogoContainerParts({ variant: "collapsed", isLoaded }),
  );
  const inner = isLoaded
    ? `<img id="img" src="${LOGO_SRC}" class="h-full w-full object-cover">`
    : `<svg class="${recipes.WORKSPACE_LOGO_ICON_FILL}" viewBox="0 0 24 24"><rect width="24" height="24" fill="currentColor"/></svg>`;
  const project = cn(recipes.projectRailIconParts(false));
  const projects = Array.from(
    { length: PROJECTS },
    (_, i) =>
      `<a href="#" data-p class="${project}"><span class="${recipes.PROJECT_RAIL_ICON_LABEL}">${String.fromCharCode(65 + i)}</span></a>`,
  ).join("");
  return (
    `<!doctype html><html><head><style>${CSS}</style></head>` +
    `<body style="margin:0;height:100vh"><div style="height:100vh;width:48px">` +
    `<aside id="aside" class="${shell.aside}">` +
    `<div id="header" class="${shell.header}"><div id="logo" class="${tile}">${inner}</div></div>` +
    `<nav id="nav" class="${shell.nav}">${projects}</nav>` +
    `</aside></div></body></html>`
  );
}

async function measure(page, shell, branch) {
  await page.setContent(markup(shell, branch), { waitUntil: "load" });
  return page.evaluate(() => {
    const A = document.getElementById("aside").getBoundingClientRect();
    const r = (el) => {
      const b = el.getBoundingClientRect();
      return { x: b.left - A.left, y: b.top - A.top, w: b.width, h: b.height };
    };
    const img = document.getElementById("img");
    return {
      aside: { w: A.width, h: A.height },
      header: r(document.getElementById("header")),
      logo: r(document.getElementById("logo")),
      img: img ? r(img) : null,
      imgLoaded: img ? img.complete && img.naturalWidth > 0 : null,
      nav: r(document.getElementById("nav")),
      project: r(document.querySelector("[data-p]")),
    };
  });
}

const r2 = (n) => Math.round(n * 100) / 100;
const box = (b) => `${r2(b.w)}x${r2(b.h)} @ (${r2(b.x)}, ${r2(b.y)})`;

function check(m, label) {
  const checks = [];
  const add = (ok, what) => checks.push({ ok, what });
  add(Math.abs(m.header.h - HEADER_PX) <= TOL, `header height ${r2(m.header.h)} = ${HEADER_PX}`);
  const wantY = m.header.y + (m.header.h - m.logo.h) / 2;
  add(Math.abs(m.logo.y - wantY) <= TOL, `logo centred vertically in header (y ${r2(m.logo.y)}, want ${r2(wantY)})`);
  add(m.logo.y >= 10, `logo top inset ${r2(m.logo.y)} >= 10 (not glued to the edge)`);
  add(m.logo.w <= m.project.w + TOL && m.logo.h <= m.project.h + TOL,
    `logo ${r2(m.logo.w)}x${r2(m.logo.h)} <= project tile ${r2(m.project.w)}x${r2(m.project.h)}`);
  const cxL = m.logo.x + m.logo.w / 2;
  const cxP = m.project.x + m.project.w / 2;
  add(Math.abs(cxL - cxP) <= TOL, `logo cx ${r2(cxL)} = project cx ${r2(cxP)}`);
  add(m.nav.y + m.nav.h <= m.aside.h + TOL, `nav bottom ${r2(m.nav.y + m.nav.h)} inside column ${r2(m.aside.h)}`);
  if (m.img) {
    add(m.imgLoaded === true, `image actually loaded`);
    add(Math.abs(m.img.w - m.logo.w) <= TOL && Math.abs(m.img.h - m.logo.h) <= TOL,
      `img fills its container (${r2(m.img.w)}x${r2(m.img.h)})`);
  }
  console.log(`\n${label}`);
  console.log(`  header ${box(m.header)}   logo ${box(m.logo)}   project ${box(m.project)}`);
  for (const c of checks) console.log(`  ${c.ok ? "✅" : "❌"} ${c.what}`);
  return checks.every((c) => c.ok);
}

async function suite(page, shell, label) {
  const loaded = await measure(page, shell, "loaded");
  const fallback = await measure(page, shell, "fallback");
  let ok = check(loaded, `${label} — branch LOADED (image)`);
  ok = check(fallback, `${label} — branch FALLBACK (no image)`) && ok;
  const same = ["x", "y", "w", "h"].every((k) => Math.abs(loaded.logo[k] - fallback.logo[k]) <= TOL);
  console.log(`  ${same ? "✅" : "❌"} container identical in both branches (${box(loaded.logo)} vs ${box(fallback.logo)})`);
  return ok && same;
}

const CSS = findBuiltCss();
const selftest = process.argv.includes("--selftest");
const browser = await launchRailGateChromium(WEB, fail);
const page = await browser.newPage({ viewport: VIEWPORT });

let exitCode = 0;
const current = await suite(
  page,
  {
    aside: recipes.RAIL_COLLAPSED_ASIDE,
    header: recipes.RAIL_COLLAPSED_HEADER,
    nav: recipes.RAIL_COLLAPSED_NAV,
  },
  `CURRENT shell (${PROJECTS} projects, ${VIEWPORT.height}px tall)`,
);
if (!current) exitCode = 1;

if (selftest) {
  const broken = await suite(page, PRE_FIX_SHELL, "NEGATIVE CONTROL (pre-fix shell)");
  if (broken) {
    console.error(`\n❌ negative control PASSED — the pre-fix shell must be rejected; this guard cannot fail.\n`);
    exitCode = 1;
  } else {
    console.log(`\n✅ negative control correctly REJECTED the pre-fix shell.`);
  }
}

await browser.close();
if (exitCode === 0) console.log(`\n✅ collapsed-rail header geometry holds in both logo branches.\n`);
else console.error(`\n❌ collapsed-rail header geometry broken.\n`);
process.exit(exitCode);

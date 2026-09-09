#!/usr/bin/env node
/**
 * Contrast guard for the collapsed-rail icon tiles (`#bb8f1092`).
 *
 * WHY THIS EXISTS
 * The first fix for `#bb8f1092` gave every project tile a container of
 * provably correct geometry — 32x32, radius 8, identical to the workspace
 * header tile — and was accepted on that measurement. The letter inside four
 * of the six tiles was invisible. Geometry was measured because geometry is
 * easy to measure; the question the card actually asked ("can you see the
 * letter?") was never put to the code. This script asks that question.
 *
 * WHAT IT MEASURES
 * The WCAG 2.1 contrast ratio between the letter's computed `color` and the
 * computed background it is actually drawn on (resolved by walking up to the
 * first ancestor with a non-transparent background), in a real browser, over
 * the real compiled stylesheet. Not class names, not tokens, not intent:
 * the two colours the user's eye receives.
 *
 * WHY IT IMPORTS THE COMPONENT'S OWN RECIPE
 * The class parts come from `web/src/components/layout/rail-icon-classes.ts`
 * and are merged with the app's own `cn`. A guard that restated the class
 * strings would be testing a copy, and would keep passing after the component
 * changed. The interesting failure — tailwind-merge dropping an earlier colour
 * for a later one — only appears if the real merge runs on the real parts.
 *
 * NEGATIVE CONTROL
 * `--selftest` additionally feeds the pre-fix recipe through the same path and
 * requires it to come out RED. A contrast checker that cannot fail is not a
 * contrast checker, and "it passed" means nothing until you have watched it
 * fail on input known to be bad. This keeps that demonstration runnable
 * forever instead of being a screenshot in a comment.
 *
 * USAGE
 *   node scripts/assert-rail-icon-contrast.mjs [--selftest]
 * Requires the frontend to have been built (web/dist/assets/*.css):
 *   cd web && pnpm build
 * If Playwright's bundled browser is not installed, point at any Chromium:
 *   RAIL_CONTRAST_CHROMIUM=/path/to/chrome node scripts/assert-rail-icon-contrast.mjs
 */

import { readFileSync, readdirSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { launchRailGateChromium } from "./rail-visual-gate-chromium.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, "..");
const WEB = path.join(REPO, "web");

// WCAG AA for normal-size text. The label is 12px/500 (`text-xs font-medium`),
// which is not "large text" (>=18.66px bold or >=24px), so 3:1 does not apply.
const MIN_RATIO = 4.5;

const { cn } = await import(path.join(WEB, "src/lib/cn.ts"));
const recipes = await import(
  path.join(WEB, "src/components/layout/rail-icon-classes.ts")
);

/**
 * The recipe as it shipped in 24a54176 — the one that produced filled boxes
 * with unreadable letters. Kept ONLY as the negative control's input. It is
 * deliberately a literal: this is the one place a frozen copy is correct,
 * because it must not follow the component when the component is fixed.
 */
function brokenProjectRailIconParts(isActive) {
  return [
    "flex shrink-0 items-center justify-center overflow-hidden text-primary-foreground",
    "h-8 w-8 rounded-lg",
    "bg-sidebar-primary",
    [
      "text-sidebar-foreground hover:bg-sidebar-accent",
      isActive ? "bg-sidebar-accent text-sidebar-primary" : "",
    ]
      .filter(Boolean)
      .join(" "),
  ];
}

function findBuiltCss() {
  const assets = path.join(WEB, "dist", "assets");
  if (!existsSync(assets)) {
    fail(
      `No built stylesheet at ${assets}\n` +
        `This check compares colours the browser actually computes, so it needs the\n` +
        `compiled CSS. Build it first:  cd web && pnpm build\n` +
        `(A missing baseline is a FAILURE here, never a skip — a check that can\n` +
        ` silently not-check is the same false green it exists to prevent.)`,
    );
  }
  const css = readdirSync(assets).filter((f) => f.endsWith(".css"));
  if (css.length === 0) fail(`No .css in ${assets} — rebuild the frontend.`);
  // Largest = the app bundle rather than any chunk-local sheet.
  const chosen = css
    .map((f) => ({ f, size: readFileSync(path.join(assets, f)).length }))
    .sort((a, b) => b.size - a.size)[0];
  return {
    path: path.join(assets, chosen.f),
    text: readFileSync(path.join(assets, chosen.f), "utf8"),
  };
}

function fail(msg) {
  console.error(`\n❌ ${msg}\n`);
  process.exit(1);
}

// --- WCAG 2.1 relative luminance + contrast ratio -------------------------
function srgbToLinear(c) {
  const s = c / 255;
  return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
}
function luminance([r, g, b]) {
  return (
    0.2126 * srgbToLinear(r) + 0.7152 * srgbToLinear(g) + 0.0722 * srgbToLinear(b)
  );
}
function contrastRatio(fg, bg) {
  const l1 = luminance(fg);
  const l2 = luminance(bg);
  const [hi, lo] = l1 >= l2 ? [l1, l2] : [l2, l1];
  return (hi + 0.05) / (lo + 0.05);
}
function parseRgb(str) {
  const m = str.match(/rgba?\(([^)]+)\)/);
  if (!m) throw new Error(`cannot parse colour: ${str}`);
  const parts = m[1].split(/[\s,/]+/).filter(Boolean).map(Number);
  return [parts[0], parts[1], parts[2]];
}

async function measure(page, css, containerClass, { dark, hover }) {
  await page.setContent(
    `<!doctype html><html class="${dark ? "dark" : ""}"><head><style>${css}</style></head>` +
      `<body style="margin:0">` +
      // Reproduce the real ancestry: the rail sits on the sidebar surface, so
      // a transparent tile must resolve its background to that, not to white.
      `<div class="bg-sidebar" style="padding:24px;width:64px">` +
      `<a href="#" class="flex items-center justify-center" id="link">` +
      `<div id="tile" class="${containerClass}">` +
      `<span id="label" class="${recipes.PROJECT_RAIL_ICON_LABEL}">T</span>` +
      `</div></a></div></body></html>`,
    { waitUntil: "load" },
  );
  // The pointer's position SURVIVES setContent. Without this reset, every case
  // after the first hover case is measured while still hovered — which silently
  // reported the hover background as the resting one and hid a real failure
  // (dark/resting came back as the accent fill instead of the mint one). Park
  // the pointer outside the tile before every measurement, hover only on
  // purpose.
  await page.mouse.move(0, 0);
  if (hover) {
    await page.locator("#tile").hover();
    // Hover styles are not transitioned here, but give the engine a frame.
    await page.waitForTimeout(50);
  } else {
    // Prove the reset landed: a resting case must not be reading hover styles.
    await page.waitForTimeout(20);
  }
  return page.evaluate(() => {
    const label = document.getElementById("label");
    const color = getComputedStyle(label).color;
    let node = label;
    let bg = "rgba(0, 0, 0, 0)";
    while (node) {
      const c = getComputedStyle(node).backgroundColor;
      if (c && !/rgba\(0,\s*0,\s*0,\s*0\)|transparent/.test(c)) {
        bg = c;
        break;
      }
      node = node.parentElement;
    }
    const tile = document.getElementById("tile");
    const ts = getComputedStyle(tile);
    return {
      color,
      bg,
      width: ts.width,
      height: ts.height,
      radius: ts.borderRadius,
      bgOwner: node === tile ? "tile" : node ? node.className || node.tagName : "none",
    };
  });
}

const CASES = [
  { name: "light / resting", dark: false, hover: false, active: false },
  { name: "light / hover", dark: false, hover: true, active: false },
  { name: "light / active", dark: false, hover: false, active: true },
  { name: "dark  / resting", dark: true, hover: false, active: false },
  { name: "dark  / hover", dark: true, hover: true, active: false },
  { name: "dark  / active", dark: true, hover: false, active: true },
];

async function runSuite(page, css, partsFor, label) {
  const rows = [];
  for (const c of CASES) {
    const klass = cn(partsFor(c.active));
    const m = await measure(page, css, klass, { dark: c.dark, hover: c.hover });
    const ratio = contrastRatio(parseRgb(m.color), parseRgb(m.bg));
    rows.push({ ...c, ...m, ratio });
  }
  console.log(`\n  ${label}`);
  console.log(
    `  ${"state".padEnd(16)}${"letter".padEnd(20)}${"on background".padEnd(22)}${"ratio".padEnd(9)}verdict`,
  );
  for (const r of rows) {
    const ok = r.ratio >= MIN_RATIO;
    console.log(
      `  ${r.name.padEnd(16)}${r.color.padEnd(20)}${r.bg.padEnd(22)}` +
        `${(r.ratio.toFixed(2) + ":1").padEnd(9)}${ok ? "✓ pass" : "✗ FAIL"}`,
    );
  }
  return rows;
}

// -------------------------------------------------------------------------
const selftest = process.argv.includes("--selftest");
const css = findBuiltCss();
console.log(`\n── rail icon contrast (WCAG AA, min ${MIN_RATIO}:1) ──`);
console.log(`  stylesheet: ${path.relative(REPO, css.path)} (${css.text.length} bytes)`);

const browser = await launchRailGateChromium(WEB, fail);
const page = await browser.newPage({ viewport: { width: 400, height: 300 } });

let exitCode = 0;

const live = await runSuite(page, css.text, recipes.projectRailIconParts, "live recipe (rail-icon-classes.ts)");
const liveFails = live.filter((r) => r.ratio < MIN_RATIO);

if (selftest) {
  const broken = await runSuite(
    page,
    css.text,
    brokenProjectRailIconParts,
    "negative control: the pre-fix recipe from 24a54176 (MUST be red)",
  );
  // Be precise about WHICH states the bug produced, because an over-broad
  // control is itself a false assertion. Two of the six were never broken and
  // must not be demanded red:
  //   - active: it overrode BOTH halves of the pair, so it always paired.
  //   - hover:  it swapped only the background, to a surface the old
  //             foreground happened to sit on legibly (7.98:1).
  // The failure was the RESTING tile in both themes: a teal/mint fill wearing
  // a foreground calculated for the sidebar surface. That is what the control
  // must reproduce, and the first draft of this selftest demanded red from
  // hover too — the guard caught that before it could be believed.
  const shouldBeRed = broken.filter((r) => !r.active && !r.hover);
  const stillPassing = shouldBeRed.filter((r) => r.ratio >= MIN_RATIO);
  if (stillPassing.length > 0) {
    console.error(
      `\n❌ SELFTEST FAILED: the known-bad recipe scored >= ${MIN_RATIO}:1 in ` +
        `${stillPassing.map((r) => r.name.trim()).join(", ")}.\n` +
        `   This guard cannot distinguish readable from unreadable, so a pass from ` +
        `it proves nothing.`,
    );
    exitCode = 1;
  } else {
    console.log(
      `\n  ✓ selftest: the pre-fix recipe is correctly reported red ` +
        `(${shouldBeRed.map((r) => r.ratio.toFixed(2)).join(", ")}) — the guard discriminates.`,
    );
  }
}

await browser.close();

if (liveFails.length > 0) {
  console.error(
    `\n❌ ${liveFails.length} state(s) below ${MIN_RATIO}:1 — the letter is not ` +
      `readable on its tile:\n` +
      liveFails.map((r) => `   ${r.name.trim()}: ${r.ratio.toFixed(2)}:1`).join("\n"),
  );
  exitCode = 1;
} else {
  console.log(`\n✓ all ${live.length} states meet WCAG AA (>= ${MIN_RATIO}:1)`);
}

process.exit(exitCode);

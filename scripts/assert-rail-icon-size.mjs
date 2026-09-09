#!/usr/bin/env node
/**
 * Size guard for the collapsed-rail glyphs (`#13ff4803`).
 *
 * WHY THIS EXISTS
 * Three consecutive fixes of the collapsed rail measured tile geometry and
 * coordinates, found them perfect, and shipped — while the workspace mark
 * inside the first tile rendered 18.55x18.55 against 16x16 for all ten of its
 * neighbours, every time. The measurements were not wrong: every tile really
 * is 32x32 at cx=23.5, and every glyph's ink really is centred to within
 * 0.01px. Nothing was off-centre. What differed was ink WIDTH, and nothing
 * looked at ink width, so the one asymmetry in the column survived every pass.
 * `assert-rail-icon-contrast.mjs` exists because geometry was measured and
 * legibility was not; this exists because the box was measured and the mark
 * inside it was not.
 *
 * WHAT IT MEASURES
 * In a real browser, over the real compiled stylesheet, with the real merged
 * classes: the <svg> box of the workspace fallback mark, and the bounding box
 * of the ink it actually draws, against the same two numbers for a real rail
 * navigation icon. Not class names, not the intended ratio — the pixels.
 *
 * WHY ink AND NOT THE <svg> BOX ALONE
 * A glyph is centred by flexbox, so its element box is centred by
 * construction; measuring only that can confirm the layout engine works and
 * nothing else. The mark and the lucide icons fill their viewBoxes to
 * different extents (this mark's paths span its full viewBox width; lucide
 * draws inside a 20-of-24 design box), so two glyphs with identical <svg>
 * boxes can still paint very differently-sized shapes. Both are asserted.
 *
 * WHY IT IMPORTS BOTH SIDES RATHER THAN RESTATING THEM
 * The fill ratio and the tile size come from
 * `web/src/components/layout/rail-icon-classes.ts`; the rail's glyph class is
 * read out of `sidebar.tsx`; the mark's paths are read out of
 * `mesh-icon.tsx`; the comparison icon's geometry is imported from
 * `lucide-react` itself. A restated copy of any of them is a copy that keeps
 * passing after the thing it copied has changed — which is precisely how a
 * back-fitted 58% survived being described as a principled percentage.
 *
 * NEGATIVE CONTROL
 * `--selftest` additionally feeds the pre-fix `h-[58%] w-[58%]` through the
 * same path and requires it to come out RED. A size check that cannot fail is
 * not a size check, and "it passed" means nothing until you have watched it
 * fail on input known to be bad.
 *
 * USAGE
 *   node scripts/assert-rail-icon-size.mjs [--selftest]
 * Requires the frontend to have been built (web/dist/assets/*.css):
 *   cd web && pnpm build
 * If Playwright's bundled browser is not installed, point at any Chromium:
 *   RAIL_CONTRAST_CHROMIUM=/path/to/chrome node scripts/assert-rail-icon-size.mjs
 */

import { readFileSync, readdirSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { launchRailGateChromium } from "./rail-visual-gate-chromium.mjs";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, "..");
const WEB = path.join(REPO, "web");

/**
 * How far the mark's optical size may sit from the rail's, as a fraction.
 * Optical size is compared as the geometric mean of ink width and height,
 * because the mark is wide-and-flat (aspect ~1.4:1) while lucide icons are
 * square, so no single axis can be matched without mismatching the other.
 * 8% is roughly the point at which a size difference stops being noticeable
 * next to a neighbouring glyph; the pre-fix value missed by 18%.
 */
const OPTICAL_TOLERANCE = 0.08;
/** The <svg> box must match the rail's exactly — same declared size, no slack. */
const BOX_TOLERANCE_PX = 0.5;

const { cn } = await import(path.join(WEB, "src/lib/cn.ts"));
const recipes = await import(
  path.join(WEB, "src/components/layout/rail-icon-classes.ts")
);
// Ink arithmetic shared with web/scripts/local-stack/measure-rail-ink.mjs — a
// guard and the evidence run behind it must measure the same way.
const { inkBoxSource, opticalSize } = await import(
  path.join(WEB, "scripts/local-stack/rail-ink.mjs")
);

/**
 * The fill as it shipped from `1a63877f` through `#119078b0` — the value that
 * rendered the mark 18.55px wide in a column of 16px glyphs. Kept ONLY as the
 * negative control's input. It is deliberately a literal: this is the one
 * place a frozen copy is correct, because it must not follow the module when
 * the module is fixed.
 *
 * It is applied as an INLINE STYLE, not as the `h-[58%] w-[58%]` class it
 * originally was, and that detail is load-bearing. Tailwind only compiles
 * classes it finds in the source, so once the source stopped saying `h-[58%]`
 * the compiled stylesheet stopped carrying a rule for it — and the control
 * then measured an unstyled svg stretching to its 32px parent. It still came
 * out red, which is the trap: a control that fails because its input never
 * applied proves the guard can print "❌", not that it can detect a wrong
 * size. Measured live: as a class, 32.00x32.00 (no rule); as inline style,
 * 18.55x18.55 (the real pre-fix rendering). Only the second is the defect.
 */
const BROKEN_ICON_PERCENT = 58;

function fail(msg) {
  console.error(`\n❌ ${msg}\n`);
  process.exit(1);
}

function findBuiltCss() {
  const assets = path.join(WEB, "dist", "assets");
  if (!existsSync(assets)) {
    fail(
      `No built stylesheet at ${assets}\n` +
        `This check compares sizes the browser actually computes, so it needs the\n` +
        `compiled CSS. Build it first:  cd web && pnpm build\n` +
        `(A missing baseline is a FAILURE here, never a skip — a check that can\n` +
        ` silently not-check is the same false green it exists to prevent.)`,
    );
  }
  const css = readdirSync(assets).filter((f) => f.endsWith(".css"));
  if (css.length === 0) fail(`No .css in ${assets} — rebuild the frontend.`);
  const chosen = css
    .map((f) => ({ f, size: readFileSync(path.join(assets, f)).length }))
    .sort((a, b) => b.size - a.size)[0];
  return readFileSync(path.join(assets, chosen.f), "utf8");
}

/**
 * The class the collapsed rail puts on its navigation icons, read from the
 * component rather than assumed. Every rail icon must carry the same one — if
 * they ever disagree there is no single "rail glyph size" to compare against
 * and the guard says so instead of silently picking one.
 */
function railGlyphClassFromSidebar() {
  const src = readFileSync(
    path.join(WEB, "src/components/layout/sidebar.tsx"),
    "utf8",
  );
  const collapsedStart = src.indexOf("if (collapsed) {");
  if (collapsedStart === -1) fail("sidebar.tsx: no `if (collapsed)` branch found");
  // The collapsed branch ends where the expanded return begins; taking the
  // whole rest of the file would mix in the expanded sidebar's own icons.
  const collapsedEnd = src.indexOf("\n  return (", collapsedStart);
  const region = src.slice(
    collapsedStart,
    collapsedEnd === -1 ? src.length : collapsedEnd,
  );
  const classes = [
    ...region.matchAll(/<[A-Z][A-Za-z0-9]*\s+className="(h-\d[^"]*w-\d[^"]*)"/g),
  ]
    .map((m) => m[1])
    // The unread-count badge is a positioned dot, not a rail glyph.
    .filter((c) => !c.includes("absolute"));
  if (classes.length === 0) fail("sidebar.tsx: no rail icon classes found in the collapsed branch");
  const sizeOf = (c) => (c.match(/\bh-\d+\b/) || [""])[0] + " " + (c.match(/\bw-\d+\b/) || [""])[0];
  const sizes = [...new Set(classes.map(sizeOf))];
  if (sizes.length !== 1) {
    fail(
      `sidebar.tsx: the collapsed rail draws more than one glyph size (${sizes.join(", ")}).\n` +
        `There is no single rail glyph size to hold the workspace mark to. Make the\n` +
        `rail consistent first, or teach this guard which one is canonical.`,
    );
  }
  return { klass: sizes[0], count: classes.length };
}

/**
 * The mark's real paths and viewBox, read from the component. Sized either by
 * the module's own class (the real path under test) or by an inline
 * percentage (the negative control — see BROKEN_ICON_PERCENT).
 */
function meshIconMarkup({ fillClass, inlinePercent }) {
  const src = readFileSync(path.join(WEB, "src/components/mesh-icon.tsx"), "utf8");
  const viewBox = (src.match(/viewBox="([^"]+)"/) || [])[1];
  if (!viewBox) fail("mesh-icon.tsx: no viewBox found");
  const ds = [...src.matchAll(/d="([^"]+)"/g)].map((m) => m[1]);
  if (ds.length === 0) fail("mesh-icon.tsx: no path data found");
  const sizing = inlinePercent
    ? `style="height:${inlinePercent}%;width:${inlinePercent}%"`
    : `class="${fillClass}"`;
  return (
    `<svg id="mark" xmlns="http://www.w3.org/2000/svg" viewBox="${viewBox}" ${sizing}>` +
    ds.map((d) => `<path fill="currentColor" d="${d}"/>`).join("") +
    `</svg>`
  );
}

/** A real rail icon, built from lucide's own geometry rather than a copy. */
async function lucideMarkup(glyphClass) {
  const [{ default: attrs }, { __iconNode }] = await Promise.all([
    import(path.join(WEB, "node_modules/lucide-react/dist/esm/defaultAttributes.mjs")),
    import(path.join(WEB, "node_modules/lucide-react/dist/esm/icons/layout-dashboard.mjs")),
  ]);
  const children = __iconNode
    .map(([tag, a]) => {
      const props = Object.entries(a)
        .filter(([k]) => k !== "key")
        .map(([k, v]) => `${k}="${v}"`)
        .join(" ");
      return `<${tag} ${props}/>`;
    })
    .join("");
  return (
    `<svg id="ref" xmlns="${attrs.xmlns}" viewBox="${attrs.viewBox}" fill="${attrs.fill}" ` +
    `stroke="${attrs.stroke}" stroke-width="${attrs.strokeWidth}" ` +
    `stroke-linecap="${attrs.strokeLinecap}" stroke-linejoin="${attrs.strokeLinejoin}" ` +
    `class="${glyphClass}">${children}</svg>`
  );
}

async function measure(page, css, { tileClass, markMarkup, refMarkup }) {
  await page.setContent(
    `<!doctype html><html><head><style>${css}</style></head><body style="margin:0">` +
      `<div class="bg-sidebar" style="padding:24px;width:64px">` +
      `<div id="tile" class="${tileClass}">${markMarkup}</div>` +
      `<a href="#" id="navtile" class="flex h-8 w-8 items-center justify-center rounded-lg">${refMarkup}</a>` +
      `</div></body></html>`,
    { waitUntil: "load" },
  );
  return page.evaluate(
    ({ inkSrc }) => {
      const inkBox = new Function("return " + inkSrc)();
      const one = (id) => {
        const svg = document.getElementById(id);
        const s = svg.getBoundingClientRect();
        return { box: { w: s.width, h: s.height }, ink: inkBox(svg) };
      };
      const tile = document.getElementById("tile").getBoundingClientRect();
      return {
        mark: one("mark"),
        ref: one("ref"),
        tile: { w: tile.width, h: tile.height },
      };
    },
    { inkSrc: inkBoxSource() },
  );
}

const r2 = (n) => Math.round(n * 100) / 100;

async function runSuite(page, css, sizing, glyph, label) {
  const m = await measure(page, css, {
    tileClass: cn(
      recipes.workspaceLogoContainerParts({ variant: "collapsed", isLoaded: false }),
    ),
    markMarkup: meshIconMarkup(sizing),
    refMarkup: await lucideMarkup(glyph.klass),
  });
  if (!m.mark.ink || !m.ref.ink) {
    fail(`${label}: could not measure ink — the markup rendered nothing drawable.`);
  }
  // The sizing must have APPLIED. An svg with no effective size rule stretches
  // to its flex parent, which here is the 32px tile — and a mark measured at
  // the tile's own size would then be compared against the rail and reported
  // as a size defect that is really a missing stylesheet rule. Two different
  // faults must not share one red.
  if (Math.abs(m.mark.box.w - m.tile.w) < 0.01 && Math.abs(m.mark.box.h - m.tile.h) < 0.01) {
    fail(
      `${label}: the mark rendered at exactly the tile's size (${r2(m.tile.w)}px), which means\n` +
        `   its sizing rule never applied — most likely the class is not in the compiled\n` +
        `   stylesheet. Rebuild the frontend (cd web && pnpm build). This is NOT a size\n` +
        `   verdict: nothing was actually measured.`,
    );
  }
  const boxDelta = Math.abs(m.mark.box.w - m.ref.box.w);
  const opticalRatio = opticalSize(m.mark.ink) / opticalSize(m.ref.ink);
  const opticalMiss = Math.abs(opticalRatio - 1);
  const boxOk = boxDelta <= BOX_TOLERANCE_PX;
  const opticalOk = opticalMiss <= OPTICAL_TOLERANCE;

  const how = sizing.inlinePercent
    ? `inline ${sizing.inlinePercent}%`
    : `class ${sizing.fillClass}`;
  console.log(`\n${label}  (${how})`);
  console.log(
    `  workspace mark   svg ${r2(m.mark.box.w)}x${r2(m.mark.box.h)}` +
      `   ink ${r2(m.mark.ink.w)}x${r2(m.mark.ink.h)}   optical ${r2(opticalSize(m.mark.ink))}`,
  );
  console.log(
    `  rail glyph (${glyph.klass})   svg ${r2(m.ref.box.w)}x${r2(m.ref.box.h)}` +
      `   ink ${r2(m.ref.ink.w)}x${r2(m.ref.ink.h)}   optical ${r2(opticalSize(m.ref.ink))}`,
  );
  console.log(
    `  ${boxOk ? "✅" : "❌"} svg box delta ${r2(boxDelta)}px (max ${BOX_TOLERANCE_PX})` +
      `   ${opticalOk ? "✅" : "❌"} optical ${r2(opticalRatio)}x ` +
      `(max ${1 + OPTICAL_TOLERANCE}x)`,
  );
  return { boxOk, opticalOk, ok: boxOk && opticalOk, boxDelta, opticalRatio };
}

const selftest = process.argv.includes("--selftest");
const css = findBuiltCss();
const glyph = railGlyphClassFromSidebar();
console.log(
  `rail glyph class read from sidebar.tsx: "${glyph.klass}" ` +
    `(${glyph.count} icons in the collapsed branch, all agreeing)`,
);

const browser = await launchRailGateChromium(WEB, fail);
const page = await browser.newPage();

let exitCode = 0;
const current = await runSuite(
  page,
  css,
  { fillClass: recipes.WORKSPACE_LOGO_ICON_FILL },
  glyph,
  "CURRENT recipe",
);
if (!current.ok) exitCode = 1;

if (selftest) {
  const broken = await runSuite(
    page,
    css,
    { inlinePercent: BROKEN_ICON_PERCENT },
    glyph,
    "NEGATIVE CONTROL (pre-fix)",
  );
  if (broken.ok) {
    console.error(
      `\n❌ negative control PASSED. The pre-fix 58% fill must be rejected by this\n` +
        `   guard; that it is not means the guard cannot fail and proves nothing.\n`,
    );
    exitCode = 1;
  } else {
    console.log(`\n✅ negative control correctly REJECTED the pre-fix 58% fill.`);
  }
}

await browser.close();
if (exitCode === 0) console.log(`\n✅ collapsed-rail glyph sizes agree.\n`);
else console.error(`\n❌ collapsed-rail glyph sizes disagree.\n`);
process.exit(exitCode);

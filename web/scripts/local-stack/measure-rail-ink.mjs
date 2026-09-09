#!/usr/bin/env node
/**
 * Evidence run: every glyph in the REAL collapsed rail, both themes
 * (`#13ff4803`).
 *
 * `measure-rail-slide.mjs` next to this file answers "did anything move?" and
 * correctly answers no — every tile is 32x32 at cx=23.5 and every glyph is
 * centred to within 0.01px. This answers the question that was never asked:
 * "is anything a different SIZE?" It reports each glyph's <svg> box, the
 * bounding box of the ink it actually paints, and how far that ink's centre
 * sits from its tile's centre, so the whole column can be read as a table
 * rather than one tile at a time.
 *
 * The ink arithmetic is imported from `rail-ink.mjs`, shared with
 * `scripts/assert-rail-icon-size.mjs`. The guard and the evidence for it must
 * measure identically or neither means anything.
 *
 * USAGE
 *   node scripts/local-stack/measure-rail-ink.mjs <seed.json> <out-dir> [label]
 * where seed.json carries { login_url, email, password } — the file
 * `scripts/local-stack.sh up` writes. Needs a running local stack.
 */
import { chromium } from "@playwright/test";
import { readFileSync } from "node:fs";
import { inkBoxSource } from "./rail-ink.mjs";

const seed = JSON.parse(readFileSync(process.argv[2], "utf8"));
const outDir = process.argv[3] || ".";
const label = process.argv[4] || "run";

async function measure(page) {
  return page.evaluate(({ inkSrc }) => {
    const inkBox = new Function("return " + inkSrc)();
    const rail = document.querySelector("aside");
    if (!rail) throw new Error("no <aside> — is the sidebar rendered?");
    const out = [];
    for (const el of rail.querySelectorAll("a, [data-testid='workspace-logo']")) {
      const r = el.getBoundingClientRect();
      if (!r.width || !r.height) continue;
      const svg = el.querySelector("svg");
      let svgBox = null;
      let ink = null;
      if (svg) {
        const s = svg.getBoundingClientRect();
        svgBox = { w: +s.width.toFixed(2), h: +s.height.toFixed(2) };
        ink = inkBox(svg);
      }
      out.push({
        testid:
          el.getAttribute("data-testid") ||
          (svg ? svg.classList[1] || "icon" : "text"),
        tile: {
          w: +r.width.toFixed(2),
          h: +r.height.toFixed(2),
          cx: +(r.left + r.width / 2).toFixed(2),
          cy: +(r.top + r.height / 2).toFixed(2),
        },
        svg: svgBox,
        ink,
        // Offset of the PAINTED shape from the tile centre — the quantity a
        // "the icon is off" complaint is actually about. The element box being
        // centred says nothing about this.
        dx: ink ? +(ink.cx - (r.left + r.width / 2)).toFixed(2) : null,
        dy: ink ? +(ink.cy - (r.top + r.height / 2)).toFixed(2) : null,
      });
    }
    return out;
  }, { inkSrc: inkBoxSource() });
}

const browser = await chromium.launch({
  executablePath: process.env.RAIL_CONTRAST_CHROMIUM || undefined,
});
const results = {};
for (const dark of [false, true]) {
  const key = dark ? "dark" : "light";
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    colorScheme: dark ? "dark" : "light",
  });
  const page = await ctx.newPage();
  await page.goto(seed.login_url, { waitUntil: "domcontentloaded" });
  await page.fill("#email", seed.email);
  await page.fill("#password", seed.password);
  await Promise.all([
    page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 15000 }),
    page.click('button[type="submit"]'),
  ]);
  await page.waitForTimeout(700);
  await page.locator("header button:has(svg.lucide-menu)").first().click();
  // Past the 200ms collapse transition — this run is about settled size, not
  // about the animation (measure-rail-slide.mjs covers the mid-transition frame).
  await page.waitForTimeout(600);
  results[key] = await measure(page);
  await page.locator("aside").screenshot({
    path: `${outDir}/rail-only-${key}-${label}.png`,
  });
  await ctx.close();
}
await browser.close();
console.log(JSON.stringify(results, null, 2));

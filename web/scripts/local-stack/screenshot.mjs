#!/usr/bin/env node
// Authed screenshot helper for scripts/local-stack.sh.
//
// The trap this exists to avoid (found live during D7 verification, 2026-08-20):
// Playwright's `fullPage: true` is a no-op on a page whose content scrolls inside
// an INNER container (`overflow-y: auto` on a div, not on <body>) — it captures the
// outer viewport only. Combined with `scrollIntoViewIfNeeded()` this produces a frame
// where the target element is visible but the rest of the page has scrolled out of
// it — a picture that cannot answer "is this section in the right place", and every
// assertion around it was green. It was caught only by opening the PNG and looking.
//
// Fix used here: don't rely on fullPage or scrolling at all. Detect which element is
// actually the scrolling container, grow the BROWSER VIEWPORT to that container's
// full content height, and screenshot the viewport (no scroll needed because
// everything now fits). Self-checked: if some element still needs to scroll after
// growing the viewport (e.g. a container with a hardcoded pixel height, unrelated to
// the viewport), refuse to emit a screenshot rather than emit a silently-wrong one.
import { chromium } from "@playwright/test";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname } from "node:path";

function parseArgs(argv) {
  const out = { widths: [1440], outDir: ".", assertVisible: [] };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const next = () => argv[++i];
    switch (a) {
      case "--url":
        out.url = next();
        break;
      case "--login-url":
        out.loginUrl = next();
        break;
      case "--email":
        out.email = next();
        break;
      case "--password":
        out.password = next();
        break;
      case "--widths":
        out.widths = next()
          .split(",")
          .map((s) => Number.parseInt(s.trim(), 10));
        break;
      case "--out-dir":
        out.outDir = next();
        break;
      case "--prefix":
        out.prefix = next();
        break;
      case "--assert-visible":
        out.assertVisible = next()
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean);
        break;
      case "--no-login":
        out.noLogin = true;
        break;
      case "--dark":
        out.dark = true;
        break;
      case "--max-height":
        out.maxHeight = Number.parseInt(next(), 10);
        break;
      default:
        throw new Error(`unknown arg: ${a}`);
    }
  }
  if (!out.url) throw new Error("--url is required");
  if (!out.noLogin && (!out.loginUrl || !out.email || !out.password)) {
    throw new Error("--login-url, --email and --password are required unless --no-login");
  }
  out.prefix ??= "shot";
  out.maxHeight ??= 8000;
  return out;
}

// Finds the element most likely to be "the real scrollable content" — the one with
// the largest (scrollHeight - clientHeight) among elements whose computed overflow-y
// is auto/scroll. Deliberately not tied to any app-specific class or data-testid: the
// whole point is that this trap (content scrolling inside a div, not <body>) recurs
// across pages and products, so the detector has to be structural.
async function findScroller(page) {
  return page.evaluate(() => {
    let best = null;
    let bestSlack = 0;
    const all = document.querySelectorAll("*");
    for (const el of all) {
      const style = getComputedStyle(el);
      if (style.overflowY !== "auto" && style.overflowY !== "scroll") continue;
      const slack = el.scrollHeight - el.clientHeight;
      if (slack > bestSlack) {
        bestSlack = slack;
        best = el;
      }
    }
    if (!best) return null;
    const rect = best.getBoundingClientRect();
    return {
      slack: bestSlack,
      scrollHeight: best.scrollHeight,
      clientHeight: best.clientHeight,
      tag: best.tagName,
      className: typeof best.className === "string" ? best.className : "",
      top: rect.top,
    };
  });
}

// Grows the viewport until the detected scroller has no slack left, or gives up.
// Returns the final scroller reading (post-growth) so the caller can self-check it.
async function growViewportToFitContent(page, width, maxHeight) {
  let height = (await page.viewportSize())?.height ?? 900;
  let scroller = await findScroller(page);
  for (let i = 0; i < 6 && scroller && scroller.slack > 4; i++) {
    height = Math.min(maxHeight, height + scroller.slack + 32);
    await page.setViewportSize({ width, height });
    // Layout can reflow after a resize (responsive breakpoints, flex re-measure);
    // let it settle before re-measuring.
    await page.waitForTimeout(150);
    scroller = await findScroller(page);
  }
  return { height, scroller };
}

async function assertVisible(page, selectors, viewportHeight) {
  const failures = [];
  for (const sel of selectors) {
    const rect = await page.evaluate((s) => {
      const el = document.querySelector(s);
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return { top: r.top, bottom: r.bottom, width: r.width, height: r.height };
    }, sel);
    if (!rect) {
      failures.push(`${sel}: not found in DOM`);
      continue;
    }
    if (rect.width === 0 || rect.height === 0) {
      failures.push(`${sel}: zero-size (${rect.width}x${rect.height}) — hidden or not rendered`);
      continue;
    }
    if (rect.top < -1 || rect.bottom > viewportHeight + 1) {
      failures.push(
        `${sel}: out of frame (top=${rect.top.toFixed(1)} bottom=${rect.bottom.toFixed(1)}, frame height=${viewportHeight})`
      );
    }
  }
  return failures;
}

async function login(page, { loginUrl, email, password }) {
  await page.goto(loginUrl, { waitUntil: "domcontentloaded" });
  await page.fill("#email", email);
  await page.fill("#password", password);
  await Promise.all([
    page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 15_000 }),
    page.click('button[type="submit"]'),
  ]);
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  await mkdir(args.outDir, { recursive: true });

  const browser = await chromium.launch();
  const results = [];
  try {
    const context = await browser.newContext({
      viewport: { width: args.widths[0], height: 900 },
      colorScheme: args.dark ? "dark" : "light",
    });
    const page = await context.newPage();

    if (!args.noLogin) {
      await login(page, args);
    }

    for (const width of args.widths) {
      await page.setViewportSize({ width, height: 900 });
      await page.goto(args.url, { waitUntil: "networkidle" });

      const { height, scroller } = await growViewportToFitContent(page, width, args.maxHeight);

      if (scroller && scroller.slack > 4) {
        results.push({
          width,
          ok: false,
          reason: `refusing to screenshot: element <${scroller.tag} class="${scroller.className}"> still needs ${scroller.slack}px of scroll after growing the viewport to ${height}px (its content height does not track the viewport — likely a hardcoded pixel height). This is the exact failure class fullPage screenshots hide silently.`,
        });
        continue;
      }

      const failures = await assertVisible(page, args.assertVisible, height);
      if (failures.length > 0) {
        results.push({ width, ok: false, reason: failures.join("; ") });
        continue;
      }

      const outPath = `${args.outDir}/${args.prefix}-${width}${args.dark ? "-dark" : ""}.png`;
      await mkdir(dirname(outPath), { recursive: true });
      await page.screenshot({ path: outPath, fullPage: false });
      results.push({ width, ok: true, path: outPath, viewportHeight: height });
    }
  } finally {
    await browser.close();
  }

  await writeFile(`${args.outDir}/${args.prefix}-results.json`, JSON.stringify(results, null, 2));
  console.log(JSON.stringify(results, null, 2));

  const failed = results.filter((r) => !r.ok);
  if (failed.length > 0) {
    console.error(`${failed.length}/${results.length} screenshot(s) failed self-check.`);
    process.exit(1);
  }
}

main().catch((err) => {
  console.error(err.stack || String(err));
  process.exit(1);
});

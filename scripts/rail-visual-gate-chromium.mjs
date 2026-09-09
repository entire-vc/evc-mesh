/**
 * Chromium loader shared by the rail visual gates (`#d8916572`).
 *
 * WHY THIS EXISTS
 * `assert-rail-icon-contrast.mjs` learned two things the hard way: the
 * `@playwright/test` namespace object may carry `chromium` directly or hang
 * it off `.default` depending on which specifier resolves, and a missing
 * module or an unlaunchable browser must become a named reason with a fix,
 * never a raw stack trace. `assert-rail-icon-size.mjs` — its neighbour, run
 * in the same CI job right after it — never learned either lesson: one
 * hardcoded `.mjs` specifier, no fallback, no `try/catch` around `launch()`.
 * The lesson was learned in one file and lost in the file next to it. This
 * module is the one definition both gates call, so that cannot happen again.
 *
 * Both callers live in top-level `scripts/`, outside `web/`'s own module
 * resolution, so `@playwright/test` cannot be a plain static import here — it
 * has to be located under `web/node_modules` explicitly. That is why this
 * takes `WEB` (the path to the `web/` package) as an argument.
 */

import path from "node:path";

/**
 * Resolve the `chromium` launcher, trying the specifiers/export shapes that
 * have actually been observed to differ across resolvers. Returns `null`
 * (never throws) when none of them yield a usable launcher, so the caller can
 * turn that into its own named failure.
 */
export async function loadChromium(WEB) {
  for (const spec of [
    path.join(WEB, "node_modules/@playwright/test/index.js"),
    path.join(WEB, "node_modules/@playwright/test/index.mjs"),
    "@playwright/test",
  ]) {
    try {
      const mod = await import(spec);
      const chromium = mod.chromium ?? mod.default?.chromium;
      if (chromium) return chromium;
    } catch {
      // try the next specifier
    }
  }
  return null;
}

/**
 * Load and launch Chromium, or call `fail(message)` with a named reason and a
 * fix — never an unhandled rejection or a bare stack trace. `fail` is
 * expected to terminate the process (both gates' `fail()` calls
 * `process.exit(1)`), so this never needs to handle a `null`/undefined
 * launcher itself past that point.
 *
 * `RAIL_CONTRAST_CHROMIUM` points at an external Chrome/Chromium binary when
 * Playwright's bundled browser is not installed — both gates already
 * document and rely on this env var, so it stays here rather than in each
 * caller.
 */
export async function launchRailGateChromium(WEB, fail) {
  const chromium = await loadChromium(WEB);
  if (!chromium) {
    fail(
      "Could not load Playwright. Install the frontend deps first: cd web && pnpm install",
    );
  }
  const launchOpts = process.env.RAIL_CONTRAST_CHROMIUM
    ? { executablePath: process.env.RAIL_CONTRAST_CHROMIUM }
    : {};
  try {
    return await chromium.launch(launchOpts);
  } catch (e) {
    fail(
      `Could not launch Chromium: ${String(e).split("\n")[0]}\n` +
        `Install it (cd web && npx playwright install chromium) or set\n` +
        `RAIL_CONTRAST_CHROMIUM=/path/to/chrome`,
    );
  }
}

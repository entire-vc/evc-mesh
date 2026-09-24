import { defineConfig, devices } from "@playwright/test";

/**
 * Per-action perf counters — part 2 of the храповик (web/perf/README.md).
 *
 * Its own config, not a project inside ../playwright.config.ts, on purpose:
 * that config's globalSetup demands the prod E2E credentials and points at the
 * live app, and this suite needs neither. It measures a build made with
 * VITE_PERF_PROFILER=1 (dist-perf/), served by `vite preview` and proxied
 * same-origin to an EPHEMERAL API seeded by perf/seed-fixture.mjs — fixed
 * data, no other writers, so the counters mean the same thing every run.
 *
 * Env:
 *   PERF_API_URL  the ephemeral API (CI: the job's own cmd/api; locally:
 *                 scripts/local-stack.sh's API port)
 */
const PORT = Number(process.env.PERF_PREVIEW_PORT || 4317);

export default defineConfig({
  testDir: ".",
  testMatch: "*.spec.ts",
  outputDir: "./test-results",
  timeout: 180_000,
  // A retry would hide exactly the non-determinism this suite has to surface.
  retries: 0,
  workers: 1,
  reporter: [["list"]],
  use: {
    baseURL: `http://localhost:${PORT}`,
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
    trace: "off",
    screenshot: "off",
  },
  webServer: {
    command: `pnpm exec vite preview --outDir dist-perf --port ${PORT} --strictPort`,
    cwd: "..",
    url: `http://localhost:${PORT}/login`,
    reuseExistingServer: false,
    timeout: 60_000,
    env: { PERF_API_URL: process.env.PERF_API_URL ?? "" },
  },
  projects: [
    {
      name: "perf-counters",
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 1440, height: 900 },
        // A service worker would serve the second navigation from its cache
        // and make "open" mean something different from run to run.
        serviceWorkers: "block",
      },
    },
  ],
});

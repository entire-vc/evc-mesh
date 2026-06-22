import { defineConfig, devices } from "@playwright/test";

/**
 * Playwright config for authed E2E tests (F1 harness).
 *
 * Auth: global-setup logs in once via Casdoor and saves storageState.json.
 * All tests in the authed-e2e project reuse that session.
 *
 * F1 harness: console.error/pageerror and HTTP 5xx assertions are enforced via
 * PW_FAIL_ON_CONSOLE_ERROR and PW_FAIL_ON_5XX env vars (see task-board.spec.ts).
 *
 * Required env vars:
 *   APP_BASE_URL       — e.g. https://mesh.entire.host
 *   CASDOOR_BASE_URL   — e.g. https://auth.entire.host
 *   CASDOOR_AGENT_USER — Casdoor username for the CI agent user
 *   CASDOOR_AGENT_PASSWORD — Casdoor password for the CI agent user
 */
export default defineConfig({
  testDir: "./e2e",
  // global-setup runs once before all workers: logs in via Casdoor, writes storageState.json.
  // Using globalSetup (not a setup project) so the file exists before authed-e2e workers start.
  globalSetup: "./e2e/global-setup.ts",
  timeout: 30_000,
  retries: process.env.CI ? 1 : 0,
  reporter: [["html", { outputFolder: "playwright-report" }], ["list"]],

  use: {
    baseURL: process.env.APP_BASE_URL || "https://mesh.entire.host",
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    // F1 fixture env flags (read inside the fixture in each spec)
    // PW_FAIL_ON_CONSOLE_ERROR and PW_FAIL_ON_5XX are set in CI env
  },

  projects: [
    // Authed E2E: all tests in e2e/ (global-setup.ts runs as globalSetup above, not as a test)
    {
      name: "authed-e2e",
      testIgnore: "**/global-setup.ts",
      use: {
        ...devices["Desktop Chrome"],
        storageState: "e2e/.auth/user.json",
      },
    },
  ],
});

import { defineConfig, devices } from "@playwright/test";

/**
 * Playwright config for the authed E2E suite (F1 harness).
 *
 * Auth: the spec logs in once, in a single shared browser context, through
 * Mesh's own /api/v1/auth/login. There is no storageState and no Casdoor/OIDC
 * flow — this app authenticates natively (web/src/stores/auth.ts), and its
 * refresh tokens rotate one-shot, so replaying a saved cookie into a second
 * context trips theft detection and revokes every session for the user.
 *
 * Required env:
 *   APP_BASE_URL       — e.g. https://mesh.entire.host
 *   E2E_USER_EMAIL     — dedicated CI user (workspace member, not owner)
 *   E2E_USER_PASSWORD  — its password
 *
 * global-setup fails the run when the credentials are absent, so an
 * unconfigured suite reports red rather than a green check over skipped tests.
 */
export default defineConfig({
  testDir: "./e2e",
  globalSetup: "./e2e/global-setup.ts",
  timeout: 60_000,
  // No retries: a retry re-runs beforeAll, and each run spends a login against
  // an endpoint capped at 5 requests/minute per IP.
  retries: 0,
  // The suite shares one context and one login, so it must not be sharded.
  workers: 1,
  reporter: [["html", { outputFolder: "playwright-report" }], ["list"]],

  use: {
    baseURL: process.env.APP_BASE_URL || "https://mesh.entire.host",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },

  projects: [
    {
      name: "authed-e2e",
      testIgnore: "**/global-setup.ts",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});

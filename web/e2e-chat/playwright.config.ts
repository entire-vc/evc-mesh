import { defineConfig, devices } from "@playwright/test";

/**
 * Playwright config for the agent-chat acceptance run (Mesh Doc e2e/agent-chat,
 * plan e2e/e2eagent-chat--ci-setup-plan-e6-setup).
 *
 * The target is the PUBLISHED app (APP_BASE_URL = $MESH_PROD_APP_BASE_URL in CI):
 * the run needs a real runner and a real interactive agent, so there is no local
 * stack and no mock of the API, the DB, the queue or the WebSocket. global-setup
 * prints /api/v1/version and opens the chat settings window; global-teardown
 * closes it again (enabled=false, agents=[]) and removes the run's threads.
 *
 * Browser contexts are made per actor (A, B, C, V) inside the specs, with no
 * shared storage. Traces, videos and HAR are OFF: every request carries a bearer
 * token and the specs hold private canary text.
 */
export default defineConfig({
  testDir: ".",
  testMatch: /.*\.spec\.ts$/,
  globalSetup: "./global-setup.ts",
  globalTeardown: "./global-teardown.ts",
  timeout: 6 * 60_000,
  retries: 0,
  workers: 1,
  fullyParallel: false,
  reporter: [
    ["list"],
    ["junit", { outputFile: "../agent-chat-e2e/junit.xml" }],
  ],
  use: {
    baseURL: process.env.APP_BASE_URL,
    trace: "off",
    video: "off",
    screenshot: "off",
  },
  projects: [
    // Pure red controls: no network, no browser. Run first so a broken detector
    // is known before it is trusted against the live target.
    { name: "controls", testMatch: /controls\.spec\.ts$/ },
    // S07 waits out the 1 h offline TTL; it runs before agent-chat so nothing else is queued for the
    // offline agent while its runner claims, and a failure here does not skip the rest.
    {
      name: "s07",
      testMatch: /s07\.spec\.ts$/,
      dependencies: ["controls"],
      timeout: 90 * 60_000,
      use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } },
    },
    {
      name: "agent-chat",
      testIgnore: /(controls|s07)\.spec\.ts$/,
      dependencies: ["controls"],
      use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } },
    },
  ],
});

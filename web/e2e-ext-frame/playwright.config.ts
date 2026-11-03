import { defineConfig, devices } from "@playwright/test";

/**
 * Browser receipt for the external-extension iframe lifecycle (`mesh.context.error`,
 * `mesh.revoked`, the revoked screen) at 1440 and 393.
 *
 * The target is the PUBLISHED app (APP_BASE_URL = $MESH_PROD_APP_BASE_URL in CI): the run needs the
 * real ui-context endpoint and a real receiver on its own origin, whose frame-ancestors names the
 * published Mesh origin. global-setup prints /api/v1/version and fails when E2E_EXT_EXPECT_SHA is set
 * and differs. Traces, videos and HAR are OFF: every request carries a bearer token.
 */
export default defineConfig({
  testDir: ".",
  testMatch: /.*\.spec\.ts$/,
  globalSetup: "./global-setup.ts",
  timeout: 4 * 60_000,
  retries: 0,
  workers: 1,
  fullyParallel: false,
  reporter: [["list"], ["junit", { outputFile: "../ext-frame-e2e/junit.xml" }]],
  use: {
    baseURL: process.env.APP_BASE_URL,
    trace: "off",
    video: "off",
    screenshot: "off",
  },
  projects: [{ name: "ext-frame", use: { ...devices["Desktop Chrome"] } }],
});

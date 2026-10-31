import { defineConfig } from "vitest/config";
import { fileURLToPath, URL } from "node:url";

export default defineConfig({
  test: {
    environment: "jsdom",
    globals: true,
    // The default 5s is below the cold-render cost of the full TaskPanel /
    // settings pages on a shared CI runner: task-panel-dependencies,
    // project-settings-deep-link and others timed out once and passed on
    // retry of the same SHA. A genuinely hung test still fails, at 20s.
    // VITEST_TIMEOUT_MS: scripts/ci/prepush.py raises it for loaded dev machines.
    testTimeout: Number(process.env.VITEST_TIMEOUT_MS ?? 20_000),
    hookTimeout: Number(process.env.VITEST_TIMEOUT_MS ?? 20_000),
    setupFiles: ["./src/test-setup.ts"],
    // Browser and perf specs run through Playwright, outside the unit suite.
    exclude: ["e2e/**", "e2e-local/**", "e2e-tabs/**", "e2e-chat/**", "e2e-appearance/**", "e2e-ext-diag/**", "board-e2e/**", "perf/**", "node_modules/**"],
    coverage: {
      provider: "v8",
      reporter: ["text", "json-summary"],
      include: ["src/**/*.{ts,tsx}"],
      exclude: ["src/**/*.test.*", "src/test-setup.ts", "src/vite-env.d.ts"],
      // Threshold calibrated to current test suite (1 component test file → ~1% lines).
      // Raise in small increments as more tests are added; target 60% long-term.
      thresholds: { lines: 1 },
    },
  },
  resolve: {
    // The contract-test harness lives in packages/ and has no node_modules of its own: it must run on the web copies
    // of these (a second React would break every hook).
    dedupe: ["react", "react-dom", "@testing-library/react", "@testing-library/user-event", "axe-core"],
    alias: {
      // `mesh-theme build` points this at the built views.js (src/core/slots/__tests__/theme-bundle-contract.test.tsx).
      "@theme-bundle": process.env.MESH_THEME_BUNDLE ?? fileURLToPath(new URL("./src/test-utils/no-theme-bundle.ts", import.meta.url)),
      "@": fileURLToPath(new URL("./src", import.meta.url)),
      "@entire-vc/mesh-theme-sdk/testing": fileURLToPath(new URL("../packages/mesh-theme-sdk/testing/index.ts", import.meta.url)),
      // Declarations only in the package; the core's runtime sits behind the same names (src/core/themes/sdk-host.ts).
      "@entire-vc/mesh-theme-sdk": fileURLToPath(new URL("./src/core/themes/sdk-host.ts", import.meta.url)),
    },
  },
});

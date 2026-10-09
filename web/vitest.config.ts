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
    testTimeout: 20_000,
    hookTimeout: 20_000,
    setupFiles: ["./src/test-setup.ts"],
    // Browser and perf specs run through Playwright, outside the unit suite.
    exclude: ["e2e/**", "e2e-local/**", "perf/**", "node_modules/**"],
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
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
});

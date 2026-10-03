import { defineConfig } from "vitest/config";
import { fileURLToPath, URL } from "node:url";

export default defineConfig({
  test: {
    environment: "jsdom",
    globals: true,
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

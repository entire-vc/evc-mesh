import { defineConfig } from "vitest/config";

// Unit tests of the theme's own logic (src/fields.ts). The slot contract tests (CT-*) need the Mesh host
// and run inside `mesh-theme build`; see the README.
export default defineConfig({
  test: { environment: "jsdom", include: ["test/**/*.test.ts"] },
});

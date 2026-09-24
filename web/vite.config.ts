import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { execSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath, URL } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));

function getBuildHash(): string {
  // Prefer CI-provided SHA, fall back to local git, then timestamp.
  if (process.env.GITHUB_SHA) return process.env.GITHUB_SHA.slice(0, 12);
  try {
    return execSync("git rev-parse --short=12 HEAD", {
      stdio: ["ignore", "pipe", "ignore"],
    })
      .toString()
      .trim();
  } catch {
    return String(Date.now());
  }
}

function swCacheVersion(): Plugin {
  return {
    name: "sw-cache-version",
    apply: "build",
    closeBundle() {
      const swPath = resolve(__dirname, "dist/sw.js");
      if (!existsSync(swPath)) return;
      const hash = getBuildHash();
      const cacheName = `mesh-${hash}`;
      const sw = readFileSync(swPath, "utf8");
      if (!sw.includes("__BUILD_HASH__")) {
        // Already patched or template missing — surface to CI logs.
        console.warn("[sw-cache-version] no __BUILD_HASH__ placeholder in dist/sw.js");
        return;
      }
      writeFileSync(swPath, sw.replace("__BUILD_HASH__", cacheName));
      console.log(`[sw-cache-version] CACHE_NAME = ${cacheName}`);
    },
  };
}

// The perf-counter build (web/perf/README.md, part 2). Separate outDir so it
// can never be mistaken for, or overwrite, the dist/ that ships.
const PERF_PROFILER = process.env.VITE_PERF_PROFILER === "1";

export default defineConfig({
  plugins: [react(), tailwindcss(), swCacheVersion()],
  resolve: {
    alias: [
      { find: "@", replacement: fileURLToPath(new URL("./src", import.meta.url)) },
      // React's production build never calls <Profiler onRender>; the
      // profiling build does. Perf build only — see src/lib/perf-profiler.tsx.
      ...(PERF_PROFILER
        ? [{ find: /^react-dom\/client$/, replacement: "react-dom/profiling" }]
        : []),
    ],
  },
  build: {
    // Read by web/perf/check-bundle-budget.mjs to find the entry chunk and
    // its static imports without guessing filenames from hashed output.
    manifest: true,
    outDir: PERF_PROFILER ? "dist-perf" : "dist",
  },
  preview: {
    // The perf spec serves dist-perf with `vite preview` and talks to the
    // ephemeral CI API through this proxy, same-origin, exactly as the dev
    // server does. Unset → preview inherits server.proxy (unchanged).
    ...(process.env.PERF_API_URL
      ? {
          proxy: {
            "/api": { target: process.env.PERF_API_URL, changeOrigin: true },
          },
        }
      : {}),
  },
  build: {
    // Lets a one-off script (or the perf-bundle CI gate, once merged) find
    // the /login entry chunk and its static imports without guessing
    // filenames from hashed output.
    manifest: true,
  },
  server: {
    port: 3000,
    proxy: {
      // Target port is configurable so a second API instance (e.g. scripts/local-stack.sh,
      // on its own throwaway port to avoid colliding with another dev API already on 8005)
      // can be proxied to without editing this file. Defaults to 8005 — unchanged behavior.
      "/api": {
        target: `http://localhost:${process.env.VITE_DEV_API_PORT || 8005}`,
        changeOrigin: true,
      },
    },
  },
});

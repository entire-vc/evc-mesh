import { defineConfig, type Plugin } from "vite";
import { e2eProxy } from "./e2e-proxy";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { execSync } from "node:child_process";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { resolve, dirname, join } from "node:path";
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

/** The commit this build is made from: CI's own SHA first, local git second.
 * CI_COMMIT_SHA is trusted only in this repo's own pipeline. The deploy
 * pipeline (entire-vc/deploy) builds evc-mesh from a checkout of its own, so
 * there CI_COMMIT_SHA is the deploy repo's commit — the stamp must come from
 * the checkout being built. web/perf/check-bundle-budget.mjs applies the same
 * rule, so the two agree on what HEAD means. null = unknown, and then no
 * .build-sha is written — which the check treats as a failure, not a pass. */
function currentCommit(): string | null {
  if (process.env.CI_COMMIT_SHA && process.env.CI_PROJECT_PATH === "entire-vc/evc-mesh") {
    return process.env.CI_COMMIT_SHA.trim();
  }
  try {
    return execSync("git rev-parse HEAD", { cwd: __dirname, stdio: ["ignore", "pipe", "ignore"] })
      .toString()
      .trim();
  } catch {
    return null;
  }
}

// Stamps the built output with the commit it came from (dist/.build-sha), so
// web/perf/check-bundle-budget.mjs can refuse a dist left over from an older
// build: `tsc -b` failing before vite runs leaves the previous dist/ in place,
// and a budget check that reads it passes on code that no longer builds.
function buildSha(): Plugin {
  let outDir = resolve(__dirname, "dist");
  return {
    name: "build-sha",
    apply: "build",
    configResolved(config) {
      outDir = resolve(config.root, config.build.outDir);
    },
    closeBundle() {
      const sha = currentCommit();
      if (!sha) {
        console.warn("[build-sha] cannot determine the current commit; dist/.build-sha not written");
        return;
      }
      writeFileSync(join(outDir, ".build-sha"), `${sha}\n`);
      console.log(`[build-sha] ${sha}`);
    },
  };
}

// The perf-counter build (web/perf/README.md, part 2). Separate outDir so it
// can never be mistaken for, or overwrite, the dist/ that ships.
const PERF_PROFILER = process.env.VITE_PERF_PROFILER === "1";

export default defineConfig({
  plugins: [react(), tailwindcss(), swCacheVersion(), buildSha()],
  resolve: {
    alias: [
      { find: "@", replacement: fileURLToPath(new URL("./src", import.meta.url)) },
      // The theme SDK lives in the repo (packages/mesh-theme-sdk) and only declares its primitives; inside the app the
      // same names are the core's (src/core/themes/sdk-host.ts). Types still come from the package (tsconfig paths).
      { find: /^@entire-vc\/mesh-theme-sdk$/, replacement: fileURLToPath(new URL("./src/core/themes/sdk-host.ts", import.meta.url)) },
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
    // Every browser we support handles <link rel="modulepreload"> natively;
    // where it doesn't, the entry's static imports still load through the
    // module graph. The polyfill only cost bytes in the entry chunk.
    modulePreload: { polyfill: false },
    outDir: PERF_PROFILER ? "dist-perf" : "dist",
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            // The ru catalogs load only for a Russian session, so one chunk
            // costs an English user nothing and the 13 small JSON modules
            // share one gzip dictionary.
            {
              name: "locale-ru",
              test: /src[\\/]locales[\\/]ru[\\/](?!common\.json)/,
              includeDependenciesRecursively: false,
            },
            // The small extension api/lib modules each shipped as a chunk of a
            // few hundred bytes; one chunk saves the per-file gzip overhead.
            {
              name: "extension-libs",
              test: /src[\\/]lib[\\/]extension-(?:repo|instance|settings|connect|admin)\./,
              includeDependenciesRecursively: false,
            },
            // Everything /login loads before first paint goes in one chunk, its icons
            // included: a separate initial icon chunk imports the lucide helper from
            // this one and this one imports the icons back, and that cycle ran the
            // icons before the helper existed ("t is not a function" on /register).
            // The same modules used to ship as ~25 chunks of a few hundred bytes
            // each, every one paying its own gzip header and import/export glue.
            // Locale JSON is kept out: the test-only eager glob in
            // src/i18n/index.ts marks every catalog $initial before the branch is
            // dropped, which would pull the lazy namespaces into this chunk.
            {
              name: "app-initial",
              test: /^(?!.*src[\\/]locales[\\/](?!(?:en|ru)[\\/]common\.json))/,
              tags: ["$initial"],
              includeDependenciesRecursively: false,
            },
            // Route-only icons share one gzip dictionary and stay out of /login.
            {
              name: "icons-lazy",
              test: /node_modules[\\/]lucide-react[\\/]/,
              includeDependenciesRecursively: false,
            },
          ],
        },
      },
    },
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
    // authed-e2e on a merge request serves THIS build with `vite preview` and
    // sends /api and /s3 to the live deployment (the sandbox account lives
    // there). The API sees a same-site Origin, as it would from the real app;
    // the page under test is still the MR's dist/, never the deployed frontend.
    // The API answers with absolute links to its own host (presigned /s3 URLs),
    // so JSON bodies are rewritten to this origin: the page then stays on one
    // origin, as it does in production, and the spec's origin assertion keeps
    // its meaning.
    ...(process.env.E2E_PROXY_API_URL
      ? { proxy: e2eProxy(process.env.E2E_PROXY_API_URL) }
      : {}),
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

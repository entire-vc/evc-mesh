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

export default defineConfig({
  plugins: [react(), tailwindcss(), swCacheVersion()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
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

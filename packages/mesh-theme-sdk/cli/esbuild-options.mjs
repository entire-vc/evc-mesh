import { EXTERNALS } from "./externals.mjs";

/** esbuild options shared by `build` and `dev`, so the bundle you preview is the bundle you ship. */
export function bundleOptions({ entry, cssEntry }) {
  return {
    entryPoints: { ...(entry ? { views: entry } : {}), ...(cssEntry ? { theme: cssEntry } : {}) },
    bundle: true,
    format: "esm",
    platform: "browser",
    target: "es2022",
    jsx: "automatic",
    external: EXTERNALS,
    legalComments: "none",
    sourcemap: false, // the bundle's sha256 is the manifest's; a map would change nothing but the size
    logLevel: "silent",
    write: false,
    outdir: "out",
  };
}

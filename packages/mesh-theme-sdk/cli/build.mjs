import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { build as esbuild } from "esbuild";
import { bundleOptions } from "./esbuild-options.mjs";
import { checkBundleText } from "./externals.mjs";
import { loadRunner, summarize } from "./contract.mjs";
import { assembleManifest, schemaErrors } from "./manifest.mjs";

const sdk = JSON.parse(readFileSync(path.join(import.meta.dirname, "..", "package.json"), "utf8"));

export const sha256 = (buf) => createHash("sha256").update(buf).digest("hex");
export const BASE_MANIFEST = "mesh-extension.json";

/** Resolves the theme project's inputs: manifest, views entry, optional stylesheet. */
export function projectInputs(cwd, { entry, css } = {}) {
  const manifestPath = path.join(cwd, BASE_MANIFEST);
  if (!existsSync(manifestPath)) throw new Error(`${BASE_MANIFEST} not found in ${cwd}`);
  const base = JSON.parse(readFileSync(manifestPath, "utf8"));
  const entryPath = path.resolve(cwd, entry ?? "src/index.tsx");
  const cssPath = path.resolve(cwd, css ?? "src/theme.css");
  return {
    base,
    entry: existsSync(entryPath) ? entryPath : null,
    cssEntry: existsSync(cssPath) ? cssPath : null,
    explicitEntry: !!entry,
  };
}

/** Bundles the theme in memory; returns {views, css} buffers or the build errors. */
export async function bundle(inputs) {
  const res = await esbuild(bundleOptions(inputs));
  const out = {};
  for (const f of res.outputFiles) out[path.basename(f.path)] = Buffer.from(f.contents);
  return out;
}

/**
 * `mesh-theme build`. Returns {ok, problems, report, manifest}. On ok the files are in outDir; when a
 * slot fails or anything else is wrong, contract_report.json is written (it says what failed) and
 * mesh-extension.json is NOT, so there is nothing install-ready to publish by mistake.
 */
export async function build({ cwd, entry, css, outDir = "dist", contractRunner }) {
  const inputs = projectInputs(cwd, { entry, css });
  const out = path.resolve(cwd, outDir);
  mkdirSync(out, { recursive: true });
  rmSync(path.join(out, BASE_MANIFEST), { force: true }); // a failed build must not leave the last green manifest behind
  const problems = [];

  let files = {};
  if (inputs.entry) {
    try {
      files = await bundle(inputs);
    } catch (e) {
      return { ok: false, problems: (e.errors ?? [e]).map((x) => `build: ${x.text ?? x.message}`) };
    }
  } else if (inputs.explicitEntry) {
    return { ok: false, problems: [`views entry not found: ${entry}`] };
  } else if (inputs.cssEntry) {
    files = await bundle({ cssEntry: inputs.cssEntry }); // a tokens+css theme with no views
  }

  const views = files["views.js"];
  const cssBuf = files["theme.css"];
  if (views) {
    writeFileSync(path.join(out, "views.js"), views);
    problems.push(...checkBundleText(views.toString("utf8")).map((p) => `views.js ${p}`));
  }
  if (cssBuf) writeFileSync(path.join(out, "theme.css"), cssBuf);

  let report;
  let slots;
  if (views) {
    const bundleSha = sha256(views);
    const runTests = await loadRunner(contractRunner, cwd);
    const answer = await runTests({ bundlePath: path.join(out, "views.js"), sdkVersion: sdk.version });
    const { passed, failed } = summarize(answer);
    for (const [slot, msgs] of Object.entries(failed)) problems.push(...msgs.map((m) => `${slot}: ${m}`));
    if (!Object.keys(answer).length) problems.push("views.js declares no slot views");
    report = { sdk_version: sdk.version, bundle_sha256: bundleSha, passed };
    slots = Object.fromEntries(Object.entries(answer).map(([s, r]) => [s, `^${String(r.contract).split(".")[0]}`]));
    writeFileSync(path.join(out, "contract_report.json"), JSON.stringify({ ...report, failed }, null, 2) + "\n");
  }

  if (problems.length) return { ok: false, problems, report };

  const manifest = assembleManifest(inputs.base, {
    bundleName: views ? "views.js" : null,
    bundleSha: views ? sha256(views) : null,
    cssName: cssBuf ? "theme.css" : null,
    cssSha: cssBuf ? sha256(cssBuf) : null,
    slots,
    report,
  });
  const errs = schemaErrors(manifest);
  if (errs.length) return { ok: false, problems: errs.map((e) => `manifest ${e}`), report };
  writeFileSync(path.join(out, BASE_MANIFEST), JSON.stringify(manifest, null, 2) + "\n");
  return { ok: true, problems: [], report, manifest };
}

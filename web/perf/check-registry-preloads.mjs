#!/usr/bin/env node
// Contract C of the dashboard widget registry (src/core/dashboard/registry.ts): a widget group's views load on
// demand, so no route that pulls in the registry (the dashboard, and every SchemaForm screen through CORE_WIDGETS)
// may carry a group's view code in its static preloads. This script reads the production chunk graph
// (dist/.vite/manifest.json, and dist/.vite/chunk-modules.json from the chunk-modules plugin in vite.config.ts)
// and fails when it does.
//
//   registry-pulling route  a dynamic entry under src/pages/ whose static-import closure holds the chunk with
//                           src/core/dashboard/registry.ts;
//   group view code         a module of a lazy group folder (widgets/<group>/ with an index.ts, not one of the host
//                           groups below; nested folders included) other than index.ts and its data files (the local
//                           modules index.ts imports at runtime, type-only by contract C).
//
// Usage: node perf/check-registry-preloads.mjs [distDir]   (default: dist)
//        node perf/check-registry-preloads.mjs --selftest  (a synthetic graph with a leak must fail, a clean one pass)
// Imports node built-ins only: the perf-bundle CI job runs it without node_modules.
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const WEB = join(dirname(fileURLToPath(import.meta.url)), "..");
const WIDGETS = join(WEB, "src/core/dashboard/widgets");
const REGISTRY_TS = join(WEB, "src/core/dashboard/registry.ts");
const REGISTRY_MODULE = "src/core/dashboard/registry.ts";
/** Groups whose views ship with the host (registry.ts: excluded from the lazy views glob, imported statically). */
const HOST_GROUPS = ["legacy", "comms"];

const runtimeImports = (src) =>
  [...src.matchAll(/^\s*(?:import|export)\s+(?!type\b)(?:[^;]*?\bfrom\s+)?["']([^"']+)["']/gm)].map((m) => m[1]);

/** Files of a group folder, nested folders included, as paths relative to it (tests left out: they never ship). */
function groupFiles(dir, rel = "") {
  return readdirSync(join(dir, rel), { withFileTypes: true }).flatMap((e) => {
    const r = rel ? `${rel}/${e.name}` : e.name;
    if (e.isDirectory()) return e.name === "__tests__" ? [] : groupFiles(dir, r);
    return /\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name) ? [r] : [];
  });
}

/** The group's data files: the local modules index.ts imports at runtime (contract C keeps them type-only, the registry
 * test enforces it), as paths relative to the group folder. */
function dataFiles(dir) {
  return runtimeImports(readFileSync(join(dir, "index.ts"), "utf8"))
    .filter((spec) => spec.startsWith("./"))
    .flatMap((spec) => [`${spec}.ts`, `${spec}.tsx`, `${spec}/index.ts`].map((c) => join(dir, c)).filter((c) => existsSync(c)).slice(0, 1))
    .map((abs) => abs.slice(dir.length + 1));
}

/** Lazy groups and, per group, its view modules (paths relative to web/): every module of the group folder, nested
 * ones included, except index.ts and its data files. A module with no imports at all is still view code. */
export function viewModules(widgetsDir = WIDGETS, base = "src/core/dashboard/widgets") {
  const out = {};
  for (const group of readdirSync(widgetsDir, { withFileTypes: true })) {
    if (!group.isDirectory() || HOST_GROUPS.includes(group.name)) continue;
    const dir = join(widgetsDir, group.name);
    if (!existsSync(join(dir, "index.ts"))) continue; // not a widget group (e.g. shared hooks)
    const data = new Set(["index.ts", ...dataFiles(dir)]);
    out[group.name] = groupFiles(dir)
      .filter((f) => !data.has(f))
      .map((f) => `${base}/${group.name}/${f}`);
  }
  return out;
}

/** manifest: dist/.vite/manifest.json; modulesOf: chunk file -> its modules; groups: viewModules(). */
export function check(manifest, modulesOf, groups) {
  const closure = (key, seen = new Set()) => {
    if (seen.has(key) || !manifest[key]) return seen;
    seen.add(key);
    for (const k of manifest[key].imports ?? []) closure(k, seen);
    return seen;
  };
  const registryKeys = Object.keys(manifest).filter((k) => (modulesOf[manifest[k].file] ?? []).includes(REGISTRY_MODULE));
  if (registryKeys.length === 0) throw new Error(`no chunk holds ${REGISTRY_MODULE}: is chunk-modules.json from this build?`);
  const views = new Set(Object.values(groups).flat());
  const routes = Object.keys(manifest)
    .filter((k) => manifest[k].isDynamicEntry && k.startsWith("src/pages/"))
    .map((k) => ({ key: k, deps: closure(k) }))
    .filter((r) => registryKeys.some((k) => r.deps.has(k)));
  const leaks = routes.flatMap((r) =>
    [...r.deps].flatMap((k) => {
      const hit = (modulesOf[manifest[k].file] ?? []).filter((m) => views.has(m));
      return hit.length ? [`${r.key} -> ${manifest[k].file} (${hit.map((m) => m.replace("src/core/dashboard/widgets/", "")).join(", ")})`] : [];
    }),
  );
  return { routes: routes.map((r) => r.key), leaks };
}

function selftest() {
  const efw = "src/core/dashboard/widgets/events/event-feed-widget.tsx";
  const groups = { events: [efw, "src/core/dashboard/widgets/events/view.tsx"] };
  const graph = (leak) => ({
    manifest: {
      "_reg.js": { file: "assets/reg.js", imports: leak ? ["_efw.js"] : [] },
      "_efw.js": { file: "assets/efw.js" },
      "src/pages/extensions.tsx": { file: "assets/ext.js", isDynamicEntry: true, imports: ["_reg.js"] },
      "src/pages/event-feed.tsx": { file: "assets/ev.js", isDynamicEntry: true, imports: ["_efw.js"] },
    },
    modules: { "assets/reg.js": [REGISTRY_MODULE], "assets/efw.js": [efw], "assets/ext.js": [], "assets/ev.js": [] },
  });
  const run = (g) => check(g.manifest, g.modules, groups);
  const bad = run(graph(true));
  const good = run(graph(false));
  // A view module in a nested folder of a group is still the group's view code.
  const tmp = mkdtempSync(join(tmpdir(), "registry-preloads-"));
  let nested;
  try {
    mkdirSync(join(tmp, "grp/parts/deep"), { recursive: true });
    mkdirSync(join(tmp, "grp/__tests__"));
    writeFileSync(join(tmp, "grp/index.ts"), 'import type { X } from "../types";\nimport { SIZE } from "./size";\nexport const descriptors = [];\n');
    writeFileSync(join(tmp, "grp/parts/plain.tsx"), "export const Plain = () => null;\n"); // no imports: still a view
    writeFileSync(join(tmp, "grp/size.ts"), 'import type { Y } from "./y";\nexport const SIZE = 1;\n');
    writeFileSync(join(tmp, "grp/views.tsx"), 'import { Part } from "./parts/deep/part";\nexport const views = { a: Part };\n');
    writeFileSync(join(tmp, "grp/parts/deep/part.tsx"), 'import { useState } from "react";\nexport const Part = () => null;\n');
    writeFileSync(join(tmp, "grp/__tests__/grp.test.tsx"), 'import { views } from "../views";\n');
    nested = viewModules(tmp, "w").grp ?? [];
  } finally {
    rmSync(tmp, { recursive: true, force: true });
  }
  const nestedOk = nested.sort().join() === "w/grp/parts/deep/part.tsx,w/grp/parts/plain.tsx,w/grp/views.tsx";
  const ok =
    nestedOk &&
    bad.leaks.length === 1 && bad.leaks[0].includes("src/pages/extensions.tsx") && bad.leaks[0].includes("event-feed-widget") &&
    good.leaks.length === 0 && good.routes.join() === "src/pages/extensions.tsx";
  console.log(`check-registry-preloads selftest: ${ok ? "ok" : "FAILED"} (leaky graph: ${bad.leaks.length} leak, clean graph: ${good.leaks.length}, nested group: [${nested}])`);
  process.exit(ok ? 0 : 1);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  if (process.argv[2] === "--selftest") selftest();
  const dist = resolve(process.argv[2] ?? "dist");
  const manifest = JSON.parse(readFileSync(join(dist, ".vite/manifest.json"), "utf8"));
  // Keep HOST_GROUPS in step with the registry's lazy glob: a host group there must be a host group here.
  const reg = readFileSync(REGISTRY_TS, "utf8");
  const excluded = [...reg.matchAll(/"!\.\/widgets\/([^/]+)\/views\.tsx"/g)].map((m) => m[1]).sort();
  if (excluded.length && excluded.join() !== [...HOST_GROUPS].sort().join()) {
    console.error(`check-registry-preloads: registry.ts ships [${excluded}] with the host, this script assumes [${HOST_GROUPS}]`);
    process.exit(1);
  }
  const modulesOf = JSON.parse(readFileSync(join(dist, ".vite/chunk-modules.json"), "utf8"));
  const groups = viewModules();
  const { routes, leaks } = check(manifest, modulesOf, groups);
  console.log(`check-registry-preloads: ${routes.length} registry-pulling routes; lazy groups: ${Object.entries(groups).map(([g, m]) => `${g} (${m.length} view modules)`).join(", ")}`);
  if (leaks.length) {
    console.error(`check-registry-preloads: group view code in the static preloads of a registry-pulling route:\n  ${leaks.join("\n  ")}`);
    process.exit(1);
  }
  console.log(`check-registry-preloads: ok, no group view chunk in their static preloads (${routes.join(", ")})`);
}

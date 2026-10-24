#!/usr/bin/env node
import { parseArgs } from "node:util";
import { build } from "./build.mjs";
import { dev } from "./dev.mjs";

const USAGE = `usage:
  mesh-theme build [--entry src/index.tsx] [--css src/theme.css] [--out dist]
  mesh-theme dev --instance <url> [--port 4173] [--entry ...] [--css ...]
`;

const { positionals, values } = parseArgs({
  allowPositionals: true,
  options: {
    entry: { type: "string" },
    css: { type: "string" },
    out: { type: "string" },
    instance: { type: "string" },
    port: { type: "string" },
    "contract-runner": { type: "string" },
  },
});
const cwd = process.cwd();

try {
  if (positionals[0] === "build") {
    const r = await build({ cwd, entry: values.entry, css: values.css, outDir: values.out, contractRunner: values["contract-runner"] });
    if (!r.ok) {
      console.error("mesh-theme build FAILED, no manifest written:");
      for (const p of r.problems) console.error("  - " + p);
      process.exit(1);
    }
    const slots = Object.keys(r.report?.passed ?? {});
    console.log(`ok: ${values.out ?? "dist"}/mesh-extension.json` + (slots.length ? `, contract passed: ${slots.join(", ")}` : ""));
  } else if (positionals[0] === "dev") {
    if (!values.instance) throw new Error("--instance <url> is required");
    await dev({ cwd, entry: values.entry, css: values.css, instance: values.instance, port: values.port ? Number(values.port) : undefined });
  } else {
    console.error(USAGE);
    process.exit(2);
  }
} catch (e) {
  console.error("mesh-theme: " + e.message);
  process.exit(2);
}

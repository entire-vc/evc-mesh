// mesh-default is an ordinary theme: what a view takes from the core's runtime comes through the SDK, by name, as it would
// in a bundle. A view that reaches into core primitives, hooks, stores or the API client directly would not survive being
// lifted out of the app. (Styling building blocks from @/components stay; the SDK does not ship a UI kit.)
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";

const SRC = resolve(__dirname, "../../..");
const FORBIDDEN = [/^@\/core\/primitives/, /^@\/core\/dashboard\/hooks/, /^@\/core\/themes/, /^@\/stores\//, /^@\/lib\/api$/];

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n);
    if (n === "__tests__") return [];
    return statSync(p).isDirectory() ? files(p) : /\.tsx?$/.test(n) ? [p] : [];
  });
}

const views = [...files(join(SRC, "themes/mesh-default")), join(SRC, "core/dashboard/views/mesh-default.tsx")];

describe("mesh-default views take the core's runtime only through the SDK", () => {
  it("covers the board, list, task, doc and dashboard views", () => {
    expect(views.length).toBeGreaterThan(8);
  });
  it.each(views.map((f) => [f.slice(SRC.length + 1), f]))("%s", (_name, file) => {
    const specs = [...readFileSync(file, "utf8").matchAll(/^\s*(?:import|export)\s[^;]*?from\s+["']([^"']+)["']/gms)].map((m) => m[1]!);
    expect(specs.filter((s) => FORBIDDEN.some((re) => re.test(s)))).toEqual([]);
  });
  it("the guard fires on a known-bad import", () => {
    const bad = `import { TaskLink } from "@/core/primitives";`;
    const specs = [...bad.matchAll(/^\s*(?:import|export)\s[^;]*?from\s+["']([^"']+)["']/gms)].map((m) => m[1]!);
    expect(specs.filter((s) => FORBIDDEN.some((re) => re.test(s)))).toEqual(["@/core/primitives"]);
  });
});

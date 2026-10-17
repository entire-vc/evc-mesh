import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

const SRC = join(__dirname, "..", "..");

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      if (name === "__tests__" || name === "node_modules") continue;
      walk(p, out);
    } else if (/\.tsx$/.test(name) && !/\.test\.tsx$/.test(name)) {
      out.push(p);
    }
  }
  return out;
}

describe("password managers", () => {
  it('every type="password" input declares autoComplete', () => {
    const offenders: string[] = [];
    for (const file of walk(SRC)) {
      const lines = readFileSync(file, "utf8").split("\n");
      lines.forEach((line, i) => {
        if (!/type="password"/.test(line)) return;
        // the attributes of one element sit within a few lines of type=
        const window = lines.slice(Math.max(0, i - 8), i + 10).join("\n");
        if (!/autoComplete=|SECRET_INPUT_PROPS/.test(window)) {
          offenders.push(`${relative(SRC, file)}:${i + 1}`);
        }
      });
    }
    expect(offenders).toEqual([]);
  });

  it("header task search is not mistaken for a login field", () => {
    const src = readFileSync(
      join(SRC, "components/layout/header.tsx"),
      "utf8",
    );
    expect(src).toContain('type="search"');
    expect(src).toContain('name="mesh-task-search"');
    expect(src).toContain("data-1p-ignore");
    expect(src).toContain("data-bwignore");
  });
});

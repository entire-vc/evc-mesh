import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import en from "@/locales/en/misc.json";

// Code that renders before the lazy `misc` chunk has loaded (login, register,
// the API client, the error boundary) passes an English default next to the
// key. That default is what the user sees until the chunk arrives, so it must
// equal locales/en/misc.json — otherwise the text would change on load.
const FILES = [
  "src/pages/login.tsx",
  "src/pages/register.tsx",
  "src/App.tsx",
  "src/lib/api.ts",
  "src/lib/api-error.ts",
  "src/lib/board-load.ts",
  "src/hooks/use-api.ts",
  "src/stores/member.ts",
  "src/stores/project.ts",
  "src/stores/task.ts",
  "src/stores/workspace.ts",
];
const CALL = /\b(?:miscT|t)\(\s*"([\w.]+)",\s*"((?:[^"\\]|\\.)*)"/g;

function lookup(key: string): unknown {
  return key.split(".").reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], en);
}

describe("English defaults next to misc keys", () => {
  const calls = FILES.flatMap((f) => {
    const src = fs.readFileSync(path.resolve(__dirname, "../../..", f), "utf8");
    return [...src.matchAll(CALL)].map((m) => ({ file: f, key: m[1]!, text: JSON.parse(`"${m[2]}"`) as string }));
  });

  it("finds the calls (positive control)", () => {
    expect(calls.length).toBeGreaterThan(30);
    expect(calls.some((c) => c.key === "auth.login.title")).toBe(true);
  });

  it("match locales/en/misc.json", () => {
    const mismatched = calls.filter((c) => lookup(c.key) !== c.text);
    expect(mismatched).toEqual([]);
  });
});

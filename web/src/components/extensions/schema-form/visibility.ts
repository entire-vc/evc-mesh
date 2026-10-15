import type { FieldSchema, Values, VisibleIf, VisibleIfExpected } from "./types";

function same(a: unknown, b: unknown): boolean {
  return a === b || (a == null && b == null);
}

function matches(actual: unknown, expected: VisibleIfExpected): boolean {
  if (Array.isArray(expected)) return expected.some((e) => same(actual, e));
  if (expected !== null && typeof expected === "object") {
    const e = expected as { not?: unknown; in?: unknown[]; exists?: boolean };
    if ("exists" in e && Boolean(e.exists) !== (actual != null && actual !== "")) return false;
    if ("not" in e && same(actual, e.not)) return false;
    if (Array.isArray(e.in) && !e.in.some((x) => same(actual, x))) return false;
    return true;
  }
  return same(actual, expected);
}

/** All conditions hold against the sibling values of the same object. */
export function evalVisibleIf(cond: VisibleIf | undefined, siblings: Values): boolean {
  if (!cond) return true;
  return Object.entries(cond).every(([name, expected]) => matches(siblings[name], expected));
}

export function isVisible(field: FieldSchema, siblings: Values): boolean {
  return evalVisibleIf(field["x-visible-if"], siblings);
}

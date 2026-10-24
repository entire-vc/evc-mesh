import { describe, expect, it } from "vitest";
import { dueLabel, text } from "../src/fields";

describe("fields", () => {
  it("joins lists and drops empty values", () => {
    expect(text(["a", "b"])).toBe("a, b");
    expect(text("")).toBe("");
    expect(text(null)).toBe("");
  });

  it("marks an overdue date", () => {
    expect(dueLabel("2026-10-01T00:00:00Z", true)).toBe("Overdue: 2026-10-01");
    expect(dueLabel("2026-10-01T00:00:00Z", false)).toBe("Due: 2026-10-01");
    expect(dueLabel(null, false)).toBe("");
  });
});

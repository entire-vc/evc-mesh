import { describe, expect, it } from "vitest";
import { resolveSlotTab } from "@/lib/extension-slots";

describe("resolveSlotTab", () => {
  it("keeps a built-in tab", () => {
    expect(resolveSlotTab("activity", [], "comments")).toBe("activity");
  });
  it("keeps a slot tab while its extension is registered", () => {
    expect(resolveSlotTab("slot:gitlab", ["gitlab"], "comments")).toBe("slot:gitlab");
  });
  it("falls back when the selected extension leaves the registry", () => {
    expect(resolveSlotTab("slot:gitlab", [], "comments")).toBe("comments");
    expect(resolveSlotTab("slot:gitlab", ["other"], "details")).toBe("details");
  });
});

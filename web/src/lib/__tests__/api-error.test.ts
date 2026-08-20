import { describe, expect, it } from "vitest";
import { apiErrorMessage } from "@/lib/api-error";

describe("apiErrorMessage", () => {
  it("prefers the field detail over the generic message", () => {
    const err = {
      message: "Validation failed",
      details: { body: "no workspace member or agent named @ghost is known in this workspace" },
    };
    expect(apiErrorMessage(err)).toContain("@ghost");
  });

  it("falls back to the message when there are no details", () => {
    expect(apiErrorMessage({ message: "Forbidden" })).toBe("Forbidden");
  });

  it("skips an empty detail rather than showing a blank error", () => {
    expect(apiErrorMessage({ message: "Validation failed", details: { body: "  " } }))
      .toBe("Validation failed");
  });

  it("falls back for something that is not an error at all", () => {
    expect(apiErrorMessage(null)).toBe("Failed to save");
    expect(apiErrorMessage(undefined, "nope")).toBe("nope");
  });
});

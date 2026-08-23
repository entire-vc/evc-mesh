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

  // §1r.A regression: a bare `.message` on an API error is unreviewed server
  // prose (api.ts builds it from `err.message || err.error || "Request
  // failed"`), not copy anyone approved. Once there's no structured `details`
  // to show instead, the caller's own approved fallback must win — never the
  // server's string. Shaped like the real ApiRequestError (`code` + `status`,
  // both required by the duck-type check) without `details`, matching the
  // PR #684 finding (`new Error("boom")` rendering literally as "boom").
  it("never shows a bare server message — falls back when there is no field detail", () => {
    const err = { message: "boom", code: "SERVER_ERROR", status: 500 };
    expect(apiErrorMessage(err, "Could not store the secret")).toBe("Could not store the secret");
  });

  it("still shows a genuine client-side Error's message (not server-sourced)", () => {
    expect(apiErrorMessage(new Error("File is too large"), "Upload failed")).toBe(
      "File is too large",
    );
  });
});

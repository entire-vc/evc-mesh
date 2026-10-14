import { afterEach, describe, expect, it } from "vitest";
import i18n from "i18next";
import "@/i18n";
import { apiErrorMessage } from "@/lib/api-error";
import { serverErrorCode } from "@/lib/api";

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

describe("apiErrorMessage by error code", () => {
  const forbidden = { errorCode: "forbidden", code: "403", status: 403, message: "Forbidden" };
  const validation = {
    errorCode: "validation_failed",
    code: "400",
    status: 400,
    message: "Validation failed",
    details: { title: "title is required" },
  };
  const conflict = { errorCode: "cas_conflict", code: "cas_conflict", status: 409, message: "Task was modified concurrently" };

  afterEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("shows the three typical errors in the user's language", async () => {
    await i18n.changeLanguage("ru");
    expect(apiErrorMessage(validation)).toBe("Проверка не пройдена. Проверьте введённые данные: title is required");
    expect(apiErrorMessage(forbidden)).toBe("Недостаточно прав для этого действия.");
    expect(apiErrorMessage(conflict)).toBe("Задачу уже изменил кто-то другой. Обновите её и повторите.");
    await i18n.changeLanguage("en");
    expect(apiErrorMessage(forbidden)).toBe("You don't have permission to do this.");
    expect(apiErrorMessage(conflict)).toBe("The task was changed by someone else. Reload it and try again.");
  });

  it("a known code with field details appends the first detail; without details only the translation", async () => {
    await i18n.changeLanguage("en");
    expect(apiErrorMessage(validation)).toBe("Validation failed. Check the entered data: title is required");
    const noDetails = { ...validation, details: undefined };
    expect(apiErrorMessage(noDetails)).toBe("Validation failed. Check the entered data.");
    const blankDetails = { ...validation, details: { title: "  ", n: 3 } };
    expect(apiErrorMessage(blankDetails)).toBe("Validation failed. Check the entered data.");
    // field keys are never shown, only the first non-empty string message
    const two = { ...validation, details: { a: "", b: "second field message", c: "third" } };
    expect(apiErrorMessage(two)).toBe("Validation failed. Check the entered data: second field message");
  });

  it("an unknown code falls back to the server's English message", async () => {
    await i18n.changeLanguage("ru");
    const unknown = { errorCode: "brand_new_code", code: "brand_new_code", status: 422, message: "Some English prose" };
    expect(apiErrorMessage(unknown, "Не удалось сохранить")).toBe("Some English prose");
    const uncoded = { code: "x", status: 500, message: "raw" };
    expect(apiErrorMessage(uncoded, "Не удалось сохранить")).toBe("Не удалось сохранить");
  });

  it("an unknown code with field details still shows the server's English message", async () => {
    const unknown = {
      errorCode: "brand_new_code",
      code: "brand_new_code",
      status: 422,
      message: "Some English prose",
      details: { title: "title is required" },
    };
    expect(apiErrorMessage(unknown, "Не удалось сохранить")).toBe("Some English prose");
  });

  it("serverErrorCode reads error_code, a string code, or a snake_case error key", () => {
    expect(serverErrorCode({ code: 403, error_code: "forbidden" })).toBe("forbidden");
    expect(serverErrorCode({ code: "cas_conflict" })).toBe("cas_conflict");
    expect(serverErrorCode({ code: 410, error: "list_revision_stale" })).toBe("list_revision_stale");
    expect(serverErrorCode({ code: 400, error: "Something human readable" })).toBeUndefined();
    expect(serverErrorCode({ code: 500 })).toBeUndefined();
  });
});

import { describe, expect, it } from "vitest";
import { isSupportedLocale, suggestedLocale } from "@/lib/locales";

describe("suggestedLocale", () => {
  it("offers ru to a never-chosen en account on a Russian browser", () => {
    expect(suggestedLocale("en", false, "ru-RU")).toBe("ru");
    expect(suggestedLocale("en", undefined, "ru")).toBe("ru");
  });
  it("stays silent once the language was chosen explicitly", () => {
    expect(suggestedLocale("en", true, "ru-RU")).toBeNull();
  });
  it("stays silent for unsupported or matching browser languages", () => {
    expect(suggestedLocale("en", false, "de-DE")).toBeNull();
    expect(suggestedLocale("en", false, "en-US")).toBeNull();
    expect(suggestedLocale("ru", false, "ru-RU")).toBeNull();
    expect(suggestedLocale("en", false, undefined)).toBeNull();
  });
  it("validates codes", () => {
    expect(isSupportedLocale("ru")).toBe(true);
    expect(isSupportedLocale("de")).toBe(false);
  });
});

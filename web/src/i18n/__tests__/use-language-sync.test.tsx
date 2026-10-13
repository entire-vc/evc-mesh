import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import i18n, { setLanguage } from "@/i18n";
import { useAuthStore } from "@/stores/auth";
import { useLanguageSync } from "@/i18n/use-language-sync";

afterEach(async () => {
  vi.restoreAllMocks();
  useAuthStore.setState({ user: null });
  await setLanguage("en");
});

it("falls back to the browser language after logout instead of keeping the account language", async () => {
  vi.spyOn(navigator, "languages", "get").mockReturnValue(["en-US"]);
  useAuthStore.setState({ user: { locale: "ru" } as never });
  renderHook(() => useLanguageSync());
  await act(async () => {});
  expect(i18n.language).toBe("ru");

  await act(async () => {
    useAuthStore.setState({ user: null });
  });
  expect(i18n.language).toBe("en");
});

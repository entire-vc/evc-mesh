import { useEffect } from "react";
import { useAuthStore } from "@/stores/auth";
import { resolveLanguage } from "./config";
import { browserLanguages, setLanguage } from "./index";

/**
 * Keeps the UI language equal to users.locale once the person is known; with
 * no user (before login, after logout) it is the browser language. Switching is live: no
 * reload.
 */
export function useLanguageSync(): void {
  const locale = useAuthStore((s) => s.user?.locale);
  useEffect(() => {
    // No locale (before login, or after logout) falls back to the browser
    // language, so a previous account's language does not stick.
    void setLanguage(resolveLanguage(locale, browserLanguages()));
  }, [locale]);
}

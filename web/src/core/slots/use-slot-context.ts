import { useTranslation } from "react-i18next";
import type { SlotContext } from "./types";

const MOBILE_QUERY = "(max-width: 767px)";

// The core decides light/dark and mobile; a view never measures the window.
export function useSlotContext(slot: string, workspace: SlotContext["workspace"]): SlotContext {
  const { i18n } = useTranslation();
  return {
    slot,
    contractVersion: "1.0.0",
    locale: i18n.language,
    mode: document.documentElement.classList.contains("dark") ? "dark" : "light",
    isMobile: typeof window.matchMedia === "function" && window.matchMedia(MOBILE_QUERY).matches,
    workspace,
  };
}

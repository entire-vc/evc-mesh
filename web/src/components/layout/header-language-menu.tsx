import { Check, Globe } from "lucide-react";
import { useTranslation } from "react-i18next";
import { DropdownMenuItem, DropdownMenuSeparator } from "@/components/ui/dropdown-menu";
import { SUPPORTED_LOCALES, DEFAULT_LOCALE } from "@/lib/locales";
import { useAuthStore } from "@/stores/auth";

// Language block of the user menu. Same store action as the profile selector
// (components/language-select.tsx); language names reuse the common-namespace
// keys, the label is in the lazy `shell` namespace.
export default function HeaderLanguageMenu() {
  const { t } = useTranslation("shell");
  const { user, setLocale } = useAuthStore();
  const current = user?.locale ?? DEFAULT_LOCALE;

  return (
    <>
      <div className="flex items-center px-2 pt-1.5 pb-0.5 text-xs text-muted-foreground">
        <Globe className="mr-2 h-3.5 w-3.5" />
        {t("header.userMenu.language")}
      </div>
      {SUPPORTED_LOCALES.map((l) => {
        const active = current === l.code;
        return (
          <DropdownMenuItem
            key={l.code}
            aria-current={active ? "true" : undefined}
            onClick={() => void setLocale(l.code).catch(() => {})}
          >
            <span className="mr-2 inline-block w-4">
              {active && <Check className="h-4 w-4" />}
            </span>
            {t(`language.${l.code}`, { ns: "common" })}
          </DropdownMenuItem>
        );
      })}
      <DropdownMenuSeparator />
    </>
  );
}

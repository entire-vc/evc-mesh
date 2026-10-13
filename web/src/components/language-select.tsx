import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Check, AlertTriangle } from "lucide-react";
import { Select } from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { useAuthStore } from "@/stores/auth";
import { apiErrorMessage } from "@/lib/api-error";
import { DEFAULT_LOCALE, SUPPORTED_LOCALES, suggestedLocale } from "@/lib/locales";

// Language picker for the profile. Saved on change (no reload); the choice is
// stored on the account so another device or a new session gets the same one.
export function LanguageSelect() {
  const { t } = useTranslation();
  const { user, setLocale } = useAuthStore();
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const current = user?.locale ?? DEFAULT_LOCALE;
  const suggestion = suggestedLocale(
    user?.locale,
    user?.locale_set_explicitly,
    typeof navigator === "undefined" ? undefined : navigator.language,
  );

  const apply = async (code: string) => {
    setError(null);
    setSaved(false);
    try {
      await setLocale(code);
      setSaved(true);
    } catch (err) {
      setError(apiErrorMessage(err, "Failed to save language"));
    }
  };

  return (
    <div className="space-y-1.5">
      <label htmlFor="profile-language" className="text-sm font-medium">
        {t("language.label")}
      </label>
      <Select
        id="profile-language"
        value={current}
        onChange={(e) => void apply(e.target.value)}
      >
        {SUPPORTED_LOCALES.map((l) => (
          <option key={l.code} value={l.code}>
            {t(`language.${l.code}`)}
          </option>
        ))}
      </Select>
      {suggestion && (
        <p className="text-xs text-muted-foreground flex items-center gap-2">
          {t("language.suggestRu")}
          <Button type="button" size="sm" variant="outline" onClick={() => void apply(suggestion)}>
            {t(`language.${suggestion}`)}
          </Button>
        </p>
      )}
      {saved && (
        <p className="text-xs text-green-600 flex items-center gap-1">
          <Check className="h-3 w-3" /> Saved
        </p>
      )}
      {error && (
        <p className="text-xs text-destructive flex items-center gap-1">
          <AlertTriangle className="h-3 w-3" /> {error}
        </p>
      )}
    </div>
  );
}

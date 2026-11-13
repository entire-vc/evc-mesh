import { useTranslation } from "react-i18next";
import { cn } from "@/lib/cn";
import { SCOPES, type NotificationScope } from "./constants";

interface ScopeSelectProps {
  /** Channel name, keeps the radio groups of different tabs apart. */
  channel: string;
  value: NotificationScope;
  onChange: (scope: NotificationScope) => void;
}

/** Which events of the workspace reach this channel: all / mine / mentions. */
export function ScopeSelect({ channel, value, onChange }: ScopeSelectProps) {
  const { t } = useTranslation("notifications");
  const titleId = `scope-title-${channel}`;
  return (
    <div className="space-y-2 rounded-lg border border-border p-3">
      <p id={titleId} className="font-medium">{t("settings.scope.title")}</p>
      <div role="radiogroup" aria-labelledby={titleId} className="space-y-1.5">
        {SCOPES.map((scope) => {
          const checked = value === scope;
          return (
            <button
              key={scope}
              type="button"
              role="radio"
              aria-checked={checked}
              onClick={() => onChange(scope)}
              className={cn(
                "flex w-full items-start gap-3 rounded-md border p-2.5 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                checked ? "border-primary bg-accent/40" : "border-border hover:bg-accent/30",
              )}
            >
              <span
                aria-hidden="true"
                className={cn(
                  "mt-1 h-3.5 w-3.5 shrink-0 rounded-full border-2",
                  checked ? "border-primary bg-primary" : "border-muted-foreground/50",
                )}
              />
              <span className="min-w-0">
                <span className="block text-sm font-medium">{t(`settings.scope.${scope}.label`)}</span>
                <span className="block text-sm text-muted-foreground">{t(`settings.scope.${scope}.description`)}</span>
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

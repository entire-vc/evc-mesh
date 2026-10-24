import { Check } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/cn";
import { Badge } from "@/components/ui/badge";
import { ThemePreview } from "./theme-preview";

/** One theme as a radio card: preview on top, name and version below; actions of the instance page sit in `actions`. */
export function ThemeCard({
  name,
  description,
  version,
  tokens,
  selected,
  disabled,
  onSelect,
  badge,
  actions,
}: {
  name: string;
  description?: string;
  version?: string;
  tokens?: React.ComponentProps<typeof ThemePreview>["tokens"];
  selected?: boolean;
  disabled?: boolean;
  /** Absent = the card is not a choice (the installed-themes list). */
  onSelect?: () => void;
  badge?: string;
  actions?: React.ReactNode;
}) {
  const { t } = useTranslation("settings");
  const body = (
    <>
      <ThemePreview tokens={tokens} decorative />
      <div className="flex items-start justify-between gap-2 px-1 pt-2.5">
        <div className="min-w-0">
          <p className="truncate text-sm font-medium">{name}</p>
          {description && <p className="line-clamp-2 text-xs text-muted-foreground">{description}</p>}
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          {version && <span className="text-xs text-muted-foreground">v{version}</span>}
          {badge && <Badge variant="secondary">{badge}</Badge>}
          {selected && <Check className="h-4 w-4 text-primary" aria-label={t("themes.picker.selected")} />}
        </div>
      </div>
    </>
  );
  return (
    <div className={cn("flex flex-col rounded-xl border bg-card p-2 transition-colors", selected ? "border-primary ring-1 ring-primary" : "border-border")}>
      {onSelect ? (
        <button
          type="button"
          role="radio"
          aria-checked={Boolean(selected)}
          aria-label={name}
          disabled={disabled}
          onClick={onSelect}
          className="rounded-lg text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default"
        >
          {body}
        </button>
      ) : (
        <div>{body}</div>
      )}
      {actions && <div className="mt-2 flex flex-wrap gap-2 px-1 pb-1">{actions}</div>}
    </div>
  );
}

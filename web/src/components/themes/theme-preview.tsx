import { useMemo, useSyncExternalStore, type CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/cn";
import type { FieldSchema, Values } from "@/components/extensions/schema-form";
import { BUILTIN_FIELDS, PICKER_FIELD_LABEL_KEYS } from "@/core/slots/board-data";
import type { ThemeInfo } from "@/lib/themes";

/** The mini card is fixed-height: more fields than this collapse into a "+N" row. */
const MAX_PREVIEW_FIELDS = 3;

type Tokens = Record<string, string>;

/** Token overrides the settings produce: every field bound with `x-token` whose value is set. */
export function settingsTokenOverrides(schemas: Array<FieldSchema | undefined>, values: Values): Tokens {
  const out: Tokens = {};
  for (const s of schemas) {
    for (const [name, f] of Object.entries(s?.properties ?? {})) {
      const tok = f["x-token"];
      const v = values[name];
      if (tok && typeof v === "string" && v.trim() !== "") out[tok] = v.trim();
    }
  }
  return out;
}

export function isDarkNow(): boolean {
  return typeof document !== "undefined" && document.documentElement.classList.contains("dark");
}

// Every mode change (choice, OS follow, a theme that forces one) ends as the `dark` class on <html>: watch that.
function subscribeDark(onChange: () => void): () => void {
  const obs = new MutationObserver(onChange);
  obs.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
  return () => obs.disconnect();
}

/** isDarkNow() that re-renders the caller when the mode changes. */
export function useDarkNow(): boolean {
  return useSyncExternalStore(subscribeDark, isDarkNow, () => false);
}

/** Mini board in the theme's tokens: what changes first when a colour or font is edited. */
export function ThemePreview({
  tokens,
  overrides,
  cardFields,
  dark,
  decorative,
  className,
}: {
  /** Tokens of the theme per mode; an empty map previews the app's own tokens. */
  tokens?: { light?: Tokens; dark?: Tokens };
  overrides?: Tokens;
  /** Card fields picked in the settings form, in order; empty or undefined keeps the generic sample card. */
  cardFields?: string[];
  dark?: boolean;
  /** Card thumbnails repeat the card's own name: hidden from assistive tech. */
  decorative?: boolean;
  className?: string;
}) {
  const { t } = useTranslation("settings");
  const { t: tb } = useTranslation("board");
  const mode = dark ?? isDarkNow();
  const vars = useMemo(() => {
    const own = (mode ? (tokens?.dark ?? tokens?.light) : (tokens?.light ?? tokens?.dark)) ?? {};
    const merged = { ...own, ...overrides };
    const style: Record<string, string> = {};
    for (const [k, v] of Object.entries(merged)) style[`--${k}`] = v;
    return style as CSSProperties;
  }, [tokens, overrides, mode]);

  const fieldLabels = cardFields?.length
    ? cardFields.map((k) => {
        const labelKey = BUILTIN_FIELDS.find((f) => f.key === k)?.labelKey ?? PICKER_FIELD_LABEL_KEYS[k];
        return labelKey ? tb(labelKey) : k.replace(/^cf:/, "");
      })
    : undefined;
  const shownFields = fieldLabels && [
    ...fieldLabels.slice(0, MAX_PREVIEW_FIELDS),
    ...(fieldLabels.length > MAX_PREVIEW_FIELDS ? [`+${fieldLabels.length - MAX_PREVIEW_FIELDS}`] : []),
  ];

  return (
    <div
      role={decorative ? undefined : "img"}
      aria-label={decorative ? undefined : t("themes.preview.label")}
      aria-hidden={decorative || undefined}
      style={{ ...vars, fontFamily: "var(--font-sans, inherit)", borderColor: "hsl(var(--border))" }}
      className={cn("pointer-events-none flex h-44 select-none overflow-hidden rounded-lg border text-[10px] leading-tight", mode && "dark", className)}
    >
      <div className="flex w-1/4 flex-col gap-1.5 border-r p-2" style={{ background: "hsl(var(--sidebar-background, var(--muted)))", borderColor: "hsl(var(--sidebar-border, var(--border)))", color: "hsl(var(--sidebar-foreground, var(--foreground)))" }}>
        <div className="h-2.5 w-3/4 rounded" style={{ background: "hsl(var(--sidebar-primary, var(--primary)))" }} />
        <div className="h-2 w-full rounded" style={{ background: "hsl(var(--sidebar-accent, var(--accent)))" }} />
        <div className="h-2 w-2/3 rounded opacity-60" style={{ background: "hsl(var(--sidebar-foreground, var(--foreground)))" }} />
      </div>
      <div className="flex min-w-0 flex-1 flex-col gap-2 p-2" style={{ background: "hsl(var(--background))", color: "hsl(var(--foreground))" }}>
        <div className="flex items-center justify-between">
          <span className="font-semibold">{t("themes.preview.board")}</span>
          <span className="rounded px-1.5 py-0.5" style={{ background: "hsl(var(--primary))", color: "hsl(var(--primary-foreground))" }}>
            {t("themes.preview.button")}
          </span>
        </div>
        <div className="grid flex-1 grid-cols-2 gap-1.5">
          {[
            ["status-todo", "priority-high"],
            ["status-in-progress", "priority-medium"],
          ].map(([status, priority], i) => (
            <div key={status} className="flex flex-col gap-1 rounded-md border p-1.5" style={{ background: "hsl(var(--card))", color: "hsl(var(--card-foreground))", borderColor: "hsl(var(--border))" }}>
              <span className="truncate font-medium">{t(i === 0 ? "themes.preview.card1" : "themes.preview.card2")}</span>
              {shownFields ? (
                <span data-testid="theme-preview-fields" className="flex flex-col gap-0.5">
                  {shownFields.map((l, n) => (
                    <span key={`${l}-${n}`} className="truncate" style={{ color: "hsl(var(--muted-foreground))" }}>
                      {l}
                    </span>
                  ))}
                </span>
              ) : (
                <>
                  <span className="flex gap-1">
                    <i className="h-1.5 w-1.5 rounded-full" style={{ background: `hsl(var(--${status}))` }} />
                    <i className="h-1.5 w-3 rounded-full" style={{ background: `hsl(var(--${priority}))` }} />
                  </span>
                  <span style={{ color: "hsl(var(--muted-foreground))" }}>{t("themes.preview.meta")}</span>
                </>
              )}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

export function themePreviewTokens(theme: ThemeInfo | undefined) {
  return theme?.tokens;
}

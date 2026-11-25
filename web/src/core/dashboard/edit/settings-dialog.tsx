import { useId, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { SchemaForm, type FieldSchema, type Values } from "@/components/extensions/schema-form";
import type { CatalogType, LayoutItem } from "./types";
import { allowedDimensions, sanitize, useCapabilities } from "@/core/agent-usage/capabilities";
import { useST, tokenKindLabel } from "@/core/agent-usage/format";
import { dimensionLabel, viewLabel, windowLabel } from "@/core/agent-usage/labels";
import type { QuotaWindow, TokenKind, UsageDimension } from "@/core/agent-usage/types";
import { DIMENSIONS, isUserUsage, stateFromSettings, stateToSettings, type StatsView } from "@/core/agent-usage/state";

type Translate = (key: string, opts: { defaultValue: string }) => string;

/** Labelled choices (`oneOf` const + title) in the user's language: a choice with `x-title-key` is translated here. */
export function localizeChoices(f: FieldSchema, t: Translate): FieldSchema {
  let out = f;
  if (Array.isArray(f.oneOf) && f.oneOf.some((o) => o && typeof o === "object" && o["x-title-key"])) {
    out = { ...out, oneOf: f.oneOf.map((o) => (o?.["x-title-key"] ? { ...o, title: t(o["x-title-key"], { defaultValue: o.title ?? String(o.const) }) } : o)) };
  }
  if (out.items) out = { ...out, items: localizeChoices(out.items, t) };
  if (out.properties) out = { ...out, properties: Object.fromEntries(Object.entries(out.properties).map(([k, v]) => [k, localizeChoices(v, t)])) };
  return out;
}

interface Props {
  item: LayoutItem | null;
  type: CatalogType | undefined;
  onClose: () => void;
  onSave: (patch: Pick<LayoutItem, "title" | "auto_refresh" | "settings">) => void;
}

/** Instance settings (§5.6): the type's schema through SchemaForm plus the two common fields. */
export function SettingsDialog({ item, type, onClose, onSave }: Props) {
  const { t } = useTranslation("dashboardEdit");
  const caps = useCapabilities(item?.type === "agent-usage" && isUserUsage(stateFromSettings(item.settings)) ? "user" : "workspace");
  const st = useST();
  const formId = useId();
  const schema = useMemo<FieldSchema>(() => {
    const own = type?.settings_schema?.properties ?? {};
    const { title: _a, auto_refresh: _b, ...rest } = own;
    if (item?.type === "agent-usage") {
      // The settings editor follows the same permissions as the page, including before discovery completes.
      const views = (rest.m?.enum ?? []).filter((v) => v !== "quota" || caps?.quota === true).filter((v) => v !== "cost" || caps?.cost !== false);
      rest.m = { ...rest.m, enum: views };
      const metric = (item.settings?.m ?? "cost") as StatsView;
      const dims = allowedDimensions(caps, metric);
      for (const key of ["g", "s"]) rest[key] = { ...rest[key], enum: dims };
      if (!caps?.quota) delete rest.w;
      const titles = { m: st("ctl.show", "Show"), g: st("ctl.group", "Breakdown"), s: st("ctl.stack", "Inside"), t: st("ctl.tokens", "Tokens"), p: st("period.label", "Period"), w: st("ctl.window", "Limit window"), n: st("settings.rows", "Rows") };
      for (const [key, title] of Object.entries(titles)) if (rest[key]) rest[key] = { ...rest[key], title };
      for (const key of ["m", "g", "s", "t", "w"]) {
        if (!rest[key]) continue;
        rest[key] = { ...rest[key], "x-options": Object.fromEntries((rest[key].enum ?? []).map((v) => [v,
          key === "m" ? viewLabel(v as StatsView, st) : key === "w" ? windowLabel(v as QuotaWindow, st)
            : key === "t" ? tokenKindLabel(v as TokenKind, st) : dimensionLabel(v as UsageDimension, st),
        ])) };
      }
      rest.p = { ...rest.p, description: st("settings.periodHint", "1d, 7d, 30d or YYYY-MM-DD..YYYY-MM-DD") };
    }
    return {
      type: "object",
      properties: {
        title: { type: "string", title: t("settingsForm.title", { defaultValue: "Title" }), maxLength: 60, description: t("settingsForm.titleHint", { defaultValue: "Replaces the widget name" }) },
        auto_refresh: { type: "boolean", title: t("settingsForm.autoRefresh", { defaultValue: "Refresh automatically" }), default: true },
        ...localizeChoices({ properties: rest }, t).properties,
      },
      required: type?.settings_schema?.required,
    };
  }, [type, item, caps, t, st]);

  const value = useMemo<Values>(
    () => ({ title: item?.title ?? "", auto_refresh: item?.auto_refresh ?? true, ...(item?.type === "agent-usage"
      ? stateToSettings(sanitize(stateFromSettings(item.settings), caps ?? { group_by: DIMENSIONS.filter((d) => d !== "account"), cost: true, quota: false }))
      : item?.settings ?? {}) }),
    [item, caps],
  );

  return (
    <Dialog open={!!item} onOpenChange={(o) => !o && onClose()} closeOnBackdrop>
      {item && (
        // Capped to the window: the form scrolls between the header and the footer, so the
        // title and Cancel / Save stay in view on a 768 px laptop and on a phone.
        <DialogContent className="flex max-h-[85dvh] flex-col">
          <DialogHeader>
            <DialogTitle>{t("settingsTitle", { defaultValue: "Widget settings" })}</DialogTitle>
            <DialogDescription>{type?.title ?? item.type}</DialogDescription>
          </DialogHeader>
          <div className="-mx-6 mt-4 min-h-0 flex-1 overflow-y-auto px-6 pb-1" data-testid="widget-settings-body">
            <SchemaForm
              key={item.id}
              formId={formId}
              schema={schema}
              value={value}
              hideSubmit
              onSubmit={async (v) => {
                const { title, auto_refresh, ...settings } = v as Record<string, unknown>;
                onSave({
                  title: typeof title === "string" && title.trim() ? title.trim() : undefined,
                  auto_refresh: typeof auto_refresh === "boolean" ? auto_refresh : undefined,
                  settings: item.type === "agent-usage"
                    ? stateToSettings(sanitize(stateFromSettings({ ...value, ...settings }), caps ?? { group_by: DIMENSIONS.filter((d) => d !== "account"), cost: true, quota: false })) as Values
                    : Object.keys(settings).length ? (settings as Values) : undefined,
                });
                onClose();
              }}
            />
          </div>
          <DialogFooter className="-mx-6 mt-0 flex-row justify-end gap-2 border-t border-border px-6 pt-4 sm:space-x-0">
            <Button type="button" variant="outline" onClick={onClose}>{t("cancel", { defaultValue: "Cancel" })}</Button>
            <Button type="submit" form={formId}>{t("save", { defaultValue: "Save" })}</Button>
          </DialogFooter>
        </DialogContent>
      )}
    </Dialog>
  );
}

import { useId, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { SchemaForm, type FieldSchema, type Values } from "@/components/extensions/schema-form";
import type { CatalogType, LayoutItem } from "./types";

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
  const formId = useId();
  const schema = useMemo<FieldSchema>(() => {
    const own = type?.settings_schema?.properties ?? {};
    const { title: _a, auto_refresh: _b, ...rest } = own;
    return {
      type: "object",
      properties: {
        title: { type: "string", title: t("settingsForm.title", { defaultValue: "Title" }), maxLength: 60, description: t("settingsForm.titleHint", { defaultValue: "Replaces the widget name" }) },
        auto_refresh: { type: "boolean", title: t("settingsForm.autoRefresh", { defaultValue: "Refresh automatically" }), default: true },
        ...localizeChoices({ properties: rest }, t).properties,
      },
      required: type?.settings_schema?.required,
    };
  }, [type, t]);

  const value = useMemo<Values>(
    () => ({ title: item?.title ?? "", auto_refresh: item?.auto_refresh ?? true, ...(item?.settings ?? {}) }),
    [item],
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
                  settings: Object.keys(settings).length ? (settings as Values) : undefined,
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

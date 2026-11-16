// Edit mode of a dashboard (PRD §5.5-§5.7). Loaded lazily, only when the gear is pressed.
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus, RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast";
import { Widget } from "../slots";
import { CatalogDialog } from "./catalog-dialog";
import { SettingsDialog } from "./settings-dialog";
import { WidgetFrame } from "./frame";
import { EditGrid } from "./grid";
import { moveBy, moveInOrder, readingOrder, resizeBy, scaleItems, scaleTypes, unscaleItems, COLS_DESKTOP, COLS_TABLET } from "./model";
import { realApi } from "./api";
import { useLayoutEditor } from "./use-layout-editor";
import type { CatalogType, DashboardEditorProps, LayoutItem } from "./types";

const UNDO_MS = 5000;

type Mode = { kind: "phone" } | { kind: "grid"; cols: number };

function useMode(): Mode {
  const read = (): Mode => {
    if (!window.matchMedia("(min-width: 768px)").matches) return { kind: "phone" };
    return { kind: "grid", cols: window.matchMedia("(min-width: 1280px)").matches ? COLS_DESKTOP : COLS_TABLET };
  };
  const [mode, setMode] = useState<Mode>(read);
  useEffect(() => {
    const qs = [window.matchMedia("(min-width: 768px)"), window.matchMedia("(min-width: 1280px)")];
    const on = () => setMode((m) => {
      const n = read();
      return n.kind === m.kind && (n.kind === "phone" || (m.kind === "grid" && n.cols === m.cols)) ? m : n;
    });
    qs.forEach((q) => q.addEventListener("change", on));
    return () => qs.forEach((q) => q.removeEventListener("change", on));
  }, []);
  return mode;
}

function DefaultBody({ item }: { item: LayoutItem }) {
  // widget contract v2: the instance carries the grid geometry and its settings (title and refresh flag included)
  const settings: Record<string, unknown> = { ...(item.settings ?? {}) };
  if (item.title) settings.title = item.title;
  if (item.auto_refresh !== undefined) settings.auto_refresh = item.auto_refresh;
  return <Widget instance={{ id: item.id, type: item.type, x: item.x, y: item.y, w: item.w, h: item.h, settings }} />;
}

export function DashboardEditor({ layout, canEdit, onChange, onRefresh, renderWidget, api = realApi }: DashboardEditorProps) {
  const { t } = useTranslation("dashboardEdit");
  const ed = useLayoutEditor(layout, api, onChange);
  const mode = useMode();

  const [catalog, setCatalog] = useState<{ state: "loading" | "error" | "ready"; types: CatalogType[] }>({ state: "loading", types: [] });
  const [attempt, setAttempt] = useState(0);
  const [catalogOpen, setCatalogOpen] = useState(false);
  const [settingsId, setSettingsId] = useState<string | null>(null);

  // the catalog is read once per session and again on retry; its size limits are needed by every drag
  useEffect(() => {
    let live = true;
    setCatalog((c) => (c.state === "ready" ? c : { ...c, state: "loading" }));
    api.catalog().then(
      (types) => live && setCatalog({ state: "ready", types }),
      () => live && setCatalog({ state: "error", types: [] }),
    );
    return () => {
      live = false;
    };
  }, [api, attempt]);
  const types = useMemo(() => new Map(catalog.types.map((x) => [x.id, x])), [catalog.types]);

  const titleOf = useCallback((it: LayoutItem) => it.title || types.get(it.type)?.title || it.type, [types]);

  const remove = (id: string, name: string) => {
    const undo = ed.remove(id);
    toast(t("removed", { defaultValue: "Removed: {{title}}", title: name }), {
      duration: UNDO_MS,
      action: { label: t("undo", { defaultValue: "Undo" }), onClick: undo },
    });
  };

  const cols = mode.kind === "grid" ? mode.cols : COLS_DESKTOP;
  // limits as the current grid sees them: a max-6 (of 12) widget allows 3 in a 6-column view
  const viewTypes = useMemo(() => scaleTypes(types, COLS_DESKTOP, cols), [types, cols]);
  const viewItems = useMemo(() => (mode.kind === "grid" ? scaleItems(ed.items, COLS_DESKTOP, mode.cols) : ed.items), [ed.items, mode]);

  if (!canEdit) return null;
  // titles and size limits come from the catalog: show the frame only once it is known (or known to be down)
  if (catalog.state === "loading") {
    return <div role="status" className="py-8 text-center text-sm text-muted-foreground">{t("loadingEditor", { defaultValue: "Loading editor" })}</div>;
  }

  const settingsItem = ed.items.find((i) => i.id === settingsId) ?? null;
  const body = (it: LayoutItem) => (renderWidget ? renderWidget(it, types.get(it.type)) : <DefaultBody item={it} />);

  const order = readingOrder(ed.items);
  const frame = (it: LayoutItem, extra: { phone?: { up?: () => void; down?: () => void }; grid?: boolean }) => (
    <WidgetFrame
      title={titleOf(it)}
      mobile={!!extra.phone}
      onSettings={() => setSettingsId(it.id)}
      onRemove={() => remove(it.id, titleOf(it))}
      onUp={extra.phone?.up}
      onDown={extra.phone?.down}
      onKeyMove={
        extra.grid
          ? (dx, dy, resize) => {
              const v = scaleItems(ed.items, COLS_DESKTOP, cols);
              const next = resize ? resizeBy(v, it.id, dx, dy, viewTypes, cols) : moveBy(v, it.id, dx, dy, viewTypes, cols);
              ed.apply(unscaleItems(next, ed.items, cols, COLS_DESKTOP, types));
            }
          : undefined
      }
    >
      {body(it)}
    </WidgetFrame>
  );

  return (
    <section aria-label={t("region", { defaultValue: "Dashboard editing" })} data-testid="dash-editor" className="space-y-3">
      <div className="sticky top-0 z-20 flex flex-wrap items-center gap-2 rounded-lg border border-border bg-background/95 p-2 backdrop-blur">
        <Button size="sm" onClick={() => setCatalogOpen(true)}>
          <Plus className="h-4 w-4" aria-hidden />
          {t("add", { defaultValue: "Add widget" })}
        </Button>
        <Button size="sm" variant="outline" disabled={!ed.dirty || ed.status === "stale"} onClick={() => void ed.cancelChanges()}>
          <RotateCcw className="h-4 w-4" aria-hidden />
          {t("cancelChanges", { defaultValue: "Cancel changes" })}
        </Button>
        <span role="status" aria-live="polite" className="ml-auto text-xs text-muted-foreground">
          {ed.status === "saving" && t("saving", { defaultValue: "Saving…" })}
          {ed.status === "idle" && t("saved", { defaultValue: "Saved" })}
        </span>
      </div>

      {ed.status === "stale" && (
        <div role="alert" className="flex flex-wrap items-center gap-2 rounded-lg border border-warning/50 bg-warning/10 p-3 text-sm">
          <span className="min-w-0 flex-1">{t("stale", { defaultValue: "The dashboard was changed in another tab or by another member" })}</span>
          <Button size="sm" variant="outline" onClick={() => void (onRefresh ? (onRefresh(), ed.refresh()) : ed.refresh())}>
            {t("refresh", { defaultValue: "Refresh" })}
          </Button>
        </div>
      )}
      {ed.status === "error" && (
        <div role="alert" className="rounded-lg border border-destructive/50 bg-destructive/10 p-3 text-sm">
          {t("saveFailed", { defaultValue: "Could not save the change; the previous layout was restored" })}
        </div>
      )}

      {ed.items.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed border-border py-12 text-center">
          <p className="text-sm text-muted-foreground">{t("empty", { defaultValue: "Add your first widget" })}</p>
          <Button onClick={() => setCatalogOpen(true)}>{t("add", { defaultValue: "Add widget" })}</Button>
        </div>
      ) : mode.kind === "phone" ? (
        <ul className="flex flex-col gap-4" data-testid="dash-edit-phone">
          {order.map((it, i) => (
            <li key={it.id} className="h-56">
              {frame(it, {
                phone: {
                  up: i > 0 ? () => ed.apply(moveInOrder(ed.items, it.id, -1)) : undefined,
                  down: i < order.length - 1 ? () => ed.apply(moveInOrder(ed.items, it.id, 1)) : undefined,
                },
              })}
            </li>
          ))}
        </ul>
      ) : (
        <EditGrid
          items={viewItems}
          types={viewTypes}
          cols={mode.cols}
          onLayout={(next) => ed.apply(unscaleItems(next, ed.items, mode.cols, COLS_DESKTOP, types))}
          renderItem={(it) => frame(ed.items.find((o) => o.id === it.id) ?? it, { grid: true })}
        />
      )}

      <CatalogDialog
        open={catalogOpen}
        onOpenChange={setCatalogOpen}
        state={catalog.state}
        types={catalog.types}
        onRetry={() => setAttempt((n) => n + 1)}
        onPick={(type) => {
          ed.add(type);
          setCatalogOpen(false);
        }}
      />
      <SettingsDialog
        item={settingsItem}
        type={settingsItem ? types.get(settingsItem.type) : undefined}
        onClose={() => setSettingsId(null)}
        onSave={(patch) => settingsItem && ed.apply(ed.items.map((i) => (i.id === settingsItem.id ? { ...i, ...patch } : i)))}
      />
    </section>
  );
}

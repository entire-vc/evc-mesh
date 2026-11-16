// Stand-alone page for the edit mode, served by `vite` only (not part of the build): the layout lives in
// localStorage, so two tabs of one browser share it exactly as two tabs share a server (versioned, 409 on stale).
import { StrictMode, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import "@/i18n";
import "@/index.css";
import { Toaster } from "@/components/ui/toast";
import { DashboardEditor, EditGear, StaleLayoutError, type DashboardLayout, type LayoutApi, type LayoutItem } from "@/core/dashboard/edit";
import { CATALOG, LAYOUT } from "@/core/dashboard/edit/__tests__/fixtures";

const KEY = "dash-edit-fixture";
const read = (): { version: number; items: LayoutItem[] } => JSON.parse(localStorage.getItem(KEY) ?? "null") ?? { version: LAYOUT.version, items: LAYOUT.items };
const write = (v: { version: number; items: LayoutItem[] }) => localStorage.setItem(KEY, JSON.stringify(v));
if (new URLSearchParams(location.search).has("reset")) localStorage.removeItem(KEY);

const api: LayoutApi = {
  catalog: async () => CATALOG,
  save: async (_id, version, items) => {
    const cur = read();
    if (version !== cur.version) throw new StaleLayoutError();
    const next = { version: cur.version + 1, items };
    write(next);
    return next;
  },
  load: async (dashboardId) => ({ dashboardId, ...read() }),
};

function Page() {
  const [editing, setEditing] = useState(new URLSearchParams(location.search).has("edit"));
  const [layout, setLayout] = useState<DashboardLayout>(() => ({ dashboardId: "d1", ...read() }));
  const theme = new URLSearchParams(location.search).get("theme") === "v1" ? "v1" : "v2";
  const byType = useMemo(() => new Map(CATALOG.map((c) => [c.id, c])), []);
  return (
    <div className="mx-auto max-w-[1400px] p-4">
      <Toaster />
      <header className="mb-4 flex items-center justify-between">
        <h1 className="text-xl font-semibold">Dashboard</h1>
        <EditGear canEdit editing={editing} onToggle={() => setEditing((e) => !e)} themeLayout={theme} />
      </header>
      {editing ? (
        <DashboardEditor
          layout={layout}
          canEdit
          api={api}
          onChange={setLayout}
          renderWidget={(it) => <p className="text-sm text-muted-foreground">{byType.get(it.type)?.description} ({it.w}×{it.h} at {it.x},{it.y})</p>}
        />
      ) : (
        <p data-testid="view-mode" className="text-sm text-muted-foreground">View mode: {layout.items.length} widgets, version {layout.version}</p>
      )}
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<StrictMode><Page /></StrictMode>);

// The gear of the dashboard header. Deliberately NOT in the lazy chunk: it is the entry point, and tiny.
import { lazy, Suspense, useState, type ComponentProps } from "react";
import { useTranslation } from "react-i18next";
import { Check, Settings } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { DashboardEditorProps, ThemeLayoutContract } from "./types";

const Editor = lazy(() => import("./dashboard-editor").then((m) => ({ default: m.DashboardEditor })));

/** `<DashboardEditor/>` as the page mounts it: the chunk (grid library, catalog, forms) loads on first render. */
export function LazyDashboardEditor(props: DashboardEditorProps) {
  return (
    <Suspense fallback={<div role="status" className="py-6 text-center text-sm text-muted-foreground">…</div>}>
      <Editor {...props} />
    </Suspense>
  );
}

export interface EditGearProps extends Pick<ComponentProps<typeof Button>, "className"> {
  canEdit: boolean;
  editing: boolean;
  onToggle: () => void;
  /** Theme's `dashboard.layout` contract; v1 cannot edit (§5.10). */
  themeLayout?: ThemeLayoutContract;
}

export function EditGear({ canEdit, editing, onToggle, themeLayout = "v2", className }: EditGearProps) {
  const { t } = useTranslation("dashboardEdit");
  const [explain, setExplain] = useState(false);
  if (!canEdit) return null;
  if (themeLayout === "v1") {
    const why = t("themeV1", { defaultValue: "The theme does not support layout editing; the standard view is used for now" });
    return (
      <span className="relative inline-flex">
        <Button variant="ghost" size="icon" className={className} aria-label={t("gear", { defaultValue: "Edit dashboard" })} aria-disabled="true" aria-describedby="dash-edit-why" onClick={() => setExplain((v) => !v)}>
          <Settings className="h-4 w-4 opacity-50" aria-hidden />
        </Button>
        {explain && (
          <span id="dash-edit-why" role="status" className="absolute right-0 top-full z-30 mt-1 w-64 rounded-lg border border-border bg-card p-2 text-xs shadow-lg">
            {why}
          </span>
        )}
      </span>
    );
  }
  return editing ? (
    <Button size="sm" className={className} onClick={onToggle}>
      <Check className="h-4 w-4" aria-hidden />
      {t("done", { defaultValue: "Done" })}
    </Button>
  ) : (
    <Button variant="ghost" size="icon" className={className} aria-label={t("gear", { defaultValue: "Edit dashboard" })} onClick={onToggle}>
      <Settings className="h-4 w-4" aria-hidden />
    </Button>
  );
}

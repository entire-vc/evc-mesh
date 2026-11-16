import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Search } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { CatalogType } from "./types";

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  state: "loading" | "error" | "ready";
  types: CatalogType[];
  onRetry: () => void;
  onPick: (type: CatalogType) => void;
}

/** Widget catalog (§5.7): search, groups by category and source, description, source badge. */
export function CatalogDialog({ open, onOpenChange, state, types, onRetry, onPick }: Props) {
  const { t } = useTranslation("dashboardEdit");
  const [q, setQ] = useState("");
  const groups = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const hit = types.filter((x) => !needle || `${x.title} ${x.description} ${x.source.name} ${x.category}`.toLowerCase().includes(needle));
    const map = new Map<string, CatalogType[]>();
    for (const x of hit) {
      // extensions are grouped by their own name, the rest by category
      const key = x.source.kind === "extension" || x.source.kind === "external" ? x.source.name : t(`category.${x.category}`, { defaultValue: x.category });
      map.set(key, [...(map.get(key) ?? []), x]);
    }
    return [...map.entries()];
  }, [types, q, t]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange} className="max-w-2xl" closeOnBackdrop>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("catalog.title", { defaultValue: "Add widget" })}</DialogTitle>
          <DialogDescription>{t("catalog.hint", { defaultValue: "The widget is placed in the first free spot." })}</DialogDescription>
        </DialogHeader>
        <div className="relative mb-3">
          <Search className="pointer-events-none absolute left-3 top-2.5 h-4 w-4 text-muted-foreground" aria-hidden />
          <Input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder={t("catalog.search", { defaultValue: "Search widgets" })}
            aria-label={t("catalog.search", { defaultValue: "Search widgets" })}
            className="pl-9"
          />
        </div>
        <div className="max-h-[60vh] space-y-4 overflow-y-auto pr-1">
          {state === "loading" && <p role="status" className="py-6 text-center text-sm text-muted-foreground">{t("catalog.loading", { defaultValue: "Loading widgets" })}</p>}
          {state === "error" && (
            <div role="alert" className="flex flex-col items-center gap-2 py-6 text-sm">
              <p>{t("catalog.error", { defaultValue: "Could not load the widget catalog" })}</p>
              <Button variant="outline" size="sm" onClick={onRetry}>{t("retry", { defaultValue: "Retry" })}</Button>
            </div>
          )}
          {state === "ready" && groups.length === 0 && (
            <p className="py-6 text-center text-sm text-muted-foreground">
              {q ? t("catalog.noMatch", { defaultValue: "Nothing matches the search" }) : t("catalog.empty", { defaultValue: "No widgets are available to you" })}
            </p>
          )}
          {state === "ready" &&
            groups.map(([group, list]) => (
              <section key={group} aria-label={group}>
                <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">{group}</h3>
                <ul className="space-y-1">
                  {list.map((x) => (
                    <li key={x.id}>
                      <button
                        type="button"
                        onClick={() => onPick(x)}
                        className="flex w-full items-start gap-3 rounded-lg border border-border p-3 text-left hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        <span className="min-w-0 flex-1">
                          <span className="block text-sm font-medium">{x.title}</span>
                          <span className="block text-xs text-muted-foreground">{x.description}</span>
                        </span>
                        <span className="flex shrink-0 flex-col items-end gap-1">
                          <Badge variant="secondary">{x.source.name}</Badge>
                          <span className="text-xs text-muted-foreground">{x.size.default.w}×{x.size.default.h}</span>
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              </section>
            ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}

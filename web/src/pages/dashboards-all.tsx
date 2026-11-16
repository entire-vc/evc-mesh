import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { LayoutDashboard, Search, Users } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EntityIcon } from "@/components/ui/entity-icon";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { apiErrorMessage } from "@/lib/api-error";
import { setDashboardHidden, type Dashboard } from "@/lib/dashboards";
import { useDashboardsStore } from "@/stores/dashboards";
import { useWorkspaceStore } from "@/stores/workspace";
import { dashboardHref } from "@/components/layout/use-dashboards-nav";

/** «All dashboards»: every dashboard I may open, the shared ones I hid from the menu included, with search. */
export function DashboardsAllPage() {
  const { t } = useTranslation("shell");
  const ws = useWorkspaceStore((s) => s.currentWorkspace);
  const { items, status, load } = useDashboardsStore();
  const [q, setQ] = useState("");
  const [busyId, setBusyId] = useState<string | null>(null);

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return items
      .filter((d) => !needle || d.name.toLowerCase().includes(needle))
      // Hidden ones last: the menu ones are what people look for first.
      .sort((a, b) => Number(a.hidden) - Number(b.hidden) || a.placement.localeCompare(b.placement) || a.position - b.position);
  }, [items, q]);

  const toggle = async (d: Dashboard) => {
    if (!ws) return;
    setBusyId(d.id);
    try {
      await setDashboardHidden(ws.id, d.id, !d.hidden);
      await load(ws.id, { silent: true });
    } catch (err) {
      toast.error(apiErrorMessage(err, t("dashboards.allPage.showFailed")));
    } finally {
      setBusyId(null);
    }
  };

  return (
    <div className="mx-auto w-full max-w-3xl space-y-4 p-4 sm:p-6">
      <h1 className="text-xl font-semibold">{t("dashboards.allPage.title")}</h1>
      <div className="relative">
        <Search aria-hidden="true" className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          type="search"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder={t("dashboards.allPage.search")}
          aria-label={t("dashboards.allPage.searchLabel")}
          className="pl-9"
        />
      </div>

      {status === "loading" || status === "idle" ? (
        <div data-testid="dashboards-all-loading" aria-busy="true" className="space-y-2">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      ) : status === "error" ? (
        <div role="alert" className="space-y-2 rounded-lg border border-border p-4 text-sm">
          <p className="text-destructive">{t("dashboards.allPage.loadFailed")}</p>
          <Button size="sm" variant="outline" onClick={() => ws && void load(ws.id)}>{t("dashboards.allPage.retry")}</Button>
        </div>
      ) : shown.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">
          {q.trim() ? t("dashboards.allPage.noMatch", { q: q.trim() }) : t("dashboards.allPage.empty")}
        </p>
      ) : (
        <ul className="divide-y divide-border rounded-lg border border-border" data-testid="dashboards-all-list">
          {shown.map((d) => (
            <li key={d.id} data-hidden={d.hidden || undefined} className="flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2.5">
              <span aria-hidden="true" className="flex h-7 w-7 shrink-0 items-center justify-center text-lg leading-none">
                <EntityIcon value={d.icon} fallback={LayoutDashboard} className="h-4 w-4 text-muted-foreground" />
              </span>
              <div className="min-w-0 flex-1">
                <Link to={ws ? dashboardHref(ws.slug, d) : "/"} className="block truncate text-sm font-medium hover:underline">
                  {d.name}
                </Link>
                <p className="flex items-center gap-1 truncate text-xs text-muted-foreground">
                  {d.shared ? (
                    <>
                      <Users aria-hidden="true" className="h-3 w-3" />
                      {d.author_name ? t("dashboards.allPage.sharedBy", { name: d.author_name }) : t("dashboards.allPage.sharedBadge")}
                    </>
                  ) : (
                    t("dashboards.allPage.personal")
                  )}
                  {d.hidden && <span className="ml-1 rounded bg-muted px-1.5 py-0.5 text-[10px] uppercase">{t("dashboards.allPage.hidden")}</span>}
                </p>
              </div>
              {d.shared && (
                <Button size="sm" variant={d.hidden ? "default" : "outline"} disabled={busyId === d.id} onClick={() => void toggle(d)}>
                  {d.hidden ? t("dashboards.allPage.show") : t("dashboards.allPage.hide")}
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

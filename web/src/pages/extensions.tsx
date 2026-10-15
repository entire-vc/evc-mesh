import { useTranslation } from "react-i18next";
import { Navigate, useLocation, useParams, useSearchParams } from "react-router";
import { AlertTriangle, PackageSearch, Plug, RefreshCw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/cn";
import { useMemberStore } from "@/stores/member";
import { useExtensionsOverview, type ExtensionsLoadState } from "@/hooks/useExtensionsOverview";
import { IntegrationsPage } from "@/pages/integrations";

const TABS = ["catalog", "installed", "updates"] as const;
type ExtensionsTab = (typeof TABS)[number];

// W2.8 / W2.9 are not shipped yet: the admin actions stay hidden until the flag is on.
const ADMIN_ACTIONS_ENABLED = import.meta.env.VITE_EXTENSIONS_ADMIN_ACTIONS === "true";

export function extensionsTab(search: URLSearchParams): ExtensionsTab {
  const tab = search.get("tab");
  return (TABS as readonly string[]).includes(tab ?? "") ? (tab as ExtensionsTab) : "catalog";
}

interface TabStateProps {
  state: ExtensionsLoadState;
  ns: "catalog" | "updates";
}

function TabState({ state, ns }: TabStateProps) {
  const { t } = useTranslation("integrations");
  const key = `extensionsPage.${ns}`;
  if (state === "loading") {
    return (
      <div role="status" aria-label={t(`${key}.loading`)} className="space-y-3">
        <Skeleton className="h-24 w-full rounded-xl" />
        <Skeleton className="h-24 w-full rounded-xl" />
      </div>
    );
  }
  const isError = state === "error";
  const Icon = isError ? AlertTriangle : ns === "catalog" ? PackageSearch : RefreshCw;
  return (
    <div
      role={isError ? "alert" : undefined}
      className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-border px-6 py-16 text-center"
    >
      <Icon className="h-8 w-8 text-muted-foreground" aria-hidden />
      <p className="text-sm font-medium">{t(`${key}.${isError ? "errorTitle" : "emptyTitle"}`)}</p>
      <p className="max-w-prose text-sm text-muted-foreground">
        {t(`${key}.${isError ? "errorBody" : "emptyBody"}`)}
      </p>
      {isError && (
        <Button variant="outline" size="sm" onClick={() => window.location.reload()}>
          {t(`${key}.retry`)}
        </Button>
      )}
    </div>
  );
}

export function ExtensionsPage() {
  const { t } = useTranslation("integrations");
  const [search, setSearch] = useSearchParams();
  const { myRole } = useMemberStore();
  const overview = useExtensionsOverview();
  const tab = extensionsTab(search);
  const isAdmin = myRole === "owner" || myRole === "admin";

  const select = (next: ExtensionsTab) => {
    const params = new URLSearchParams(search);
    if (next === "catalog") params.delete("tab");
    else params.set("tab", next);
    setSearch(params, { replace: true });
  };

  return (
    <div className="flex h-full flex-col">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <p className="max-w-prose text-sm text-muted-foreground">{t("extensionsPage.intro")}</p>
        {ADMIN_ACTIONS_ENABLED && isAdmin && (
          <div className="flex items-center gap-3">
            {/* Route lands with W2.8; until then the entry is inert, not a dead link. */}
            <span aria-disabled="true" className="cursor-not-allowed text-sm text-muted-foreground/60">
              {t("extensionsPage.instanceSettings")}
            </span>
            <Button size="sm" disabled>
              <Plug className="mr-1 h-4 w-4" aria-hidden />
              {t("extensionsPage.installFromRepo")}
            </Button>
          </div>
        )}
      </div>

      <div
        role="tablist"
        aria-label={t("extensionsPage.tabs.label")}
        className="mb-4 flex gap-1 border-b border-border"
      >
        {TABS.map((id) => {
          const active = tab === id;
          return (
            <button
              key={id}
              type="button"
              role="tab"
              id={`extensions-tab-${id}`}
              aria-selected={active}
              aria-controls={`extensions-panel-${id}`}
              onClick={() => select(id)}
              className={cn(
                "-mb-px flex items-center gap-2 border-b-2 px-3 py-2 text-sm transition-colors",
                active
                  ? "border-primary font-medium text-foreground"
                  : "border-transparent text-muted-foreground hover:text-foreground",
              )}
            >
              {t(`extensionsPage.tabs.${id}`)}
              {id === "updates" && overview.updatesCount > 0 && (
                <Badge
                  variant="default"
                  aria-label={t("extensionsPage.updatesCount", { count: overview.updatesCount })}
                >
                  {overview.updatesCount}
                </Badge>
              )}
            </button>
          );
        })}
      </div>

      <div
        role="tabpanel"
        id={`extensions-panel-${tab}`}
        aria-labelledby={`extensions-tab-${tab}`}
        className="flex-1 overflow-y-auto"
      >
        {tab === "catalog" && <TabState state={overview.state} ns="catalog" />}
        {tab === "installed" && (
          // Built-in connections (GitLab, GitHub, Spark, Team Relay, Telegram) live
          // here until W2.5 moves them into the extension list.
          <IntegrationsPage />
        )}
        {tab === "updates" && <TabState state={overview.state} ns="updates" />}
      </div>
    </div>
  );
}

/** Old `w/:ws/integrations` route: keep bookmarks working, preserve query and hash. */
export function IntegrationsRedirect() {
  const { wsSlug } = useParams();
  const { search, hash } = useLocation();
  return <Navigate to={`/w/${wsSlug}/extensions${search}${hash}`} replace />;
}

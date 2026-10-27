import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, Navigate, useLocation, useParams, useSearchParams } from "react-router";
import { Plug } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { useMemberStore } from "@/stores/member";
import { useWorkspaceStore } from "@/stores/workspace";
import { useExtensionCatalog } from "@/hooks/useExtensionCatalog";
import { getInstanceOverview } from "@/lib/extension-instance";
import { ExtensionsCatalog } from "@/components/extensions/catalog";
import { InstalledExtensions } from "@/components/extensions/installed";
import { RepoInstallDialog } from "@/components/extensions/repo-install-dialog";
import { UpdatesTab } from "@/components/extensions/updates-tab";
import { useRepoUpdates } from "@/hooks/useRepoUpdates";
import { McpReference } from "@/pages/mcp-reference";

const TABS = ["catalog", "installed", "updates"] as const;
type ExtensionsTab = (typeof TABS)[number];

export function extensionsTab(search: URLSearchParams): ExtensionsTab {
  const tab = search.get("tab");
  return (TABS as readonly string[]).includes(tab ?? "") ? (tab as ExtensionsTab) : "catalog";
}

export function ExtensionsPage() {
  const { t } = useTranslation("integrations");
  const [search, setSearch] = useSearchParams();
  const { wsSlug } = useParams();
  const { fetchMyRole } = useMemberStore();
  const currentWorkspace = useWorkspaceStore((st) => st.currentWorkspace);
  const catalog = useExtensionCatalog();
  const tab = extensionsTab(search);

  // The role is not carried over on a direct load or reload; the removed legacy page used to fetch it.
  const workspaceId = currentWorkspace?.id;
  useEffect(() => {
    if (workspaceId) void fetchMyRole(workspaceId);
  }, [workspaceId, fetchMyRole]);

  // The instance administrator is not the same person as the admin of the current
  // workspace: only the instance API can say who may open the settings page.
  const [instanceAdmin, setInstanceAdmin] = useState<boolean | null>(null);
  const [autoUpdate, setAutoUpdate] = useState(false);
  const [installOpen, setInstallOpen] = useState(false);
  const isInstanceAdmin = instanceAdmin === true;
  const repoUpdates = useRepoUpdates(instanceAdmin);
  useEffect(() => {
    let live = true;
    getInstanceOverview().then(
      (ov) => {
        if (!live) return;
        setInstanceAdmin(true);
        setAutoUpdate(ov.auto_update);
      },
      () => live && setInstanceAdmin(false),
    );
    return () => {
      live = false;
    };
  }, []);

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
        {isInstanceAdmin && (
          <div className="flex items-center gap-3">
            <Link to={`/w/${wsSlug}/instance`} className="text-sm text-primary hover:underline">
              {t("extensionsPage.instanceSettings")}
            </Link>
            <Button size="sm" onClick={() => setInstallOpen(true)}>
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
              {id === "updates" && repoUpdates.count > 0 && (
                <Badge
                  variant="default"
                  aria-label={t("extensionsPage.updatesCount", { count: repoUpdates.count })}
                >
                  {repoUpdates.count}
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
        {tab === "catalog" && <ExtensionsCatalog catalog={catalog} />}
        {tab === "installed" && (
          <div className="space-y-8">
            <InstalledExtensions catalog={catalog} />
            {/* MCP is a reference card, not an extension. */}
            <section aria-labelledby="legacy-connections">
              <h2 id="legacy-connections" className="mb-3 text-sm font-semibold">
                {t("installed.legacyTitle")}
              </h2>
              <McpReference />
            </section>
          </div>
        )}
        {tab === "updates" && <UpdatesTab state={repoUpdates} autoUpdate={autoUpdate} onAutoUpdateChange={setAutoUpdate} />}
      </div>
      {installOpen && (
        <RepoInstallDialog
          onClose={() => setInstallOpen(false)}
          onInstalled={() => {
            repoUpdates.reload();
            catalog.refresh();
          }}
        />
      )}
    </div>
  );
}

/** Old `w/:ws/integrations` route: keep bookmarks working, preserve query and hash. */
export function IntegrationsRedirect() {
  const { wsSlug } = useParams();
  const { search, hash } = useLocation();
  return <Navigate to={`/w/${wsSlug}/extensions${search}${hash}`} replace />;
}

import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, Navigate, useLocation, useParams, useSearchParams } from "react-router";
import { AlertTriangle, ArrowLeft, Lock } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ExtensionsTab } from "@/components/instance-admin/extensions-tab";
import { JournalTab } from "@/components/instance-admin/journal-tab";
import { InstalledThemes } from "@/components/themes/installed-themes";
import { ThemeSettingsPanel } from "@/components/themes/theme-settings-panel";
import { ApiRequestError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { getInstanceOverview, listExtensionSummaries, type InstanceExtensionSummary, type InstanceOverview } from "@/lib/extension-instance";

const TABS = ["extensions", "themes", "journal"] as const;
type AdminTab = (typeof TABS)[number];

export function instanceAdminTab(search: URLSearchParams): AdminTab {
  const tab = search.get("tab");
  return (TABS as readonly string[]).includes(tab ?? "") ? (tab as AdminTab) : "extensions";
}

function ThemesTab() {
  const { t } = useTranslation("settings");
  const [forbidden, setForbidden] = useState(false);
  // Installing or removing a theme changes what the default-theme cards offer.
  const [rev, setRev] = useState(0);
  if (forbidden) {
    return (
      <p role="alert" className="flex items-center gap-2 rounded-lg border p-4 text-sm text-muted-foreground">
        <Lock className="h-4 w-4" aria-hidden />
        {t("themes.instance.forbidden")}
      </p>
    );
  }
  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>{t("themes.instance.defaultTitle")}</CardTitle>
          <CardDescription>{t("themes.instance.defaultHint")}</CardDescription>
        </CardHeader>
        <CardContent>
          <ThemeSettingsPanel key={rev} level="instance" onForbidden={() => setForbidden(true)} />
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>{t("themes.instance.installedTitle")}</CardTitle>
          <CardDescription>{t("themes.instance.installedHint")}</CardDescription>
        </CardHeader>
        <CardContent>
          <InstalledThemes onChanged={() => setRev((r) => r + 1)} />
        </CardContent>
      </Card>
    </div>
  );
}

type LoadState = "loading" | "ready" | "forbidden" | "error";

/** `w/:ws/instance` — instance administration in one place: extensions, themes, journal. */
export function InstanceAdminPage() {
  const { t } = useTranslation("integrations");
  const { wsSlug = "" } = useParams();
  const [search, setSearch] = useSearchParams();
  const tab = instanceAdminTab(search);
  const [state, setState] = useState<LoadState>("loading");
  const [overview, setOverview] = useState<InstanceOverview | null>(null);
  const [summaries, setSummaries] = useState<InstanceExtensionSummary[]>([]);

  const load = useCallback(async () => {
    try {
      const [ov, sm] = await Promise.all([getInstanceOverview(), listExtensionSummaries()]);
      setOverview({ ...ov, items: ov.items ?? [] });
      setSummaries(sm.extensions ?? []);
      setState("ready");
    } catch (err) {
      setState(err instanceof ApiRequestError && err.status === 403 ? "forbidden" : "error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const refresh = useCallback(() => {
    void getInstanceOverview().then((ov) => setOverview({ ...ov, items: ov.items ?? [] }), () => undefined);
  }, []);

  const select = (next: AdminTab) => {
    const params = new URLSearchParams(search);
    if (next === "extensions") params.delete("tab");
    else params.set("tab", next);
    if (next !== "extensions") params.delete("ext");
    setSearch(params, { replace: true });
  };

  const names = new Map(overview?.items.map((i) => [i.id, i.name]) ?? []);

  return (
    <div data-testid="instance-admin" className="mx-auto flex h-full w-full max-w-6xl flex-col gap-4 overflow-y-auto pb-8">
      <div>
        <Link to={`/w/${wsSlug}/extensions`} className="mb-2 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
          <ArrowLeft className="h-4 w-4" aria-hidden />
          {t("instanceAdmin.back")}
        </Link>
        <h1 className="text-xl font-semibold">{t("instanceAdmin.title")}</h1>
        <p className="max-w-prose text-sm text-muted-foreground">{t("instanceAdmin.intro")}</p>
      </div>

      {state === "loading" && (
        <div role="status" aria-label={t("instanceAdmin.loading")} className="space-y-3">
          <Skeleton className="h-10 w-72 rounded-lg" />
          <Skeleton className="h-16 w-full rounded-xl" />
          <Skeleton className="h-64 w-full rounded-xl" />
        </div>
      )}

      {(state === "forbidden" || state === "error") && (
        <div role={state === "error" ? "alert" : undefined} data-testid={`state-${state}`} className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-border px-6 py-16 text-center">
          {state === "forbidden" ? <Lock className="h-8 w-8 text-muted-foreground" aria-hidden /> : <AlertTriangle className="h-8 w-8 text-muted-foreground" aria-hidden />}
          <p className="text-sm font-medium">{t(state === "forbidden" ? "instanceAdmin.noAccessTitle" : "instanceAdmin.errorTitle")}</p>
          <p className="max-w-prose text-sm text-muted-foreground">{t(state === "forbidden" ? "instanceAdmin.noAccessBody" : "instanceAdmin.errorBody")}</p>
          {state === "error" && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setState("loading");
                void load();
              }}
            >
              {t("instanceAdmin.retry")}
            </Button>
          )}
        </div>
      )}

      {state === "ready" && overview && (
        <>
          <div role="tablist" aria-label={t("instanceAdmin.tabs.label")} className="flex shrink-0 gap-1 overflow-x-auto border-b border-border">
            {TABS.map((id) => {
              const active = tab === id;
              return (
                <button
                  key={id}
                  type="button"
                  role="tab"
                  id={`instance-tab-${id}`}
                  aria-selected={active}
                  aria-controls={`instance-panel-${id}`}
                  onClick={() => select(id)}
                  className={cn(
                    "-mb-px shrink-0 border-b-2 px-3 py-2 text-sm transition-colors",
                    active ? "border-primary font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
                  )}
                >
                  {t(`instanceAdmin.tabs.${id}`)}
                </button>
              );
            })}
          </div>
          <div role="tabpanel" id={`instance-panel-${tab}`} aria-labelledby={`instance-tab-${tab}`}>
            {tab === "extensions" && <ExtensionsTab overview={overview} summaries={summaries} onChanged={refresh} />}
            {tab === "themes" && <ThemesTab />}
            {tab === "journal" && <JournalTab names={names} />}
          </div>
        </>
      )}
    </div>
  );
}

/** Old addresses (`/instance/extensions`, `/instance/themes`): same screen, query kept. */
export function InstanceAdminRedirect({ tab }: { tab: AdminTab }) {
  const { wsSlug = "" } = useParams();
  const { search, hash } = useLocation();
  const params = new URLSearchParams(search);
  if (tab !== "extensions") params.set("tab", tab);
  const q = params.toString();
  return <Navigate to={`/w/${wsSlug}/instance${q ? `?${q}` : ""}${hash}`} replace />;
}

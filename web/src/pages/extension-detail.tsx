import { useTranslation } from "react-i18next";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { AlertTriangle, ArrowLeft, ExternalLink, PackageSearch } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { MarkdownView } from "@/components/markdown-view";
import { ExtensionIcon, KindTrustBadges } from "@/components/extensions/catalog";
import { useExtensionCatalog, type CatalogEntry } from "@/hooks/useExtensionCatalog";
import { allowedDomains, describeCapabilities } from "@/lib/extension-capabilities";
import { useMemberStore } from "@/stores/member";
import { cn } from "@/lib/cn";

export const DETAIL_TABS = ["description", "permissions", "versions", "checks", "support"] as const;
export type DetailTab = (typeof DETAIL_TABS)[number];

export function detailTab(search: URLSearchParams): DetailTab {
  const tab = search.get("tab");
  return (DETAIL_TABS as readonly string[]).includes(tab ?? "") ? (tab as DetailTab) : "description";
}

// W2.9 (install from repository) is not shipped: the admin action stays inert.
const ADMIN_ACTIONS_ENABLED = import.meta.env.VITE_EXTENSIONS_ADMIN_ACTIONS === "true";

function Permissions({ entry }: { entry: CatalogEntry }) {
  const { t } = useTranslation("integrations");
  const caps = entry.manifest.capabilities;
  const lines = describeCapabilities(caps);
  const domains = allowedDomains(caps);
  if (lines.length === 0 && domains.length === 0) {
    return <p className="text-sm text-muted-foreground">{t("detail.permissions.none")}</p>;
  }
  return (
    <div className="space-y-6">
      <ul className="space-y-2" aria-label={t("detail.tabs.permissions")}>
        {lines.map((l) => (
          <li key={`${l.group}:${l.raw}`} className="flex items-start gap-2 text-sm">
            <span className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-primary" aria-hidden />
            <span title={l.raw}>{t(l.key, l.values)}</span>
          </li>
        ))}
      </ul>
      {domains.length > 0 && (
        <div>
          <h3 className="mb-2 text-sm font-semibold">{t("detail.permissions.domains")}</h3>
          <ul className="flex flex-wrap gap-2">
            {domains.map((d) => <li key={d}><code className="rounded bg-muted px-2 py-0.5 text-xs">{d}</code></li>)}
          </ul>
          {caps?.network?.reasoning && <p className="mt-2 text-sm text-muted-foreground">{caps.network.reasoning}</p>}
        </div>
      )}
    </div>
  );
}

function Versions({ entry }: { entry: CatalogEntry }) {
  const { t } = useTranslation("integrations");
  const rows = entry.versions?.length
    ? entry.versions
    : [{ version: entry.version, mesh_api: entry.manifest.mesh_api }];
  return (
    <ol className="space-y-3">
      {rows.map((v) => (
        <li key={v.version} className="rounded-lg border border-border p-3 text-sm">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-semibold">{v.version}</span>
            {v.version === entry.version && <Badge variant="success">{t("detail.versions.current")}</Badge>}
            {"released_at" in v && v.released_at && (
              <span className="text-xs text-muted-foreground">{v.released_at.slice(0, 10)}</span>
            )}
          </div>
          {v.mesh_api && (
            <p className="mt-1 text-muted-foreground">{t("detail.versions.meshApi", { range: v.mesh_api })}</p>
          )}
          {"changes" in v && v.changes && <p className="mt-2 whitespace-pre-wrap">{v.changes}</p>}
          {"capabilities_added" in v && (v.capabilities_added?.length ?? 0) > 0 && (
            <p className="mt-2 text-warning-foreground">
              {t("detail.versions.added")}: <code>{v.capabilities_added?.join(", ")}</code>
            </p>
          )}
          {"capabilities_removed" in v && (v.capabilities_removed?.length ?? 0) > 0 && (
            <p className="mt-1 text-muted-foreground">
              {t("detail.versions.removed")}: <code>{v.capabilities_removed?.join(", ")}</code>
            </p>
          )}
        </li>
      ))}
      {!entry.versions?.length && <p className="text-sm text-muted-foreground">{t("detail.versions.noHistory")}</p>}
    </ol>
  );
}

function Checks({ entry }: { entry: CatalogEntry }) {
  const { t } = useTranslation("integrations");
  if (entry.trust === "official" && entry.runtime === "builtin") {
    return <p className="text-sm">{t("detail.checks.builtin")}</p>;
  }
  if (!entry.checks) {
    return (
      <p className="text-sm text-muted-foreground">
        {t(entry.trust === "custom" ? "detail.checks.custom" : "detail.checks.none")}
      </p>
    );
  }
  const variant = { passed: "success", warnings: "warning", failed: "destructive" } as const;
  return (
    <div className="space-y-2 text-sm">
      <Badge variant={variant[entry.checks.result]}>{t(`detail.checks.result.${entry.checks.result}`)}</Badge>
      {entry.checks.checked_at && <p>{t("detail.checks.date", { date: entry.checks.checked_at.slice(0, 10) })}</p>}
      {entry.checks.reviewer && <p>{t("detail.checks.reviewer", { name: entry.checks.reviewer })}</p>}
    </div>
  );
}

function Support({ entry }: { entry: CatalogEntry }) {
  const { t } = useTranslation("integrations");
  const m = entry.manifest;
  const link = (href: string, label: string) => (
    <a href={href} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-primary hover:underline">
      {label}<ExternalLink className="h-3 w-3" aria-hidden />
    </a>
  );
  const canReport = entry.runtime !== "builtin" && m.repository;
  return (
    <dl className="grid max-w-xl grid-cols-[8rem_1fr] gap-x-4 gap-y-2 text-sm">
      <dt className="text-muted-foreground">{t("detail.support.publisher")}</dt>
      <dd>{entry.publisher}</dd>
      {m.repository && (<><dt className="text-muted-foreground">{t("detail.support.repository")}</dt><dd className="break-all">{link(m.repository, m.repository)}</dd></>)}
      {m.homepage && (<><dt className="text-muted-foreground">{t("detail.support.docs")}</dt><dd className="break-all">{link(m.homepage, m.homepage)}</dd></>)}
      {m.license && (<><dt className="text-muted-foreground">{t("detail.support.license")}</dt><dd>{m.license}</dd></>)}
      {canReport && (<><dt className="text-muted-foreground">{t("detail.support.report")}</dt><dd>{link(`${m.repository}/issues`, t("detail.support.reportLink"))}</dd></>)}
    </dl>
  );
}

function PrimaryAction({ entry, wsSlug, isAdmin }: { entry: CatalogEntry; wsSlug: string; isAdmin: boolean }) {
  const { t } = useTranslation("integrations");
  const navigate = useNavigate();
  // Until the connect wizard (W2.4) and the installed list (W2.5) land, connecting
  // and settings both live on the legacy Installed tab.
  const installed = `/w/${wsSlug}/extensions?tab=installed`;
  if (entry.blocked) return <Button disabled>{t("detail.action.blocked")}</Button>;
  if (!entry.installed) {
    return ADMIN_ACTIONS_ENABLED && isAdmin
      ? <Button disabled>{t("detail.action.install")}</Button>
      : <p className="max-w-56 text-xs text-muted-foreground">{t("detail.action.askAdmin")}</p>;
  }
  if (entry.connected) {
    return <Button variant="outline" onClick={() => navigate(installed)}>{t("detail.action.openSettings")}</Button>;
  }
  if (!entry.allowed) {
    return isAdmin && ADMIN_ACTIONS_ENABLED
      ? <Button disabled>{t("detail.action.allow")}</Button>
      : <p className="max-w-56 text-xs text-muted-foreground">{t("detail.action.notAllowed")}</p>;
  }
  return <Button onClick={() => navigate(installed)}>{t("detail.action.connect")}</Button>;
}

export function ExtensionDetailPage() {
  const { t } = useTranslation("integrations");
  const { wsSlug = "", extId = "" } = useParams();
  const [search, setSearch] = useSearchParams();
  const { myRole } = useMemberStore();
  const catalog = useExtensionCatalog();
  const tab = detailTab(search);
  const isAdmin = myRole === "owner" || myRole === "admin";
  const entry = catalog.entries.find((e) => e.id === extId);

  const back = (
    <Link to={`/w/${wsSlug}/extensions`} className="mb-4 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="h-4 w-4" aria-hidden />{t("detail.back")}
    </Link>
  );

  if (catalog.state === "loading") {
    return (
      <div>
        {back}
        <div role="status" aria-label={t("detail.loading")} className="space-y-3">
          <Skeleton className="h-16 w-full rounded-xl" /><Skeleton className="h-48 w-full rounded-xl" />
        </div>
      </div>
    );
  }
  if (catalog.state === "error" || !entry) {
    const notFound = catalog.state === "ready";
    const Icon = notFound ? PackageSearch : AlertTriangle;
    return (
      <div>
        {back}
        <div role={notFound ? undefined : "alert"} className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-border px-6 py-16 text-center">
          <Icon className="h-8 w-8 text-muted-foreground" aria-hidden />
          <p className="text-sm font-medium">{t(notFound ? "detail.notFoundTitle" : "detail.errorTitle")}</p>
          <p className="max-w-prose text-sm text-muted-foreground">{t(notFound ? "detail.notFoundBody" : "detail.errorBody")}</p>
          {!notFound && <Button variant="outline" size="sm" onClick={catalog.reload}>{t("extensionsPage.catalog.retry")}</Button>}
        </div>
      </div>
    );
  }

  const select = (next: DetailTab) => {
    const p = new URLSearchParams(search);
    if (next === "description") p.delete("tab"); else p.set("tab", next);
    setSearch(p, { replace: true });
  };
  const readme = entry.manifest.readme?.trim() || entry.description;

  return (
    <div className="flex h-full flex-col">
      {back}
      <header className="mb-4 flex flex-wrap items-start justify-between gap-4">
        <div className="flex min-w-0 items-start gap-3">
          <ExtensionIcon entry={entry} className="h-14 w-14" />
          <div className="min-w-0">
            <h1 className="truncate text-xl font-semibold">{entry.name}</h1>
            <p className="text-sm text-muted-foreground">{t("detail.byVersion", { publisher: entry.publisher, version: entry.version })}</p>
            <div className="mt-2 flex flex-wrap items-center gap-1.5">
              <KindTrustBadges entry={entry} />
              {entry.blocked && <Badge variant="destructive">{t("catalog.state.blocked")}</Badge>}
              {!entry.allowed && <Badge variant="warning">{t("catalog.state.notAllowed")}</Badge>}
            </div>
          </div>
        </div>
        <PrimaryAction entry={entry} wsSlug={wsSlug} isAdmin={isAdmin} />
      </header>

      {entry.blocked && (
        <div role="alert" className="mb-4 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm">
          {t("detail.blockedNote", { reason: entry.blockedReason || t("detail.blockedNoReason") })}
        </div>
      )}
      {entry.trust === "custom" && (
        <div role="note" className="mb-4 rounded-lg border border-warning/40 bg-warning/10 px-3 py-2 text-sm">
          {t("detail.customNote")}
        </div>
      )}

      <div role="tablist" aria-label={t("detail.tabs.label")} className="mb-4 flex gap-1 overflow-x-auto border-b border-border">
        {DETAIL_TABS.map((id) => {
          const active = tab === id;
          return (
            <button key={id} type="button" role="tab" id={`ext-tab-${id}`} aria-selected={active}
              aria-controls={`ext-panel-${id}`} onClick={() => select(id)}
              className={cn("-mb-px shrink-0 border-b-2 px-3 py-2 text-sm transition-colors",
                active ? "border-primary font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground")}>
              {t(`detail.tabs.${id}`)}
            </button>
          );
        })}
      </div>

      <div role="tabpanel" id={`ext-panel-${tab}`} aria-labelledby={`ext-tab-${tab}`} className="flex-1 overflow-y-auto pb-6">
        {tab === "description" && (
          <div className="max-w-3xl space-y-4">
            <MarkdownView content={readme} />
            {(entry.manifest.screenshots?.length ?? 0) > 0 && (
              <ul className="grid gap-3 sm:grid-cols-2">
                {entry.manifest.screenshots?.map((src) => (
                  <li key={src}><img src={src} alt="" loading="lazy" className="w-full rounded-lg border border-border" /></li>
                ))}
              </ul>
            )}
          </div>
        )}
        {tab === "permissions" && <Permissions entry={entry} />}
        {tab === "versions" && <Versions entry={entry} />}
        {tab === "checks" && <Checks entry={entry} />}
        {tab === "support" && <Support entry={entry} />}
      </div>
    </div>
  );
}

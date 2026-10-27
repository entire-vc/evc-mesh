import { useTranslation } from "react-i18next";
import type { CatalogEntry } from "@/hooks/useExtensionCatalog";
import { Button } from "@/components/ui/button";
import { extensionState } from "@/lib/extensionState";
import { ExtensionStateBadge, ExtensionStateNote } from "@/components/extensions/extension-state";
import { formatDateTime, formatNumber } from "@/i18n/format";
import { cn } from "@/lib/cn";

/** GitLab/GitHub: task links arrive through instance-level webhooks, with or without a workspace connection. */
export const VCS_EXTENSION_IDS = new Set(["gitlab", "github"]);

/**
 * The instance governs it: nothing to fill in here. A VCS extension may carry an
 * empty workspace connection row and still be governed by the instance.
 */
export function instanceManaged(e: CatalogEntry): boolean {
  if (e.status?.source !== "instance") return false;
  return VCS_EXTENSION_IDS.has(e.id) || !e.connection;
}

/** Repositories whose webhook events brought task links into this workspace. */
export function VcsRepoList({ entry }: { entry: CatalogEntry }) {
  const { t } = useTranslation("integrations");
  const repos = entry.activity?.repos ?? [];
  return (
    <section className="space-y-2" data-testid={`ext-repos-${entry.id}`}>
      <h3 id={`ext-repos-${entry.id}-title`} className="text-sm font-medium">{t("installed.repos.title")}</h3>
      {repos.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("installed.repos.none")}</p>
      ) : (
        <ul aria-labelledby={`ext-repos-${entry.id}-title`} className="max-h-60 divide-y divide-border overflow-y-auto rounded-lg border border-border text-sm">
          {repos.map((r) => (
            <li key={`${r.host}/${r.repo}`} className="flex flex-col gap-0.5 px-3 py-2">
              <span className="min-w-0 break-all font-medium">{r.repo}</span>
              <span className="text-xs text-muted-foreground">
                {r.host} · {t("installed.repos.links", { count: r.links, value: formatNumber(r.links) })}
                {r.last_event_at && <> · {t("installed.repos.last", { last: formatDateTime(r.last_event_at) })}</>}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/**
 * Read-only state of a workspace extension: where it comes from, health, task
 * links it brought in and the last error. Shows no settings and no secrets.
 */
export function ExtensionStatusPanel({ entry, className, instanceNote = true, onCheck, checking }: {
  entry: CatalogEntry; className?: string;
  /** Off where the surrounding view already says who manages the credentials. */
  instanceNote?: boolean;
  /** Runs the check; the panel then shows the re-read status, never a result of its own. */
  onCheck?: () => void;
  checking?: boolean;
}) {
  const { t } = useTranslation("integrations");
  const st = extensionState(entry);
  const health = entry.status?.health;
  const vcs = VCS_EXTENSION_IDS.has(entry.id);
  const act = entry.activity;
  return (
    <dl className={cn("grid gap-x-4 gap-y-2 rounded-lg border border-border p-3 text-sm sm:grid-cols-[auto_1fr]", className)}
      data-testid={`ext-status-${entry.id}`}>
      <dt className="text-muted-foreground">{t("statusPanel.state")}</dt>
      <dd className="min-w-0 space-y-1">
        <span className="flex flex-wrap items-center gap-2">
          <ExtensionStateBadge state={st} />
          {onCheck && (st.state === "error" || st.state === "enabled") && (
            <Button variant="outline" size="sm" disabled={checking} onClick={onCheck}>{t("extensionState.check")}</Button>
          )}
        </span>
        <ExtensionStateNote state={st} />
      </dd>
      <dt className="text-muted-foreground">{t("statusPanel.health")}</dt>
      <dd>{t(health ? (health.ok ? "installed.health.ok" : "installed.health.bad") : "installed.health.unknown")}</dd>
      {vcs && (
        <>
          <dt className="text-muted-foreground">{t("statusPanel.links")}</dt>
          <dd data-testid={`ext-activity-${entry.id}`}>
            {act && act.links > 0
              ? t("statusPanel.linksValue", {
                count: act.links, value: formatNumber(act.links),
                last: act.last_event_at ? formatDateTime(act.last_event_at) : "-",
              })
              : t("statusPanel.linksNone")}
          </dd>
        </>
      )}
      {instanceNote && entry.status?.source === "instance" && (
        <p className="text-xs text-muted-foreground sm:col-span-2">{t("statusPanel.instanceNote")}</p>
      )}
    </dl>
  );
}

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, RefreshCw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { apiErrorMessage } from "@/lib/api-error";
import { setInstanceAutoUpdate } from "@/lib/extension-instance";
import {
  rollbackRepoExtension, shortSha, updateAllRepoExtensions, updateRepoExtension,
  type RepoUpdate,
} from "@/lib/extension-repo";
import type { RepoUpdatesState } from "@/hooks/useRepoUpdates";

function Row({ u, busy, onUpdate, onRollback }: { u: RepoUpdate; busy: boolean; onUpdate: (u: RepoUpdate) => void; onRollback: (u: RepoUpdate) => void }) {
  const { t } = useTranslation("integrations");
  return (
    <li data-testid={`update-row-${u.extension_id}`} className="flex flex-col gap-3 py-4 sm:flex-row sm:items-start sm:justify-between">
      <div className="min-w-0 space-y-1">
        <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
          <span className="truncate">{u.name || u.extension_id}</span>
          <Badge variant="warning">{t("catalog.trust.custom")}</Badge>
          {u.pending && <Badge variant="secondary">{t("updatesTab.awaitingConsent")}</Badge>}
        </p>
        <p className="break-words text-xs text-muted-foreground">
          {t("updatesTab.current", { version: u.current_version, ref: u.current_ref, sha: shortSha(u.current_commit) })}
        </p>
        {u.update_available && (
          <p className="break-words text-xs">
            {t("updatesTab.latest", { version: u.latest_version, ref: u.latest_ref, sha: shortSha(u.latest_commit) })}
          </p>
        )}
        {u.needs_consent && (
          <div className="text-xs">
            <p className="font-medium">{t("updatesTab.newPermissions")}</p>
            <ul className="mt-1 flex flex-wrap gap-1">
              {u.new_capabilities.map((c) => (
                <li key={c}><code className="rounded bg-muted px-1.5 py-0.5">{c}</code></li>
              ))}
            </ul>
          </div>
        )}
        {u.error && (
          <p role="alert" className="flex items-start gap-1 text-xs text-destructive">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" aria-hidden />
            {t("updatesTab.checkFailed", { reason: u.error })}
          </p>
        )}
        {!u.update_available && !u.error && <p className="text-xs text-muted-foreground">{t("updatesTab.upToDate")}</p>}
      </div>
      <div className="flex shrink-0 flex-wrap gap-2">
        {u.can_rollback && (
          <Button variant="outline" size="sm" disabled={busy} onClick={() => onRollback(u)}>
            {t("updatesTab.rollback", { version: u.previous_version })}
          </Button>
        )}
        {u.update_available && (
          <Button size="sm" disabled={busy} onClick={() => onUpdate(u)}>
            {u.needs_consent ? t("updatesTab.consentUpdate") : t("updatesTab.update")}
          </Button>
        )}
      </div>
    </li>
  );
}

export function UpdatesTab({ state, autoUpdate, onAutoUpdateChange }: { state: RepoUpdatesState; autoUpdate: boolean; onAutoUpdateChange: (v: boolean) => void }) {
  const { t } = useTranslation("integrations");
  const [busy, setBusy] = useState(false);
  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    try {
      await fn();
    } catch (err) {
      toast.error(apiErrorMessage(err, t("updatesTab.actionFailed")));
    } finally {
      setBusy(false);
      state.reload();
    }
  };

  if (state.status === "loading") {
    return (
      <div role="status" aria-label={t("extensionsPage.updates.loading")} className="space-y-3">
        <Skeleton className="h-24 w-full rounded-xl" />
        <Skeleton className="h-24 w-full rounded-xl" />
      </div>
    );
  }
  if (state.status === "forbidden") {
    return <Notice icon="info" title={t("updatesTab.adminOnlyTitle")} body={t("updatesTab.adminOnlyBody")} />;
  }
  if (state.status === "error") {
    return (
      <Notice icon="error" title={t("extensionsPage.updates.errorTitle")} body={t("extensionsPage.updates.errorBody")}>
        <Button variant="outline" size="sm" onClick={state.reload}>{t("extensionsPage.updates.retry")}</Button>
      </Notice>
    );
  }

  const pendingUpdates = state.updates.filter((u) => u.update_available && !u.needs_consent);
  const onUpdate = (u: RepoUpdate) =>
    run(async () => {
      const r = await updateRepoExtension(u.extension_id, u.needs_consent ? u.latest_commit : undefined);
      toast.success(t(r.outcome === "updated" ? "updatesTab.updated" : "updatesTab.noChange", { name: u.name || u.extension_id }));
    });
  const onRollback = (u: RepoUpdate) =>
    run(async () => {
      await rollbackRepoExtension(u.extension_id);
      toast.success(t("updatesTab.rolledBack", { name: u.name || u.extension_id }));
    });
  const onAll = () =>
    run(async () => {
      const r = await updateAllRepoExtensions();
      const done = r.results.filter((x) => x.outcome === "updated").length;
      toast.success(t("updatesTab.updatedAll", { count: done }));
    });

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3 rounded-xl border border-border p-4">
        <div className="max-w-prose space-y-1">
          <label className="inline-flex cursor-pointer items-center gap-2 text-sm font-medium">
            <input
              type="checkbox"
              role="switch"
              aria-label={t("instanceAdmin.autoUpdate.label")}
              className="h-4 w-4 accent-primary"
              checked={autoUpdate}
              disabled={busy}
              onChange={(e) =>
                void run(async () => {
                  await setInstanceAutoUpdate(e.target.checked);
                  onAutoUpdateChange(e.target.checked);
                })
              }
            />
            {t("instanceAdmin.autoUpdate.label")}
          </label>
          <p className="text-xs text-muted-foreground">{t("updatesTab.autoUpdateHint")}</p>
        </div>
        <Button size="sm" disabled={busy || pendingUpdates.length === 0} onClick={() => void onAll()}>
          <RefreshCw className="mr-1 h-4 w-4" aria-hidden />
          {t("updatesTab.updateAll")}
        </Button>
      </div>

      {state.updates.length === 0 ? (
        <Notice icon="info" title={t("extensionsPage.updates.emptyTitle")} body={t("updatesTab.emptyBody")} />
      ) : (
        <ul className="divide-y divide-border rounded-xl border border-border px-4">
          {state.updates.map((u) => (
            <Row key={u.extension_id} u={u} busy={busy} onUpdate={(x) => void onUpdate(x)} onRollback={(x) => void onRollback(x)} />
          ))}
        </ul>
      )}
    </div>
  );
}

function Notice({ icon, title, body, children }: { icon: "info" | "error"; title: string; body: string; children?: React.ReactNode }) {
  const Icon = icon === "error" ? AlertTriangle : RefreshCw;
  return (
    <div
      role={icon === "error" ? "alert" : undefined}
      className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-border px-6 py-16 text-center"
    >
      <Icon className="h-8 w-8 text-muted-foreground" aria-hidden />
      <p className="text-sm font-medium">{title}</p>
      <p className="max-w-prose text-sm text-muted-foreground">{body}</p>
      {children}
    </div>
  );
}

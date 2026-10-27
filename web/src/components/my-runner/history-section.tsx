import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast";
import { SectionShell } from "@/components/my-runner/section-shell";
import type { Loaded } from "@/components/my-runner/use-load";
import { formatDateTime } from "@/i18n/format";
import { apiErrorMessage } from "@/lib/api-error";
import { restoreRevision, type CatalogRevision, type HistoryView, type OperatorAgent, type RuntimeHistoryEntry } from "@/lib/my-runner";

/** The author is the signed-in user ("You"), or an agent shown by name when it is one of the user's. */
export function authorLabel(
  e: Pick<RuntimeHistoryEntry, "author_type" | "author_id">,
  me: string | undefined,
  agents: OperatorAgent[],
  t: (key: string, o?: Record<string, unknown>) => string,
): string {
  if (e.author_type === "user") return e.author_id === me ? t("history.you") : e.author_id.slice(0, 8);
  const name = agents.find((a) => a.agent_id === e.author_id)?.name ?? e.author_id.slice(0, 8);
  return t("history.agent", { name });
}

/** A catalog revision is signed by the user who saved it: "You", or their name. */
export function revisionAuthor(e: CatalogRevision, me: string | undefined, t: (key: string) => string): string {
  if (e.author_id === me) return t("history.you");
  return e.author_name || e.author_id.slice(0, 8);
}

/**
 * What a catalog revision did. The Runner switch writes a revision with the
 * previous config (same digest) and the flag flipped, so that is told apart
 * from a save.
 */
export function catalogKind(e: CatalogRevision, older: CatalogRevision | undefined): string {
  if (older && older.digest === e.digest && older.enabled !== e.enabled) return e.enabled ? "turnedOn" : "turnedOff";
  return e.enabled ? "catalog" : "catalogPaused";
}

function Row({ id, label, detail, at, action }: { id: string | number; label: string; detail: string; at: string; action?: ReactNode }) {
  return (
    <li data-revision={id} className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-border px-3 py-2 text-sm">
      {/* Below sm the label takes its own line; time and Restore go under it. */}
      <span className="w-full min-w-0 sm:w-auto sm:flex-1">
        <span className="font-medium">{label}</span>
        <span className="text-muted-foreground"> · {detail}</span>
      </span>
      <time dateTime={at} className="mr-auto text-xs text-muted-foreground sm:mr-0">{formatDateTime(at)}</time>
      {action}
    </li>
  );
}

/** Restore for one earlier revision: asks in the row, then saves its config as the next revision. */
function RestoreAction({ wsId, entry, latest, onRestored }: { wsId: string; entry: CatalogRevision; latest: number; onRestored: () => void }) {
  const { t } = useTranslation("runner");
  const [asking, setAsking] = useState(false);
  const [busy, setBusy] = useState(false);

  const restore = async () => {
    setBusy(true);
    try {
      await restoreRevision(wsId, latest, entry.revision);
      toast.success(t("history.restored", { n: entry.revision }));
      setAsking(false);
      onRestored();
    } catch (e) {
      const conflict = (e as { status?: number }).status === 409;
      toast.error(conflict ? t("resources.conflict") : apiErrorMessage(e, t("history.restoreFailed")));
      if (conflict) onRestored();
    } finally {
      setBusy(false);
    }
  };

  if (!asking) {
    return <Button size="sm" variant="ghost" className="-my-1 h-7" onClick={() => setAsking(true)}>{t("history.restore")}</Button>;
  }
  return (
    // A row of its own under the revision it restores.
    <div role="group" aria-label={t("history.restore")} className="flex basis-full flex-wrap items-center gap-2 rounded-md border border-warning/50 p-2" data-testid={`my-runner-restore-${entry.revision}`}>
      <span className="min-w-0 basis-full sm:basis-auto sm:flex-1">{t("history.restoreConfirm", { n: entry.revision, next: latest + 1 })}</span>
      <Button size="sm" disabled={busy} onClick={() => void restore()}>{t(busy ? "history.restoring" : "history.restore")}</Button>
      <Button size="sm" variant="outline" disabled={busy} onClick={() => setAsking(false)}>{t("admin.cancel")}</Button>
    </div>
  );
}

export function HistorySection({ result, reload, me, agents, wsId, onRestored }: {
  result: Loaded<HistoryView>; reload: () => void; me: string | undefined; agents: OperatorAgent[];
  wsId?: string;
  /** After a restore: the catalog, its history and the controllers all changed. */
  onRestored?: () => void;
}) {
  const { t } = useTranslation("runner");
  return (
    <SectionShell title={t("history.title")} testId="my-runner-history" result={result} onRetry={reload}>
      {(view) =>
        view.entries.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="my-runner-history-empty">{t("history.empty")}</p>
        ) : (
          <ol className="space-y-2">
            {view.source === "workspace"
              ? view.entries.map((e, i, all) => {
                const latest = all[0]!;
                // Restoring makes sense only for a config other than the current one.
                const restorable = wsId && onRestored && i > 0 && e.digest !== latest.digest;
                return (
                  <Row key={e.revision} id={e.revision} at={e.created_at}
                    label={t(`history.kind.${catalogKind(e, all[i + 1])}`)}
                    detail={`${t("history.revision", { n: e.revision })} · ${revisionAuthor(e, me, t)}`}
                    action={restorable ? <RestoreAction wsId={wsId} entry={e} latest={latest.revision} onRestored={onRestored} /> : undefined} />
                );
              })
              : view.entries.map((e: RuntimeHistoryEntry) => (
                <Row key={e.id} id={e.id} at={e.created_at}
                  label={t(`history.kind.${e.kind}`)}
                  detail={`${t("history.revision", { n: e.revision })} · ${authorLabel(e, me, agents, t)}`} />
              ))}
          </ol>
        )
      }
    </SectionShell>
  );
}

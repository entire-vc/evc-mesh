import { useTranslation } from "react-i18next";
import { SectionShell } from "@/components/my-runner/section-shell";
import type { Loaded } from "@/components/my-runner/use-load";
import { formatDateTime } from "@/i18n/format";
import type { CatalogRevision, HistoryView, OperatorAgent, RuntimeHistoryEntry } from "@/lib/my-runner";

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

function Row({ id, label, detail, at }: { id: string | number; label: string; detail: string; at: string }) {
  return (
    <li data-revision={id} className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 rounded-lg border border-border px-3 py-2 text-sm">
      <span className="min-w-0">
        <span className="font-medium">{label}</span>
        <span className="text-muted-foreground"> · {detail}</span>
      </span>
      <time dateTime={at} className="text-xs text-muted-foreground">{formatDateTime(at)}</time>
    </li>
  );
}

export function HistorySection({ result, reload, me, agents }: {
  result: Loaded<HistoryView>; reload: () => void; me: string | undefined; agents: OperatorAgent[];
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
              ? view.entries.map((e) => (
                <Row key={e.revision} id={e.revision} at={e.created_at}
                  label={t(e.enabled ? "history.kind.catalog" : "history.kind.catalogPaused")}
                  detail={`${t("history.revision", { n: e.revision })} · ${revisionAuthor(e, me, t)}`} />
              ))
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

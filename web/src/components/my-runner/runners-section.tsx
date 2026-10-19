import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { SectionShell } from "@/components/my-runner/section-shell";
import type { Loaded } from "@/components/my-runner/use-load";
import { formatRelativeTime } from "@/i18n/format";
import type { OperatorRunner } from "@/lib/my-runner";

export function RunnersSection({ result, reload }: { result: Loaded<OperatorRunner[]>; reload: () => void }) {
  const { t } = useTranslation("runner");
  return (
    <SectionShell title={t("runners.title")} testId="my-runner-runners" result={result} onRetry={reload}>
      {(runners) =>
        runners.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="my-runner-runners-empty">{t("runners.empty")}</p>
        ) : (
          <ul className="grid gap-3 lg:grid-cols-2">
            {runners.map((r) => (
              <li key={r.runner_id} className="space-y-2 rounded-lg border border-border p-3" data-testid={`my-runner-runner-${r.runner_id}`}>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="min-w-0 truncate font-medium">{r.name}</span>
                  <Badge variant={r.live ? "success" : "warning"}>{t(r.live ? "runners.live" : "runners.lapsed")}</Badge>
                </div>
                <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
                  <dt className="text-muted-foreground">{t("runners.host")}</dt>
                  <dd className="min-w-0 break-all">{r.host}</dd>
                  <dt className="text-muted-foreground">{t("runners.version")}</dt>
                  <dd>{r.version || t("runners.unknownVersion")}</dd>
                  <dt className="text-muted-foreground">{t("runners.heartbeat")}</dt>
                  <dd>{t(r.live ? "runners.leaseLive" : "runners.leaseLapsed", { when: formatRelativeTime(r.lease_expires_at) })}</dd>
                  <dt className="text-muted-foreground">{t("runners.agents")}</dt>
                  <dd className="flex flex-wrap gap-1">
                    {r.agents.map((a) => <Badge key={a.agent_id} variant="secondary">{a.name}</Badge>)}
                  </dd>
                </dl>
              </li>
            ))}
          </ul>
        )
      }
    </SectionShell>
  );
}

import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { SectionShell } from "@/components/my-runner/section-shell";
import { useLoad, type Loaded } from "@/components/my-runner/use-load";
import { formatRelativeTime } from "@/i18n/format";
import { controllerSync, fetchWorkspaceRuntime, type ControllerState, type RuntimeController, type RuntimeLoad, type RuntimeState } from "@/lib/agent-runtime";
import type { OperatorRunner } from "@/lib/my-runner";

const SYNC_VARIANT = { applied: "success", stale: "warning", paused: "warning", error: "destructive", unknown: "outline" } as const;

/** One runner on the screen: the lease that leads agents and the catalog controller with its report, whichever exist. */
export interface RunnerRow {
  key: string;
  runner?: OperatorRunner;
  controller?: { ref: string; entry: RuntimeController; report?: ControllerState };
}

/**
 * The runner that leads agents (lease) and the controller of the workspace
 * catalog that reports its applied revision are the same runner: a report's
 * controller_ref is the runner id of a report-derived lease, and the name an
 * announcing runner gives itself. They become one row; a controller nothing
 * leads through yet is a row of its own.
 */
export function mergeRunners(runners: OperatorRunner[], state: RuntimeState | undefined): RunnerRow[] {
  const controllers = state ? Object.entries(state.catalog.controllers) : [];
  const used = new Set<string>();
  const ctl = (ref: string, entry: RuntimeController) => {
    used.add(ref);
    return { ref, entry, report: state?.controllers.find((c) => c.controller_ref === ref) };
  };
  const rows: RunnerRow[] = runners.map((r) => {
    const hit = controllers.find(([ref]) => !used.has(ref) && ref === r.runner_id)
      ?? controllers.find(([ref]) => !used.has(ref) && ref === r.name);
    return { key: r.runner_id, runner: r, controller: hit ? ctl(hit[0], hit[1]) : undefined };
  });
  for (const [ref, entry] of controllers) {
    if (!used.has(ref)) rows.push({ key: ref, controller: ctl(ref, entry) });
  }
  return rows;
}

export function RunnersSection({ result, reload, wsId }: { result: Loaded<OperatorRunner[]>; reload: () => void; wsId?: string }) {
  const { t } = useTranslation("runner");
  // The runtime route answers workspace admins only; everyone else (403) and a workspace
  // without a runner connection see the leases alone.
  const runtime = useLoad<RuntimeLoad>(
    () => (wsId ? fetchWorkspaceRuntime(wsId) : Promise.resolve({ kind: "forbidden" })),
    wsId,
  );
  const rt = runtime.result;
  const state = rt.state === "ready" && rt.data.kind === "ok" ? rt.data.state : undefined;
  return (
    <SectionShell title={t("runners.title")} testId="my-runner-runners" result={result} onRetry={() => { reload(); runtime.reload(); }}>
      {(runners) => {
        const rows = mergeRunners(runners, state);
        return (
          <>
            {rt.state === "ready" && rt.data.kind === "unavailable" && (
              <p role="alert" className="mb-3 text-sm text-destructive">{t("loadFailed")}</p>
            )}
            {rows.length === 0 ? (
              <p className="text-sm text-muted-foreground" data-testid="my-runner-runners-empty">{t("runners.empty")}</p>
            ) : (
              <ul className="grid gap-3 lg:grid-cols-2">
                {rows.map((row) => <RunnerItem key={row.key} row={row} state={state} />)}
              </ul>
            )}
          </>
        );
      }}
    </SectionShell>
  );
}

function RunnerItem({ row, state }: { row: RunnerRow; state: RuntimeState | undefined }) {
  const { t } = useTranslation("runner");
  const r = row.runner;
  const c = row.controller;
  const host = r?.host || c?.entry.host || "";
  const version = r?.version || c?.entry.version || "";
  return (
    <li className="space-y-2 rounded-lg border border-border p-3" data-testid={`my-runner-runner-${row.key}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="min-w-0 truncate font-medium">{r?.name || c?.ref}</span>
        {r && <Badge variant={r.live ? "success" : "warning"}>{t(r.live ? "runners.live" : "runners.lapsed")}</Badge>}
      </div>
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
        <dt className="text-muted-foreground">{t("runners.host")}</dt>
        <dd className="min-w-0 break-all">{host}</dd>
        <dt className="text-muted-foreground">{t("runners.version")}</dt>
        <dd className="whitespace-nowrap">{version || t("runners.unknownVersion")}</dd>
        {r && (
          <>
            <dt className="text-muted-foreground">{t("runners.heartbeat")}</dt>
            <dd>{t(r.live ? "runners.leaseLive" : "runners.leaseLapsed", { when: formatRelativeTime(r.lease_expires_at) })}</dd>
            <dt className="text-muted-foreground">{t("runners.agents")}</dt>
            <dd className="flex flex-wrap gap-1">
              {r.agents.map((a) => <Badge key={a.agent_id} variant="secondary">{a.name}</Badge>)}
            </dd>
          </>
        )}
      </dl>
      {c && state && <ControllerLine ctl={c} state={state} />}
    </li>
  );
}

/** Whether this runner runs the workspace catalog's desired revision (its controller report). */
function ControllerLine({ ctl, state }: { ctl: NonNullable<RunnerRow["controller"]>; state: RuntimeState }) {
  const { t } = useTranslation("runner");
  const sync = controllerSync(ctl.report, state.revision, state.digest);
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-border pt-2 text-sm" data-testid={`my-runner-controller-${ctl.ref}`}>
      <span className="text-muted-foreground">{t("runners.sync")}</span>
      <Badge variant={SYNC_VARIANT[sync.label]}>{t(`controllers.sync.${sync.label}`)}</Badge>
      <span>
        {ctl.report
          ? t("controllers.revision", { applied: ctl.report.report.revision, desired: state.revision })
          : t("controllers.desiredOnly", { desired: state.revision })}
      </span>
      <span className="text-muted-foreground">
        {ctl.report ? t("controllers.heartbeat", { when: formatRelativeTime(ctl.report.received_at) }) : t("controllers.noHeartbeat")}
      </span>
      {!ctl.entry.enabled && <span className="text-xs font-medium text-warning">{t("controllers.disabled")}</span>}
    </div>
  );
}

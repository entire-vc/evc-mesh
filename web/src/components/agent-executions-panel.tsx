import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Copy } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { useAgentExecutions } from "@/hooks/use-agent-executions";
import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import {
  attemptRows,
  filterAttemptRows,
  modelEffort,
  observedModel,
  routeMode,
  sortAttemptRows,
  type AgentExecutionView,
  type AttemptRow,
  type AttemptScope,
} from "@/lib/agent-executions";
import { useProjectStore } from "@/stores/project";

const PAGE = 20;

/**
 * Detail tab: the same scoped projection as the org chart, one agent. Configured
 * policy (with revision) is a separate section from observed runs; history is
 * separated from active runs. Read-only: there is nothing here to "switch".
 */
export function AgentExecutionsPanel({ agentId, workspaceId }: { agentId: string; workspaceId: string | null }) {
  const { t } = useTranslation("integrations");
  const projects = useProjectStore((s) => s.projects);
  const { load, reload } = useAgentExecutions(workspaceId, { agentId });
  const [projectId, setProjectId] = useState("");
  const [scope, setScope] = useState<AttemptScope>("all");
  const [query, setQuery] = useState("");
  const [shown, setShown] = useState(PAGE);

  const items = load !== "loading" && load.kind === "ok" ? load.items : null;
  const projectName = (id: string) => projects.find((p) => p.id === id)?.name ?? id;
  const rows = useMemo(() => (items ? attemptRows(items) : []), [items]);
  const filtered = useMemo(
    () => sortAttemptRows(filterAttemptRows(rows, { projectId: projectId || null, scope, query })),
    [rows, projectId, scope, query],
  );
  const visibleViews = (items ?? []).filter((v) => !projectId || v.project_id === projectId);

  if (load === "loading") {
    return (
      <div aria-busy="true" aria-label={t("executions.loading")} className="space-y-2">
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }
  if (load.kind === "error") {
    return (
      <div role="alert" data-testid="exec-error" className="space-y-2 rounded-md border border-destructive/40 p-3">
        <p className="text-sm text-destructive">{t("executions.error")}</p>
        <Button size="sm" variant="outline" onClick={reload}>
          {t("executions.retry")}
        </Button>
      </div>
    );
  }
  if (load.kind === "forbidden") {
    return <Note>{t("executions.forbidden")}</Note>;
  }

  const projectIds = [...new Set(load.items.map((v) => v.project_id))];
  const active = filtered.filter((r) => r.active);
  const history = filtered.filter((r) => !r.active);
  const page = filtered.slice(0, shown);
  const pageActive = page.filter((r) => r.active);
  const pageHistory = page.filter((r) => !r.active);

  return (
    <div className="space-y-5">
      {load.partial && <Note tone="warn">{t("executions.partial")}</Note>}
      <p className="text-xs text-muted-foreground">{t("executions.profileEditNote")}</p>

      {load.items.length === 0 ? (
        <Note>{t("executions.panel.noViews")}</Note>
      ) : (
        <>
          <div className="flex flex-wrap items-end gap-2">
            <label className="space-y-1 text-xs text-muted-foreground">
              {t("executions.project")}
              <Select value={projectId} onChange={(e) => { setProjectId(e.target.value); setShown(PAGE); }} className="h-8 w-44 text-xs">
                <option value="">{t("executions.allProjects")}</option>
                {projectIds.map((id) => (
                  <option key={id} value={id}>
                    {projectName(id)}
                  </option>
                ))}
              </Select>
            </label>
            <label className="space-y-1 text-xs text-muted-foreground">
              {t("executions.panel.scope")}
              <Select value={scope} onChange={(e) => { setScope(e.target.value as AttemptScope); setShown(PAGE); }} className="h-8 w-36 text-xs">
                {(["all", "active", "history"] as const).map((s) => (
                  <option key={s} value={s}>
                    {t(`executions.panel.scopes.${s}`)}
                  </option>
                ))}
              </Select>
            </label>
            <Input
              aria-label={t("executions.panel.search")}
              placeholder={t("executions.panel.search")}
              value={query}
              onChange={(e) => { setQuery(e.target.value); setShown(PAGE); }}
              className="h-8 min-w-0 flex-1 basis-48 text-xs"
            />
          </div>

          <section aria-label={t("executions.panel.configuredHeading")} className="space-y-2">
            <Heading>{t("executions.panel.configuredHeading")}</Heading>
            {visibleViews.map((v) => (
              <ConfiguredBlock key={v.project_id} view={v} projectName={projectName(v.project_id)} />
            ))}
          </section>

          <section aria-label={t("executions.panel.attemptsHeading")} className="space-y-2">
            <Heading>{t("executions.panel.attemptsHeading")}</Heading>
            {filtered.length === 0 ? (
              <Note>{rows.length === 0 ? t("executions.panel.noAttempts") : t("executions.panel.noMatch")}</Note>
            ) : (
              <div className="space-y-4" data-testid="exec-rows">
                <RowGroup title={t("executions.panel.activeHeading")} count={active.length} rows={pageActive} projectName={projectName} />
                <RowGroup title={t("executions.panel.historyHeading")} count={history.length} rows={pageHistory} projectName={projectName} muted />
                {filtered.length > shown && (
                  <Button size="sm" variant="outline" onClick={() => setShown((n) => n + PAGE)}>
                    {t("executions.panel.showMore", { count: Math.min(PAGE, filtered.length - shown) })}
                  </Button>
                )}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  );
}

function Heading({ children }: { children: ReactNode }) {
  return <h3 className="text-sm font-medium">{children}</h3>;
}

function Note({ children, tone }: { children: ReactNode; tone?: "warn" }) {
  return (
    <p
      role="status"
      className={cn(
        "rounded-md border border-dashed border-border p-3 text-sm text-muted-foreground",
        tone === "warn" && "border-tone-attention-500 text-tone-attention-600",
      )}
    >
      {children}
    </p>
  );
}

function ConfiguredBlock({ view, projectName }: { view: AgentExecutionView; projectName: string }) {
  const { t } = useTranslation("integrations");
  const mode = routeMode(view);
  return (
    <div data-testid={`exec-view-${view.project_id}`} className="rounded-md border border-border p-2.5 text-sm">
      <p className="font-medium break-words">{projectName}</p>
      {mode !== "managed" ? (
        <div data-testid="exec-unmanaged" className="mt-1 space-y-0.5 text-muted-foreground">
          <p>{mode === "disabled" ? t("executions.disabledHint") : t("executions.unmanagedHint")}</p>
          <p className="font-mono text-xs break-words">
            {view.profile?.model
              ? t("executions.profileFrom", { value: [view.profile.harness, view.profile.model].filter(Boolean).join(" · "), source: view.profile.source })
              : t("executions.profileNone", { source: view.profile?.source ?? "agent_profile" })}
          </p>
        </div>
      ) : (
        <div data-testid="exec-configured" className="mt-1 space-y-1">
          <p className="text-xs text-muted-foreground">
            {t("executions.panel.policyRevision", { rev: view.configured?.revision != null ? t("executions.revision", { n: view.configured.revision }) : "?" })}
            {view.configured?.digest && ` · ${t("executions.panel.digest", { digest: view.configured.digest.slice(0, 12) })}`}
            {view.configured?.source && ` · ${view.configured.source}`}
          </p>
          <ol className="space-y-1">
            {(view.configured?.routes ?? []).map((r) => (
              <li key={`${r.binding_ref}:${r.profile_ref}:${r.order}`} className="flex flex-wrap gap-x-2 gap-y-0.5 text-xs">
                <span className="font-medium">{t("executions.panel.routeOrder", { order: r.order + 1, profile: r.profile_ref })}</span>
                <span className="font-mono break-all text-muted-foreground">
                  {[r.harness, r.model, r.effort ?? t("executions.effortNotSet")].filter(Boolean).join(" · ")}
                </span>
                <span className="break-all text-muted-foreground">{r.controller_ref}</span>
              </li>
            ))}
          </ol>
        </div>
      )}
    </div>
  );
}

function RowGroup({
  title,
  count,
  rows,
  projectName,
  muted,
}: {
  title: string;
  count: number;
  rows: AttemptRow[];
  projectName: (id: string) => string;
  muted?: boolean;
}) {
  if (count === 0) return null;
  return (
    <div className={cn("space-y-2", muted && "border-t border-border pt-3")}>
      <p className="text-xs font-medium text-muted-foreground">
        {title} ({count})
      </p>
      <ul className="space-y-2">
        {rows.map((r) => (
          <AttemptItem key={`${r.view.project_id}:${r.attempt.attempt_id}`} row={r} projectName={projectName(r.view.project_id)} />
        ))}
      </ul>
    </div>
  );
}

function AttemptItem({ row, projectName }: { row: AttemptRow; projectName: string }) {
  const { t } = useTranslation("integrations");
  const { attempt: a, view, active } = row;
  const model = modelEffort(observedModel(a), a.observed.effort);
  const currentRev = view.configured?.revision ?? null;
  const runRev = a.resolved_route?.revision ?? null;
  const drift = a.policy_matches === false || (currentRev != null && runRev != null && currentRev !== runRev);
  return (
    <li data-testid="exec-attempt" className={cn("space-y-1.5 rounded-md border border-border p-2.5 text-sm", !active && "opacity-80")}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className={cn("font-mono break-all", !model && "font-sans italic text-muted-foreground")}>{model ?? t("executions.modelUnknown")}</span>
        {a.observed.harness && <span className="text-xs text-muted-foreground">{a.observed.harness}</span>}
        <Badge variant={active ? "secondary" : "outline"} className="text-xs">
          {t(`executions.panel.states.${a.state}`)}
        </Badge>
        {(a.freshness === "stale" || a.freshness === "offline") && (
          <Badge variant="outline" className="text-xs text-tone-attention-600">
            {t(`executions.panel.freshness.${a.freshness}`)}
          </Badge>
        )}
        <span className="ml-auto max-w-full break-words text-xs text-muted-foreground">{projectName}</span>
      </div>
      <p className="text-xs text-muted-foreground">
        {a.observed.observed_at
          ? t("executions.panel.observed", { when: formatRelative(a.observed.observed_at), source: a.observed.source ?? "?" })
          : a.observed.source
          ? t("executions.panel.notObservedSource", { source: a.observed.source })
          : t("executions.panel.notObserved")}
        {a.heartbeat_at && ` · ${t("executions.panel.heartbeat", { when: formatRelative(a.heartbeat_at) })}`}
      </p>
      {(runRev != null || a.resolved_route?.profile_ref) && (
        <p className="text-xs text-muted-foreground">
          {a.resolved_route?.source}
          {runRev != null && ` ${t("executions.revision", { n: runRev })}`}
          {a.resolved_route?.profile_ref && ` · ${a.resolved_route.profile_ref}`}
        </p>
      )}
      {drift && (
        <p className="text-xs text-tone-attention-600">
          {t("executions.panel.mismatch", {
            run: runRev != null ? t("executions.revision", { n: runRev }) : "?",
            current: currentRev != null ? t("executions.revision", { n: currentRev }) : "?",
          })}
        </p>
      )}
      <dl className="grid gap-x-3 gap-y-0.5 text-xs sm:grid-cols-[max-content_1fr]">
        <IdRow label={t("executions.panel.ids.attempt")} value={a.attempt_id} />
        {a.turn_id && <IdRow label={t("executions.panel.ids.turn")} value={a.turn_id} />}
        {a.task_id && <IdRow label={t("executions.panel.ids.task")} value={a.task_id} />}
        {a.controller_ref && <IdRow label={t("executions.panel.ids.controller")} value={a.controller_ref} />}
        {a.instance_id && (
          <IdRow
            label={t("executions.panel.ids.instance")}
            value={a.instance_id}
            suffix={a.generation != null ? `g${a.generation}` : undefined}
          />
        )}
      </dl>
    </li>
  );
}

function IdRow({ label, value, suffix }: { label: string; value: string; suffix?: string }) {
  const { t } = useTranslation("integrations");
  const [copied, setCopied] = useState(false);
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="flex min-w-0 items-start gap-1.5">
        <span className="min-w-0 break-all font-mono">{value}{suffix && <span className="text-muted-foreground"> · {suffix}</span>}</span>
        <button
          type="button"
          aria-label={t("executions.panel.copy", { label })}
          title={copied ? t("executions.panel.copied") : t("executions.panel.copy", { label })}
          onClick={() => {
            void copyText(value).then(() => setCopied(true));
          }}
          className="mt-0.5 shrink-0 rounded text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Copy className="h-3 w-3" />
        </button>
      </dd>
    </>
  );
}

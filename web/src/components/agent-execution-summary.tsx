import { useState } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import {
  modelEffort,
  observedModel,
  routeMode,
  type AgentExecutionSummary,
  type ExecutionAttempt,
} from "@/lib/agent-executions";

const configuredLabel = (c: AgentExecutionSummary["configured"][number]) =>
  [c.model ?? c.harness, c.effort].filter(Boolean).join(" · ");

/**
 * Org chart card block: configured policy on one line, what actually runs on
 * the next. The two never share a value — an unobserved model is "unknown".
 */
export function ExecutionCardSummary({
  summary,
  projectName,
}: {
  summary: AgentExecutionSummary;
  projectName: (projectId: string) => string;
}) {
  const { t } = useTranslation("integrations");
  const [open, setOpen] = useState(false);

  if (summary.mode !== "managed") {
    const p = summary.profile;
    return (
      <div data-testid="exec-unmanaged" className="space-y-0.5 text-[11px] text-muted-foreground">
        <p className="font-medium">{summary.mode === "disabled" ? t("executions.disabled") : t("executions.unmanaged")}</p>
        <p className="font-mono break-words" title={p ? `${p.harness ?? ""} ${p.model ?? ""}` : undefined}>
          {p?.model
            ? t("executions.profileFrom", { value: [p.harness, p.model].filter(Boolean).join(" · "), source: p.source })
            : t("executions.profileNone", { source: p?.source ?? "agent_profile" })}
        </p>
      </div>
    );
  }

  const active = summary.active;
  // Offline is reported even when another project still has an active run: one live run must not hide a lost one.
  const offline = summary.views.some((v) => v.freshness === "offline" || v.attempts.some((a) => a.freshness === "offline"));
  const otherModes = summary.mixed ? summary.views.filter((v) => routeMode(v) !== "managed") : [];
  const drift = active.some((a) => a.attempt.policy_matches === false);

  return (
    <div className="space-y-1 text-[11px]">
      <p data-testid="exec-configured" className="text-muted-foreground">
        <span className="font-medium">{t("executions.configured")}</span>{" "}
        {summary.configuredRevisions.length > 0 && (
          <span className="tabular-nums">{summary.configuredRevisions.map((r) => t("executions.revision", { n: r })).join(", ")} </span>
        )}
        <span className="font-mono break-words">{summary.configured.map(configuredLabel).join(", ")}</span>
        {summary.configuredSources.length > 0 && <span data-testid="exec-configured-source"> · {summary.configuredSources.join(", ")}</span>}
      </p>
      <div data-testid="exec-observed" className="text-muted-foreground">
        {active.length === 0 ? (
          <p className={cn(offline && "text-tone-attention-600")}>{offline ? t("executions.offline") : t("executions.noActive")}</p>
        ) : active.length === 1 ? (
          <AttemptLine a={active[0]!.attempt} />
        ) : (
          <>
            <button
              type="button"
              aria-expanded={open}
              onClick={(e) => {
                e.stopPropagation();
                setOpen((v) => !v);
              }}
              className="rounded font-medium underline decoration-dotted underline-offset-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {t("executions.runs", { count: active.length })}
            </button>
            {open && (
              <ul data-testid="exec-attempts" className="mt-1 space-y-1">
                {active.map(({ view, attempt }) => (
                  <li key={`${view.project_id}:${attempt.attempt_id}`} className="rounded border border-border px-1.5 py-1">
                    <AttemptLine a={attempt} />
                    <span className="block truncate" title={projectName(view.project_id)}>
                      {projectName(view.project_id)}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </>
        )}
        {drift && <p className="text-tone-attention-600">{t("executions.driftsFromPolicy")}</p>}
        {offline && active.length > 0 && (
          <p data-testid="exec-offline" className="text-tone-attention-600">
            {t("executions.offline")}
          </p>
        )}
        {otherModes.length > 0 && (
          <ul data-testid="exec-other-modes" className="space-y-0.5">
            {otherModes.map((v) => (
              <li key={v.project_id} className="break-words">
                {projectName(v.project_id)}: {routeMode(v) === "disabled" ? t("executions.disabled") : t("executions.unmanaged")}
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function AttemptLine({ a }: { a: ExecutionAttempt }) {
  const { t } = useTranslation("integrations");
  const label = modelEffort(observedModel(a), a.observed.effort);
  return (
    <>
      <p>
        <span className={cn("font-mono break-words", !label && "font-sans italic")}>{label ?? t("executions.modelUnknown")}</span>
        {a.freshness === "stale" && <span className="text-tone-attention-600"> · {t("executions.stale")}</span>}
      </p>
      <p data-testid="exec-observation" className="break-words">
        {a.observed.observed_at
          ? t("executions.panel.observed", { when: formatRelative(a.observed.observed_at), source: a.observed.source ?? "?" })
          : a.observed.source
          ? t("executions.panel.notObservedSource", { source: a.observed.source })
          : t("executions.panel.notObserved")}
      </p>
    </>
  );
}

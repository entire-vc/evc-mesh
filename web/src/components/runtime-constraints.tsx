import { useTranslation } from "react-i18next";
import { cn } from "@/lib/cn";
import { constraintViews, isUnverifiedDiagnostic, sameTarget, stateReasonDetail, waitBrief, type ConstraintView } from "@/lib/runtime-state";
import type { RuntimeState } from "@/types";

interface Props {
  state: RuntimeState;
  /** Task detail only: the kanban tile never renders constraints. */
  variant?: "full";
  /** Task detail: the target is already named by the "Now" brief, so a constraint repeating it is not printed twice (it stays in technical details). */
  hideBriefTarget?: boolean;
  className?: string;
}

function RefLink({ view, hidden = false }: { view: ConstraintView; hidden?: boolean }) {
  const ref = view.ref;
  // `hidden`: the brief above already names this target; the row keeps only its own facts.
  if (!ref || hidden) return null;
  const cls = "min-w-0 break-all";
  return ref.href ? (
    <a
      href={ref.href}
      className={cn(cls, "underline underline-offset-2")}
      target={ref.href.startsWith("http") ? "_blank" : undefined}
      rel={ref.href.startsWith("http") ? "noreferrer" : undefined}
      onClick={(e) => e.stopPropagation()}
      data-testid="constraint-ref"
    >
      {ref.label}
    </a>
  ) : (
    <span className={cls} data-testid="constraint-ref">
      {ref.label}
    </span>
  );
}

/**
 * All operational constraints of a card: why it stands, who lifts it, what wakes it.
 * Rendered in task detail only; the kanban tile shows the kind word and nothing else.
 */
export function RuntimeConstraints({ state, variant = "full", hideBriefTarget = false, className }: Props) {
  const { t } = useTranslation("board");
  const views = constraintViews(state);
  const fullReason = hideBriefTarget ? stateReasonDetail(state) : null;
  const target = hideBriefTarget ? waitBrief(state) : null;
  if (views.length === 0 && !fullReason && !target) return null;
  const unknown = t("runtimeState.constraints.nextActorUnknown");
  // Only ONE plain constraint repeats the brief: stale, CI and redacted ones carry facts of their own and stay as rows.
  const dup = target ? views.find((v) => sameTarget(v.ref?.label, target.ref.label) && !v.stale && !v.ci) : undefined;
  const main = dup ? views.filter((v) => v !== dup) : views;
  const incomplete = hideBriefTarget && isUnverifiedDiagnostic(state);

  return (
    <div className={cn("flex min-w-0 flex-col gap-1", className)}>
      {main.length > 0 && (
        <ul className="m-0 flex min-w-0 list-none flex-col gap-1 p-0" data-testid="runtime-constraints" data-variant={variant}>
          {main.map((v) => (
            <li
              key={v.key}
              className={cn("min-w-0 max-w-full text-[11px] leading-snug", v.stale ? "text-muted-foreground opacity-60" : "text-foreground")}
              data-testid="runtime-constraint"
              data-constraint-key={v.key}
              data-constraint-class={v.classKey}
              data-constraint-stale={v.stale ? "true" : "false"}
            >
              <div className="flex min-w-0 flex-wrap items-baseline gap-x-1.5">
                <span className="shrink-0 font-medium text-muted-foreground" data-testid="constraint-class">
                  {v.classLabel}
                </span>
                <span className="min-w-0 break-words" data-testid="constraint-reason">
                  {v.reason ?? t("runtimeState.constraints.restricted")}
                </span>
                <RefLink view={v} hidden={!!target && sameTarget(v.ref?.label, target.ref.label)} />
                {v.stale && (
                  <span className="italic" data-testid="constraint-stale">
                    {t("runtimeState.stale")}
                  </span>
                )}
              </div>
              {v.ci && (
                <div className="min-w-0 break-words text-muted-foreground" data-testid="constraint-ci">
                  {[v.ci.label, ...v.ci.details].join(", ")}
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
      {incomplete && (
        <div className="text-[11px] text-muted-foreground" data-testid="runtime-incomplete">
          {t("runtimeState.constraints.incomplete")}
        </div>
      )}
      {/* Next / Wakes on / Source / time are service facts: collapsed, native <details> works with keyboard, screen reader and touch. */}
      <details className="min-w-0 text-[11px] text-muted-foreground" data-testid="runtime-technical">
        <summary className="cursor-pointer select-none py-0.5">{t("runtimeState.constraints.technical")}</summary>
        {fullReason && (
          <p className="m-0 mt-0.5 min-w-0 break-words" data-testid="runtime-technical-reason">
            {fullReason}
          </p>
        )}
        {target && (
          <dl className="m-0 mt-0.5 grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-2">
            <dt>{t("runtimeState.constraints.target")}</dt>
            <dd className="m-0 break-all" data-testid="runtime-technical-target">
              {target.raw}
            </dd>
          </dl>
        )}
        {views.map((v) => (
          <dl key={v.key} className="m-0 mt-0.5 grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-2" data-testid="runtime-technical-item" data-constraint-key={v.key} data-constraint-class={v.classKey} data-constraint-stale={v.stale ? "true" : "false"}>
            <dt>{v.classLabel}</dt>
            <dd className="m-0 break-words">{v.reasonFull ?? v.reason ?? t("runtimeState.constraints.restricted")}</dd>
            <dt>{t("runtimeState.constraints.nextActor")}</dt>
            <dd className="m-0 break-words" data-testid="constraint-next-actor">
              {v.nextActor ?? unknown}
            </dd>
            <dt>{t("runtimeState.constraints.wake")}</dt>
            <dd className="m-0 break-all" data-testid="constraint-wake">
              {v.wake ?? t("runtimeState.constraints.wakeUnknown")}
            </dd>
            {v.rawRef && (
              <>
                <dt>{t("runtimeState.constraints.target")}</dt>
                <dd className="m-0 break-all" data-testid="constraint-target">
                  {v.rawRef}
                </dd>
              </>
            )}
            <dt>{t("runtimeState.constraints.source")}</dt>
            <dd className="m-0 break-all" data-testid="constraint-source">
              {v.source ?? unknown}
              {" · "}
              {v.observedAt ? t("runtimeState.constraints.observed", { time: v.observedAt }) : t("runtimeState.constraints.observedUnknown")}
            </dd>
          </dl>
        ))}
      </details>
    </div>
  );
}

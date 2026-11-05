import { useTranslation } from "react-i18next";
import { cn } from "@/lib/cn";
import { constraintViews, type ConstraintView } from "@/lib/runtime-state";
import type { RuntimeState } from "@/types";

interface Props {
  state: RuntimeState;
  /** Task detail only: the kanban tile never renders constraints. */
  variant?: "full";
  className?: string;
}

function RefLink({ view }: { view: ConstraintView }) {
  const ref = view.ref;
  if (!ref) return null;
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
export function RuntimeConstraints({ state, variant = "full", className }: Props) {
  const { t } = useTranslation("board");
  const views = constraintViews(state);
  if (views.length === 0) return null;
  const unknown = t("runtimeState.constraints.nextActorUnknown");

  return (
    <ul className={cn("m-0 flex min-w-0 list-none flex-col gap-1 p-0", className)} data-testid="runtime-constraints" data-variant={variant}>
      {views.map((v) => (
        <li
          key={v.key}
          className={cn(
            "min-w-0 max-w-full text-[11px] leading-snug",
            v.stale ? "text-muted-foreground opacity-60" : "text-foreground",
          )}
          data-testid="runtime-constraint"
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
            <RefLink view={v} />
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
          {variant === "full" && (
            <dl className="m-0 mt-0.5 grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-2 text-muted-foreground">
              <dt>{t("runtimeState.constraints.nextActor")}</dt>
              <dd className="m-0 break-words" data-testid="constraint-next-actor">
                {v.nextActor ?? unknown}
              </dd>
              <dt>{t("runtimeState.constraints.wake")}</dt>
              <dd className="m-0 break-all" data-testid="constraint-wake">
                {v.wake ?? t("runtimeState.constraints.wakeUnknown")}
              </dd>
              <dt>{t("runtimeState.constraints.source")}</dt>
              <dd className="m-0 break-all" data-testid="constraint-source">
                {v.source ?? unknown}
                {" · "}
                {v.observedAt
                  ? t("runtimeState.constraints.observed", { time: v.observedAt })
                  : t("runtimeState.constraints.observedUnknown")}
              </dd>
            </dl>
          )}
        </li>
      ))}
    </ul>
  );
}

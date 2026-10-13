import { CirclePause, Clock, Hourglass, Play } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/cn";
import {
  parseRuntimeRef,
  runtimeStaleNote,
  runtimeKindTone,
  runtimeStateText,
} from "@/lib/runtime-state";
import type { RuntimeState, RuntimeStateKind } from "@/types";

const icons = {
  working: Play,
  waiting: Hourglass,
  queued: Clock,
  stopped: CirclePause,
} satisfies Record<RuntimeStateKind, unknown>;

interface Props {
  state: RuntimeState;
  /** `badge` is the dense board form; `line` is the card-header row. */
  variant?: "badge" | "line";
  className?: string;
}

/**
 * Live state of the work behind a card. Shown next to — never instead of — the
 * Mesh status. A stale value is muted and says so: a dead publisher must not
 * paint a confident "working" on the board.
 */
export function RuntimeStateBadge({ state, variant = "badge", className }: Props) {
  useTranslation("board"); // re-render on language change; the texts come from runtime-state.ts
  const Icon = icons[state.kind];
  const ref = parseRuntimeRef(state.ref);
  const text = runtimeStateText(state);
  const title = state.stale ? `${text} (${runtimeStaleNote()})` : text;

  return (
    <span
      className={cn(
        "inline-flex min-w-0 items-center gap-1 text-xs",
        variant === "badge" && "max-w-full text-[11px]",
        state.stale ? "text-muted-foreground opacity-60" : runtimeKindTone[state.kind],
        className,
      )}
      title={title}
      data-testid="runtime-state"
      data-runtime-kind={state.kind}
      data-runtime-stale={state.stale ? "true" : "false"}
    >
      <Icon className="h-3 w-3 shrink-0" aria-hidden="true" />
      <span className={cn(variant === "badge" && "truncate")}>{text}</span>
      {ref &&
        (ref.href ? (
          <a
            href={ref.href}
            className="shrink-0 underline underline-offset-2"
            target={ref.href.startsWith("http") ? "_blank" : undefined}
            rel={ref.href.startsWith("http") ? "noreferrer" : undefined}
            onClick={(e) => e.stopPropagation()}
            data-testid="runtime-state-ref"
          >
            {ref.label}
          </a>
        ) : (
          <span className="shrink-0" data-testid="runtime-state-ref">
            {ref.label}
          </span>
        ))}
      {state.stale && (
        <span className="shrink-0 italic" data-testid="runtime-state-stale">
          · {runtimeStaleNote()}
        </span>
      )}
    </span>
  );
}

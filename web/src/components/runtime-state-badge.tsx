import { CirclePause, Clock, Hourglass, Play } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/cn";
import {
  safeRef,
  runtimeStaleNote,
  runtimeKindTone,
  runtimeStateText,
  runtimeKindLabel,
  waitBrief,
  waitingBoardText,
  briefReason,
  isUnverifiedDiagnostic,
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
  variant?: "badge" | "line" | "kind";
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
  const compact = variant === "kind";
  // A waiting card with a structured target gets a brief ("Waiting for MR !12"); no target → the neutral wording.
  const brief = variant === "badge" ? null : waitBrief(state);
  // Detail line of a waiting card: neutral wording + a SHORT reason when no target is known; the full reason lives in technical details.
  const plainWait = variant === "line" && !brief && state.kind === "waiting" && !isUnverifiedDiagnostic(state);
  const briefReasonText = (brief && !compact) || plainWait ? briefReason(state) : null;
  // Without a recognised target the detail/tile show no ref at all; only the dense badge keeps one, and it is sanitised.
  const ref = brief ? brief.ref : variant === "badge" ? safeRef(state.ref) : null;
  const refText = brief ? brief.short : ref?.label;
  const text = brief
    ? waitingBoardText(state, brief)
    : plainWait
      ? waitingBoardText(state, null)
      : compact
      ? state.kind === "waiting" && state.until
        ? waitingBoardText(state, null)
        : runtimeKindLabel(state)
      : runtimeStateText(state);
  const fullText = [text, brief ? brief.ref.label : null, briefReasonText].filter(Boolean).join(" ");
  const title = state.stale && !compact ? `${fullText} (${runtimeStaleNote()})` : fullText;

  return (
    <span
      className={cn(
        "inline-flex min-w-0 items-center gap-1 text-xs",
        (variant === "badge" || compact) && "max-w-full text-[11px]",
        state.stale ? "text-muted-foreground opacity-60" : runtimeKindTone[state.kind],
        className,
      )}
      title={title}
      data-testid="runtime-state"
      data-runtime-kind={state.kind}
      data-runtime-stale={state.stale ? "true" : "false"}
    >
      <Icon className="h-3 w-3 shrink-0" aria-hidden="true" />
      <span className={cn((variant === "badge" || compact) && "truncate", compact && "min-w-0")}>{text}</span>
      {ref &&
        (ref.href ? (
          <a
            href={ref.href}
            className="shrink-0 underline underline-offset-2"
            target={ref.href.startsWith("http") ? "_blank" : undefined}
            rel={ref.href.startsWith("http") ? "noreferrer" : undefined}
            onClick={(e) => e.stopPropagation()}
            data-testid="runtime-state-ref"
            title={ref.label}
            aria-label={`${text} ${ref.label}`}
          >
            {refText}
          </a>
        ) : (
          <span className="shrink-0" data-testid="runtime-state-ref" title={ref.label}>
            {refText}
          </span>
        ))}
      {briefReasonText && <span className="min-w-0 break-words text-muted-foreground">· {briefReasonText}</span>}
      {state.stale && !compact && (
        <span className="shrink-0 italic" data-testid="runtime-state-stale">
          · {runtimeStaleNote()}
        </span>
      )}
    </span>
  );
}

import { forwardRef, memo, type HTMLAttributes, type MouseEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { CircleCheck, ExternalLink, GitBranch, Link2, Lock, Paperclip, RefreshCw, Snowflake } from "lucide-react";
import { AssigneeAvatarView } from "@/components/assignee-avatar";
import { DelegationLevelBadge } from "@/components/delegation-level-select";
import { PriorityFlag } from "@/components/priority-flag";
import { RuntimeStateBadge } from "@/components/runtime-state-badge";
import { Badge } from "@/components/ui/badge";
import { SlotBadges } from "@/components/extensions/slots/slot-host";
import { Draggable, TaskBadges, TaskLink } from "@entire-vc/mesh-theme-sdk";
import { PerfProfiler } from "@/lib/perf-profiler";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import type { DelegationLevel, Task } from "@/types";
import type { BoardCardView, BoardCardViewData, FieldDescriptor, TaskData } from "@/core/slots/types";

const priorityBorderColors: Record<string, string> = {
  urgent: "border-l-tone-danger-600",
  high: "border-l-tone-high-500",
  medium: "border-l-tone-attention-500",
  low: "border-l-tone-info-500",
  none: "border-l-transparent",
};

/** `t` is the view's own board-namespace translator; the view never touches the global i18n instance. */
export function humanGateTitle(gate: { class: "hard" | "soft"; reason: string }, t: T): string {
  const base = gate.class === "soft" ? t("card.humanGate.soft") : t("card.humanGate.hard");
  return gate.reason ? t("card.humanGate.withReason", { base, reason: gate.reason }) : base;
}

// Fields of the bottom row, in the order chosen. `subtasks` (counters) and `runtime` sit under the title, in that order;
// `artifacts` closes the bottom row, after the VCS marks. Every other field is a row of its own.
const INLINE_FIELDS = new Set(["assignee", "gate", "reviewer", "priority", "delegation", "due_date", "estimate"]);

function formatCustom(value: unknown): string {
  if (value == null || value === "") return "";
  if (Array.isArray(value)) return value.join(", ");
  if (typeof value === "boolean") return value ? "✓" : "";
  return String(value);
}

type T = (key: string, vars?: Record<string, unknown>) => string;

interface CardBodyProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  data: BoardCardViewData;
  isDragging?: boolean;
  /** Title as plain text (drag preview, a card drawn outside a board): no link. */
  plainTitle?: boolean;
  /** The REST task for `task.badge` extensions, when the caller holds it; a board card finds it in the store. */
  badgeTask?: Task;
}

/** The card itself: today's kanban card, drawn from the contract's data only. */
export const CardBody = memo(
  forwardRef<HTMLDivElement, CardBodyProps>(({ data, isDragging, plainTitle, badgeTask, className, ...props }, ref) => {
    const { t } = useTranslation("board");
    const { task, fields } = data;
    const borderColor = priorityBorderColors[task.priority] ?? "border-l-transparent";

    const hasVcsLinks = task.counts.vcsLinks > 0;
    // Links of a provider whose extension owns a badge are drawn by that badge; the generic
    // icon covers the rest. Without provider data (older API) it stays, as before.
    const hasUnownedVcsLinks = task.vcsProvidersKnown
      ? task.vcsProviders.some((p) => !task.vcsOwnedProviders.includes(p))
      : true;

    const rows: ReactNode[] = [];
    const bottom: ReactNode[] = [];
    let runtimeRow: ReactNode = null;
    let subtasks: ReactNode = null;
    let artifacts: ReactNode = null;
    for (const f of fields) {
      if (f.key === "runtime") runtimeRow = rowField(f, task);
      else if (f.key === "subtasks") subtasks = rowField(f, task);
      else if (f.key === "artifacts") artifacts = inlineField(f, task, data.gateStatusName, t as T);
      else if (INLINE_FIELDS.has(f.key)) {
        const node = inlineField(f, task, data.gateStatusName, t as T);
        if (node) bottom.push(node);
      } else {
        const node = rowField(f, task);
        if (node) rows.push(node);
      }
    }
    const showVcs = hasVcsLinks && hasUnownedVcsLinks;

    return (
      <div
        ref={ref}
        className={cn(
          "group relative cursor-pointer rounded-lg border border-border border-l-[3px] bg-card p-3 shadow-sm transition-shadow hover:shadow-md",
          borderColor,
          isDragging && "shadow-lg opacity-90 ring-2 ring-primary/20",
          className,
        )}
        {...props}
      >
        {/* Title row */}
        <div className="flex items-start justify-between gap-1.5">
          <p className="text-sm font-medium leading-snug line-clamp-2 min-w-0 flex items-start gap-1">
            {task.recurring && (
              <span
                title={
                  task.recurring.instanceNumber != null
                    ? t("card.recurringNumber", { number: task.recurring.instanceNumber })
                    : t("card.recurring")
                }
                className="inline-flex items-center"
              >
                <RefreshCw className="mt-0.5 h-3 w-3 shrink-0 text-primary/70" aria-hidden="true" />
              </span>
            )}
            {plainTitle || data.isOverlay ? (
              task.title
            ) : (
              <TaskLink taskId={task.id}>{task.title}</TaskLink>
            )}
          </p>
        </div>

        {subtasks}

        {runtimeRow}

        {rows}

        <div className={cn("flex items-center gap-2", (bottom.length > 0 || showVcs || artifacts) && "mt-1.5")}>
          {bottom}
          <div className="ml-auto flex items-center gap-1 empty:hidden">
            <CardBadges data={task} task={badgeTask} />
            {showVcs && <ExternalLink className="h-3.5 w-3.5 text-muted-foreground" aria-label={t("card.hasVcsLinks")} />}
            {artifacts}
          </div>
        </div>
      </div>
    );
  }),
);
CardBody.displayName = "CardBody";

/** `task.badge` insertion point (required by the contract). */
function CardBadges({ data, task: given }: { data: TaskData; task?: Task }) {
  // The drag overlay already holds the REST task; the slot view rebuilds it from its data.
  return given ? <SlotBadges task={given} /> : <TaskBadges task={data} />;
}

function rowField(f: FieldDescriptor, task: TaskData): ReactNode {
  switch (f.key) {
    case "labels":
      return task.labels.length > 0 ? (
        <div key="labels" className="mt-1.5 flex flex-wrap items-center gap-1">
          {task.labels.map((label) => (
            <Badge key={label} variant="outline" className="text-[10px]">
              {label}
            </Badge>
          ))}
        </div>
      ) : null;
    case "runtime":
      return task.runtimeState ? (
        <div key="runtime" className="mt-1.5 flex min-w-0">
          <RuntimeStateBadge state={task.runtimeState} />
        </div>
      ) : null;
    case "subtasks": {
      const { subtasks, openSubtasks = 0, dependencies = 0, openDependencies = 0 } = task.counts;
      return subtasks > 0 || dependencies > 0 ? (
        <div key="subtasks" className="mt-1.5 flex items-center gap-2 text-xs text-muted-foreground">
          {subtasks > 0 && <CountPair icon={<GitBranch className="h-3.5 w-3.5" aria-hidden="true" />} total={subtasks} open={openSubtasks} />}
          {dependencies > 0 && <CountPair icon={<Link2 className="h-3.5 w-3.5" aria-hidden="true" />} total={dependencies} open={openDependencies} />}
        </div>
      ) : null;
    }
    default: {
      if (!f.key.startsWith("cf:")) return null;
      const text = formatCustom(task.customFields[f.key.slice(3)]);
      return text ? (
        <div key={f.key} className="mt-1.5 flex min-w-0 items-baseline gap-1 text-xs text-muted-foreground" data-field={f.key}>
          <span className="shrink-0">{f.label}:</span>
          <span className="truncate text-foreground">{text}</span>
        </div>
      ) : null;
    }
  }
}

/** «total (open)»: the bracket is the destructive tone and is dropped at zero open. */
function CountPair({ icon, total, open }: { icon: ReactNode; total: number; open: number }) {
  return (
    <span className="flex items-center gap-1">
      {icon}
      <span>{total}</span>
      {open > 0 && <span className="text-destructive">({open})</span>}
    </span>
  );
}

function inlineField(
  f: FieldDescriptor,
  task: TaskData,
  gateStatusName: string | undefined,
  t: T,
): ReactNode {
  switch (f.key) {
    case "assignee":
      return (
        <AssigneeAvatarView
          key="assignee"
          name={task.assignee?.name || undefined}
          shortTag={task.assignee?.shortTag ?? null}
          type={task.assignee?.type ?? "unassigned"}
          size="sm"
        />
      );
    case "gate":
      return <GateMarks key="gate" task={task} gateStatusName={gateStatusName} t={t} />;
    case "reviewer":
      return task.reviewer ? (
        <AssigneeAvatarView
          key="reviewer"
          name={task.reviewer.name || undefined}
          shortTag={task.reviewer.shortTag ?? null}
          type={task.reviewer.type}
          size="sm"
        />
      ) : null;
    case "priority":
      return task.priority !== "none" ? <PriorityFlag key="priority" priority={task.priority} size="sm" /> : null;
    case "delegation":
      return task.delegationLevel && task.delegationLevel !== "review" ? (
        <DelegationLevelBadge key="delegation" value={task.delegationLevel as DelegationLevel} />
      ) : null;
    case "artifacts":
      return task.counts.artifacts > 0 ? (
        <span key="artifacts" className="inline-flex items-center gap-0.5 text-xs text-muted-foreground">
          <Paperclip className="h-3.5 w-3.5" aria-hidden="true" />
          {task.counts.artifacts}
        </span>
      ) : null;
    case "due_date":
      return task.dueDate ? (
        <span key="due" className={cn("text-xs", task.overdue ? "font-medium text-tone-danger-500" : "text-muted-foreground")}>
          {formatRelative(task.dueDate)}
        </span>
      ) : null;
    case "estimate":
      return task.estimatedHours != null ? (
        <span key="estimate" className="text-xs text-muted-foreground">
          {task.estimatedHours}h
        </span>
      ) : null;
    default:
      return null;
  }
}

function GateMarks({ task, gateStatusName, t }: { task: TaskData; gateStatusName?: string; t: T }) {
  const fo = task.falseOpen;
  return (
    <>
      {task.gate && (
        <span
          className="inline-flex items-center text-tone-warning-600 dark:text-tone-warning-400"
          title={humanGateTitle(task.gate, t)}
          data-testid="human-gate-indicator"
          data-human-gate-class={task.gate.class}
        >
          <Lock className="h-3.5 w-3.5" aria-hidden="true" />
          {gateStatusName ? (
            <span className="ml-1 text-[10px] font-medium" data-testid="human-gate-status">
              {t("card.humanGate.awaitingAnswer", { status: gateStatusName })}
            </span>
          ) : (
            <span className="sr-only">{t("card.humanGate.srOnly")}</span>
          )}
        </span>
      )}
      {fo?.allChildrenClosed && (
        <span
          className="inline-flex items-center text-tone-positive-600 dark:text-tone-positive-400"
          title={t("falseOpen.allChildrenClosed", { days: fo.staleDays })}
          data-testid="false-open-indicator"
          data-false-open-kind="all-children-closed"
        >
          <CircleCheck className="h-3.5 w-3.5" aria-hidden="true" />
          <span className="sr-only">{t("card.falseOpen.allChildrenClosed")}</span>
        </span>
      )}
      {fo?.onlyParkedChildrenLeft && (
        <span
          className="inline-flex items-center text-tone-sky-600 dark:text-tone-sky-400"
          title={t("falseOpen.onlyParkedChildrenLeft", { count: fo.openChildrenCount, days: fo.staleDays })}
          data-testid="false-open-indicator"
          data-false-open-kind="only-parked-children-left"
        >
          <Snowflake className="h-3.5 w-3.5" aria-hidden="true" />
          <span className="sr-only">{t("card.falseOpen.onlyParkedChildrenLeft")}</span>
        </span>
      )}
    </>
  );
}

/** A card click opens the task, unless the title link already handled it (plain click) or is the target (ctrl/middle click). */
function isLinkClick(e: MouseEvent): boolean {
  return e.defaultPrevented || Boolean((e.target as HTMLElement).closest("a"));
}

/** `board.card` of `mesh-default`. */
export const BoardCard: BoardCardView = ({ data, actions }) => {
  // The drag overlay is not a board card: no Profiler, as before the split (it would add one board_card commit per drag).
  if (data.isOverlay) return <CardBody data={data} isDragging />;
  const open = (e: MouseEvent) => {
    if (!isLinkClick(e)) actions.openTask();
  };
  return (
    <Draggable item={{ kind: "task", id: data.task.id }} label={data.task.title} data-testid="task-card" data-task-id={data.task.id}>
      {({ isDragging }) => (
        <PerfProfiler id="board-card">
          <CardBody data={data} onClick={open} isDragging={isDragging} />
        </PerfProfiler>
      )}
    </Draggable>
  );
};

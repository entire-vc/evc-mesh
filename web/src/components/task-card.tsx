import { useTranslation } from "react-i18next";
import i18n from "@/i18n";
import { AgentShortTag } from "@/components/agent-short-tag";
import { agentLabel } from "@/lib/agent-label";
import { forwardRef, memo, type HTMLAttributes } from "react";
import { AlignLeft, CircleCheck, ExternalLink, GitBranch, Hand, Lock, Paperclip, Pencil, RefreshCw, Snowflake } from "lucide-react";
import { parseISO } from "date-fns";
import { useAgentShortTag } from "@/hooks/use-agent-short-tag";
import { cn } from "@/lib/cn";
import { Badge } from "@/components/ui/badge";
import { formatRelative } from "@/lib/utils";
import { AssigneeAvatar } from "@/components/assignee-avatar";
import { PriorityFlag } from "@/components/priority-flag";
import { RuntimeStateBadge } from "@/components/runtime-state-badge";
import { readableRuntimeState } from "@/lib/runtime-state";
import { DelegationLevelBadge } from "@/components/delegation-level-select";
import type { Task, StatusCategory } from "@/types";

const priorityBorderColors: Record<string, string> = {
  urgent: "border-l-red-600",
  high: "border-l-orange-500",
  medium: "border-l-yellow-500",
  low: "border-l-blue-500",
  none: "border-l-transparent",
};

const DONE_CATEGORIES = new Set<StatusCategory>(["done", "cancelled"]);

function isOverdue(dueDate: string, statusCategory?: StatusCategory): boolean {
  if (statusCategory && DONE_CATEGORIES.has(statusCategory)) {
    return false;
  }
  return parseISO(dueDate) < new Date();
}

interface TaskCardProps extends HTMLAttributes<HTMLDivElement> {
  task: Task;
  isDragging?: boolean;
  /** Optional status category — used to suppress overdue highlighting for done/cancelled tasks. */
  statusCategory?: StatusCategory;
  /** Called when the explicit edit icon is clicked. Receives the mouse event (already stopped propagation). */
  onEditClick?: (e: React.MouseEvent) => void;
  /**
   * Display name of the checkout holder, when the parent can resolve it. Only
   * used when the holder differs from the assignee — in the usual case the
   * assignee avatar already says who, and repeating it is noise.
   */
  checkedOutByName?: string;
  /**
   * Set when the board shows this gated card in the Triage column instead of
   * its own status column: the card's real status name, rendered next to the
   * gate so the move is never mistaken for a status change.
   */
  gateStatusName?: string;
}

export function humanGateTitle(task: Pick<Task, "human_gate_class" | "gate_reason">): string {
  const base =
    task.human_gate_class === "soft"
      ? i18n.t("board:card.humanGate.soft")
      : i18n.t("board:card.humanGate.hard");
  return task.gate_reason ? i18n.t("board:card.humanGate.withReason", { base, reason: task.gate_reason }) : base;
}

export const TaskCard = memo(
  forwardRef<HTMLDivElement, TaskCardProps>(
    ({ task, isDragging, statusCategory, onEditClick, checkedOutByName, gateStatusName, className, ...props }, ref) => {
    const { t } = useTranslation("board");
    const shortTag = useAgentShortTag(task.assignee_id, task.assignee_type);
    const holderTag = useAgentShortTag(task.checked_out_by, "agent");
    const borderColor =
      priorityBorderColors[task.priority] ?? "border-l-transparent";

    // has_description comes from the server; the local check is the fallback for
    // a cached task object from before the field existed.
    const hasDescription =
      task.has_description ?? Boolean(task.description && task.description.trim().length > 0);
    const hasVcsLinks = (task.vcs_link_count ?? 0) > 0;
    const hasArtifacts = (task.artifact_count ?? 0) > 0;
    const hasSubtasks = (task.subtask_count ?? 0) > 0;
    const labels = task.labels ?? [];

    const dueDateOverdue =
      task.due_date ? isOverdue(task.due_date, statusCategory) : false;

    // A held card and an untouched one used to look identical, which is how a
    // board showing an empty In Progress column came to be read as "nobody
    // started anything" while twelve cards were in agents' hands (#5f9c5117).
    // Live and lapsed are drawn differently on purpose: a lapsed lock means the
    // holder's session died, so the card is NOT being worked despite appearances.
    const checkoutExpiry = task.checked_out_by && task.checkout_expires
      ? parseISO(task.checkout_expires)
      : null;
    const checkoutLapsed = checkoutExpiry !== null && checkoutExpiry < new Date();
    const holderIsSomeoneElse =
      Boolean(checkedOutByName) && task.checked_out_by !== task.assignee_id;

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
            {task.recurring_schedule_id && (
              <span
                title={
                  task.recurring_instance_number != null
                    ? t("card.recurringNumber", { number: task.recurring_instance_number })
                    : t("card.recurring")
                }
                className="inline-flex items-center"
              >
                <RefreshCw
                  className="mt-0.5 h-3 w-3 shrink-0 text-primary/70"
                  aria-hidden="true"
                />
              </span>
            )}
            {task.title}
          </p>
          <div className="flex items-center gap-0.5 shrink-0 mt-0.5">
            {hasVcsLinks && (
              <ExternalLink
                className="h-3.5 w-3.5 text-muted-foreground"
                aria-label={t("card.hasVcsLinks")}
              />
            )}
            {onEditClick && (
              <button
                type="button"
                className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 hover:bg-muted hover:text-foreground focus-visible:opacity-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
                onClick={(e) => { e.stopPropagation(); onEditClick(e); }}
                aria-label={t("card.edit")}
                title={t("card.edit")}
              >
                <Pencil className="h-3.5 w-3.5" />
              </button>
            )}
          </div>
        </div>

        {/* Description indicator */}
        {hasDescription && (
          <div className="mt-1">
            <AlignLeft className="h-3.5 w-3.5 text-muted-foreground" aria-label={t("card.hasDescription")} />
          </div>
        )}

        {/* Labels row */}
        {labels.length > 0 && (
          <div className="mt-1.5 flex flex-wrap items-center gap-1">
            {labels.map((label) => (
              <Badge key={label} variant="outline" className="text-[10px]">
                {label}
              </Badge>
            ))}
          </div>
        )}

        {task.runtime_state && (
          <div className="mt-1.5 flex min-w-0">
            <RuntimeStateBadge state={readableRuntimeState(task)!} />
          </div>
        )}

        {/* Bottom info row: assignee, priority, attachments, due date */}
        <div className="mt-1.5 flex items-center gap-2">
          <AssigneeAvatar
            name={task.assignee_name ?? undefined}
            shortTag={shortTag}
            type={task.assignee_type}
            size="sm"
          />

          {task.human_gate && (
            <span
              className="inline-flex items-center text-amber-600 dark:text-amber-400"
              title={humanGateTitle(task)}
              data-testid="human-gate-indicator"
              data-human-gate-class={task.human_gate_class ?? "hard"}
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

          {task.false_open?.all_children_closed && (
            <span
              className="inline-flex items-center text-emerald-600 dark:text-emerald-400"
              title={t("falseOpen.allChildrenClosed", { days: task.false_open.stale_days })}
              data-testid="false-open-indicator"
              data-false-open-kind="all-children-closed"
            >
              <CircleCheck className="h-3.5 w-3.5" aria-hidden="true" />
              <span className="sr-only">{t("card.falseOpen.allChildrenClosed")}</span>
            </span>
          )}

          {task.false_open?.only_parked_children_left && (
            <span
              className="inline-flex items-center text-sky-600 dark:text-sky-400"
              title={t("falseOpen.onlyParkedChildrenLeft", { count: task.false_open.open_children_count, days: task.false_open.stale_days })}
              data-testid="false-open-indicator"
              data-false-open-kind="only-parked-children-left"
            >
              <Snowflake className="h-3.5 w-3.5" aria-hidden="true" />
              <span className="sr-only">{t("card.falseOpen.onlyParkedChildrenLeft")}</span>
            </span>
          )}

          {task.reviewer_id && (
            <AssigneeAvatar
              id={task.reviewer_id}
              name={task.reviewer_name ?? undefined}
              type={task.reviewer_type ?? "unassigned"}
              size="sm"
            />
          )}

          {task.priority !== "none" && (
            <PriorityFlag priority={task.priority} size="sm" />
          )}

          {task.delegation_level && task.delegation_level !== "review" && (
            <DelegationLevelBadge value={task.delegation_level} />
          )}

          {checkoutExpiry && (
            <span
              className={cn(
                "inline-flex items-center gap-0.5 text-xs",
                checkoutLapsed ? "text-amber-600" : "text-muted-foreground",
              )}
              title={
                checkoutLapsed
                  ? t("card.checkout.lapsedTitle", { time: formatRelative(task.checkout_expires!) })
                  : holderIsSomeoneElse
                    ? t("card.checkout.liveHolderTitle", { holder: agentLabel(checkedOutByName!, holderTag), time: formatRelative(task.checkout_expires!) })
                    : t("card.checkout.liveTitle", { time: formatRelative(task.checkout_expires!) })
              }
              data-testid="checkout-indicator"
              data-checkout-state={checkoutLapsed ? "lapsed" : "live"}
            >
              <Hand className="h-3.5 w-3.5" aria-hidden="true" />
              <span className="sr-only">
                {checkoutLapsed ? t("card.checkout.lapsed") : t("card.checkout.live")}
              </span>
              {holderIsSomeoneElse && <span>{checkedOutByName}<AgentShortTag id={task.checked_out_by} /></span>}
              <span>{formatRelative(task.checkout_expires!)}</span>
            </span>
          )}

          {hasArtifacts && (
            <span className="inline-flex items-center gap-0.5 text-xs text-muted-foreground">
              <Paperclip className="h-3.5 w-3.5" aria-hidden="true" />
              {task.artifact_count}
            </span>
          )}

          {task.due_date && (
            <span
              className={cn(
                "ml-auto text-xs",
                dueDateOverdue
                  ? "font-medium text-red-500"
                  : "text-muted-foreground",
              )}
            >
              {formatRelative(task.due_date)}
            </span>
          )}
        </div>

        {/* Subtask row */}
        {hasSubtasks && (
          <div className="mt-1.5 flex items-center gap-1 text-xs text-muted-foreground">
            <GitBranch className="h-3.5 w-3.5" aria-hidden="true" />
            <span>
              {t("card.subtaskCount", { count: task.subtask_count })}
            </span>
          </div>
        )}
      </div>
    );
  },
  ),
);
TaskCard.displayName = "TaskCard";

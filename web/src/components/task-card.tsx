import { forwardRef, type HTMLAttributes } from "react";
import { AlignLeft, ExternalLink, GitBranch, Paperclip, Pencil, RefreshCw } from "lucide-react";
import { parseISO } from "date-fns";
import { cn } from "@/lib/cn";
import { Badge } from "@/components/ui/badge";
import { formatRelative } from "@/lib/utils";
import { AssigneeAvatar } from "@/components/assignee-avatar";
import { PriorityFlag } from "@/components/priority-flag";
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
}

export const TaskCard = forwardRef<HTMLDivElement, TaskCardProps>(
  ({ task, isDragging, statusCategory, onEditClick, className, ...props }, ref) => {
    const borderColor =
      priorityBorderColors[task.priority] ?? "border-l-transparent";

    const hasDescription = Boolean(task.description && task.description.trim().length > 0);
    const hasVcsLinks = (task.vcs_link_count ?? 0) > 0;
    const hasArtifacts = (task.artifact_count ?? 0) > 0;
    const hasSubtasks = (task.subtask_count ?? 0) > 0;
    const labels = task.labels ?? [];

    const dueDateOverdue =
      task.due_date ? isOverdue(task.due_date, statusCategory) : false;

    return (
      <div
        ref={ref}
        className={cn(
          "group cursor-pointer rounded-lg border border-border border-l-[3px] bg-card p-3 shadow-sm transition-shadow hover:shadow-md",
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
                    ? `Recurring task #${task.recurring_instance_number}`
                    : "Recurring task"
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
                aria-label="Has VCS links"
              />
            )}
            {onEditClick && (
              <button
                type="button"
                className="flex h-6 w-6 items-center justify-center rounded text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 hover:bg-muted hover:text-foreground focus-visible:opacity-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
                onClick={(e) => { e.stopPropagation(); onEditClick(e); }}
                aria-label="Edit task"
                title="Edit task"
              >
                <Pencil className="h-3.5 w-3.5" />
              </button>
            )}
          </div>
        </div>

        {/* Description indicator */}
        {hasDescription && (
          <div className="mt-1">
            <AlignLeft className="h-3.5 w-3.5 text-muted-foreground" aria-label="Has description" />
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

        {/* Bottom info row: assignee, priority, attachments, due date */}
        <div className="mt-1.5 flex items-center gap-2">
          <AssigneeAvatar
            name={task.assignee_name ?? undefined}
            type={task.assignee_type}
            size="sm"
          />

          {task.reviewer_id && (
            <AssigneeAvatar
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
              {task.subtask_count} subtask{task.subtask_count === 1 ? "" : "s"}
            </span>
          </div>
        )}
      </div>
    );
  },
);
TaskCard.displayName = "TaskCard";

import { forwardRef, type HTMLAttributes } from "react";
import { Bot, Clock, User } from "lucide-react";
import { cn } from "@/lib/cn";
import { Badge } from "@/components/ui/badge";
import { priorityConfig, formatRelative } from "@/lib/utils";
import {
  CustomFieldRenderer,
  shouldShowInCardPreview,
} from "@/components/custom-field-renderer";
import type { Task, CustomFieldDefinition } from "@/types";

const priorityBorderColors: Record<string, string> = {
  urgent: "border-l-red-600",
  high: "border-l-orange-500",
  medium: "border-l-yellow-500",
  low: "border-l-blue-500",
  none: "border-l-transparent",
};

interface TaskCardProps extends HTMLAttributes<HTMLDivElement> {
  task: Task;
  isDragging?: boolean;
  customFields?: CustomFieldDefinition[];
}

export const TaskCard = forwardRef<HTMLDivElement, TaskCardProps>(
  ({ task, isDragging, customFields, className, ...props }, ref) => {
    const pConfig = priorityConfig[task.priority];
    const borderColor = priorityBorderColors[task.priority] ?? "border-l-transparent";

    // Determine which custom fields to show (non-empty, preview-compatible)
    const visibleCustomFields = customFields
      ? customFields
          .filter((f) =>
            shouldShowInCardPreview(f, task.custom_fields?.[f.slug]),
          )
          .slice(0, 3) // Show max 3 to keep card compact
      : [];

    return (
      <div
        ref={ref}
        className={cn(
          "cursor-pointer rounded-lg border border-border border-l-[3px] bg-card p-3 shadow-sm transition-shadow hover:shadow-md",
          borderColor,
          isDragging && "shadow-lg opacity-90 ring-2 ring-primary/20",
          className,
        )}
        {...props}
      >
        <p className="text-sm font-medium leading-snug">{task.title}</p>

        <div className="mt-2 flex items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-1">
            {task.priority !== "none" && (
              <Badge variant="secondary" className={cn("text-[10px]", pConfig.color)}>
                {pConfig.label}
              </Badge>
            )}
            {task.labels.map((label) => (
              <Badge key={label} variant="outline" className="text-[10px]">
                {label}
              </Badge>
            ))}
          </div>

          <div className="flex shrink-0 items-center gap-1.5 text-muted-foreground">
            {task.due_date && (
              <span className="flex items-center gap-0.5 text-[10px]">
                <Clock className="h-3 w-3" />
                {formatRelative(task.due_date)}
              </span>
            )}
            <AssigneeIcon type={task.assignee_type} />
          </div>
        </div>

        {/* Custom field previews */}
        {visibleCustomFields.length > 0 && (
          <div className="mt-1.5 flex flex-wrap items-center gap-1">
            {visibleCustomFields.map((field) => (
              <span
                key={field.id}
                className="inline-flex items-center gap-0.5 text-[10px] text-muted-foreground"
                title={field.name}
              >
                <span className="font-medium">{field.name}:</span>
                <CustomFieldRenderer
                  field={field}
                  value={task.custom_fields?.[field.slug]}
                  onChange={() => {}}
                  readOnly
                  compact
                />
              </span>
            ))}
          </div>
        )}
      </div>
    );
  },
);
TaskCard.displayName = "TaskCard";

function AssigneeIcon({ type }: { type: string }) {
  if (type === "agent") {
    return <Bot className="h-3.5 w-3.5 text-violet-500" />;
  }
  if (type === "user") {
    return <User className="h-3.5 w-3.5 text-sky-500" />;
  }
  return <span className="text-xs text-muted-foreground">--</span>;
}

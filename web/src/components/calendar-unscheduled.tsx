import { CalendarOff, GripVertical } from "lucide-react";
import { cn } from "@/lib/cn";
import { Badge } from "@/components/ui/badge";
import type { Task } from "@/types";

interface CalendarUnscheduledProps {
  tasks: Task[];
  statusMap: Map<string, { name: string; color: string; category: string }>;
  onTaskClick: (task: Task) => void;
}

export function CalendarUnscheduled({
  tasks,
  statusMap,
  onTaskClick,
}: CalendarUnscheduledProps) {
  if (tasks.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-12 text-muted-foreground">
        <CalendarOff className="mb-2 h-8 w-8" />
        <p className="text-sm">No unscheduled tasks</p>
      </div>
    );
  }

  return (
    <div className="space-y-1.5">
      {tasks.map((task) => {
        const status = statusMap.get(task.status_id);
        return (
          <button
            key={task.id}
            onClick={() => onTaskClick(task)}
            className={cn(
              "flex w-full items-center gap-2 rounded-md border border-border px-2.5 py-2 text-left text-sm",
              "transition-colors hover:bg-muted/50",
            )}
          >
            <GripVertical className="h-3 w-3 shrink-0 text-muted-foreground" />
            {status && (
              <span
                className="inline-block h-2 w-2 shrink-0 rounded-full"
                style={{ backgroundColor: status.color }}
              />
            )}
            <span className="min-w-0 flex-1 truncate">{task.title}</span>
            {task.priority !== "none" && (
              <Badge variant="outline" className="shrink-0 text-[10px]">
                {task.priority}
              </Badge>
            )}
          </button>
        );
      })}
    </div>
  );
}

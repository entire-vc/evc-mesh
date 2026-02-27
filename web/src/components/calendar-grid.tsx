import { useMemo } from "react";
import {
  startOfMonth,
  endOfMonth,
  startOfWeek,
  endOfWeek,
  eachDayOfInterval,
  format,
  isSameMonth,
  isToday,
} from "date-fns";
import { Plus } from "lucide-react";
import { cn } from "@/lib/cn";
import type { Task } from "@/types";

const WEEKDAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

interface CalendarGridProps {
  currentMonth: Date;
  tasksByDate: Map<string, Task[]>;
  statusMap: Map<string, { name: string; color: string; category: string }>;
  onTaskClick: (task: Task) => void;
  onAddTask: (date: string) => void;
}

export function CalendarGrid({
  currentMonth,
  tasksByDate,
  statusMap,
  onTaskClick,
  onAddTask,
}: CalendarGridProps) {
  const days = useMemo(() => {
    const monthStart = startOfMonth(currentMonth);
    const monthEnd = endOfMonth(currentMonth);
    const calStart = startOfWeek(monthStart, { weekStartsOn: 1 });
    const calEnd = endOfWeek(monthEnd, { weekStartsOn: 1 });
    return eachDayOfInterval({ start: calStart, end: calEnd });
  }, [currentMonth]);

  return (
    <div className="flex flex-1 flex-col overflow-hidden rounded-lg border border-border">
      {/* Weekday header */}
      <div className="grid grid-cols-7 border-b border-border bg-muted/30">
        {WEEKDAYS.map((day) => (
          <div
            key={day}
            className="px-2 py-1.5 text-center text-xs font-medium text-muted-foreground"
          >
            {day}
          </div>
        ))}
      </div>

      {/* Day cells */}
      <div className="grid flex-1 grid-cols-7 auto-rows-fr">
        {days.map((day) => {
          const dateKey = format(day, "yyyy-MM-dd");
          const dayTasks = tasksByDate.get(dateKey) ?? [];
          const inMonth = isSameMonth(day, currentMonth);
          const today = isToday(day);

          return (
            <div
              key={dateKey}
              className={cn(
                "group relative flex flex-col border-b border-r border-border p-1",
                !inMonth && "bg-muted/20",
              )}
            >
              {/* Day number + add button */}
              <div className="mb-0.5 flex items-center justify-between px-0.5">
                <span
                  className={cn(
                    "inline-flex h-6 w-6 items-center justify-center rounded-full text-xs",
                    today
                      ? "bg-primary font-semibold text-primary-foreground"
                      : !inMonth
                        ? "text-muted-foreground/50"
                        : "text-foreground",
                  )}
                >
                  {format(day, "d")}
                </span>
                <button
                  onClick={() => onAddTask(dateKey)}
                  className="hidden h-5 w-5 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground group-hover:flex"
                  title="Add task"
                >
                  <Plus className="h-3 w-3" />
                </button>
              </div>

              {/* Task chips */}
              <div className="flex-1 space-y-0.5 overflow-y-auto">
                {dayTasks.slice(0, 4).map((task) => {
                  const status = statusMap.get(task.status_id);
                  const isDone = status?.category === "done" || status?.category === "cancelled";
                  return (
                    <button
                      key={task.id}
                      onClick={() => onTaskClick(task)}
                      className={cn(
                        "flex w-full items-center gap-1 rounded px-1.5 py-0.5 text-left text-[11px] leading-tight",
                        "transition-colors hover:bg-muted",
                        isDone && "line-through opacity-60",
                      )}
                      style={{
                        borderLeft: `2px solid ${status?.color ?? "#9ca3af"}`,
                      }}
                    >
                      <span className="min-w-0 flex-1 truncate">{task.title}</span>
                    </button>
                  );
                })}
                {dayTasks.length > 4 && (
                  <span className="block px-1.5 text-[10px] text-muted-foreground">
                    +{dayTasks.length - 4} more
                  </span>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

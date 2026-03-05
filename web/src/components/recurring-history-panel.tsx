import { useEffect } from "react";
import { useNavigate, useParams } from "react-router";
import {
  CheckCircle2,
  Circle,
  Clock,
  Loader2,
  Paperclip,
  RefreshCw,
  XCircle,
} from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { useRecurringStore } from "@/stores/recurring";
import { cn } from "@/lib/cn";
import { formatDate } from "@/lib/utils";
import type { RecurringSchedule, StatusCategory } from "@/types";

const statusCategoryIcons: Record<StatusCategory, typeof CheckCircle2> = {
  done: CheckCircle2,
  cancelled: XCircle,
  in_progress: Clock,
  review: Clock,
  todo: Circle,
  backlog: Circle,
  triage: Circle,
};

const statusCategoryColors: Record<StatusCategory, string> = {
  done: "text-emerald-500",
  cancelled: "text-muted-foreground",
  in_progress: "text-blue-500",
  review: "text-yellow-500",
  todo: "text-muted-foreground",
  backlog: "text-muted-foreground",
  triage: "text-orange-500",
};

const statusCategoryLabels: Record<StatusCategory, string> = {
  done: "Done",
  cancelled: "Cancelled",
  in_progress: "In Progress",
  review: "In Review",
  todo: "To Do",
  backlog: "Backlog",
  triage: "Triage",
};

interface RecurringHistoryPanelProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  schedule: RecurringSchedule;
  currentInstanceNumber?: number | null;
}

export function RecurringHistoryPanel({
  open,
  onOpenChange,
  schedule,
  currentInstanceNumber,
}: RecurringHistoryPanelProps) {
  const { history, isLoading, fetchHistory } = useRecurringStore();
  const navigate = useNavigate();
  const { wsSlug, projectSlug } = useParams();

  useEffect(() => {
    if (open) {
      void fetchHistory(schedule.id);
    }
  }, [open, schedule.id, fetchHistory]);

  const handleInstanceClick = (taskId: string) => {
    if (wsSlug && projectSlug) {
      onOpenChange(false);
      navigate(`/w/${wsSlug}/p/${projectSlug}/t/${taskId}`);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent onClose={() => onOpenChange(false)}>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <RefreshCw className="h-4 w-4 text-muted-foreground" />
            Recurring History
          </DialogTitle>
          <p className="text-sm text-muted-foreground">
            "{schedule.title_template}"
          </p>
        </DialogHeader>

        <div className="mt-2 max-h-[60vh] space-y-2 overflow-y-auto">
          {isLoading ? (
            <div className="space-y-2">
              {[1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-16 w-full" />
              ))}
            </div>
          ) : history.length === 0 ? (
            <div className="flex flex-col items-center gap-2 py-8 text-center text-sm text-muted-foreground">
              <Loader2 className="h-6 w-6 animate-spin opacity-50" />
              <p>No instances created yet.</p>
            </div>
          ) : (
            history.map((instance) => {
              const StatusIcon =
                statusCategoryIcons[instance.status_category] ?? Circle;
              const colorClass =
                statusCategoryColors[instance.status_category] ??
                "text-muted-foreground";
              const isCurrentInstance =
                instance.instance_number === currentInstanceNumber;

              return (
                <div
                  key={instance.task_id}
                  className={cn(
                    "cursor-pointer rounded-lg border border-border p-3 transition-colors hover:bg-accent/50",
                    isCurrentInstance &&
                      "border-primary/40 bg-primary/5",
                  )}
                  onClick={() => handleInstanceClick(instance.task_id)}
                  title="Click to open task"
                >
                  <div className="flex items-start justify-between gap-2">
                    <div className="flex items-center gap-2 min-w-0">
                      <StatusIcon
                        className={cn("h-4 w-4 shrink-0", colorClass)}
                      />
                      <span className="truncate text-sm font-medium">
                        {instance.title}
                      </span>
                    </div>
                    <div className="flex shrink-0 items-center gap-2">
                      {isCurrentInstance && (
                        <Badge variant="secondary" className="text-[10px]">
                          current
                        </Badge>
                      )}
                      <Badge
                        variant="outline"
                        className={cn("text-[10px]", colorClass)}
                      >
                        {statusCategoryLabels[instance.status_category] ??
                          instance.status_category}
                      </Badge>
                    </div>
                  </div>

                  <div className="mt-1.5 flex items-center gap-3 text-xs text-muted-foreground">
                    <span>#{instance.instance_number}</span>
                    {instance.completed_at ? (
                      <span>{formatDate(instance.completed_at)}</span>
                    ) : (
                      <span>{formatDate(instance.created_at)}</span>
                    )}
                    {instance.artifact_count > 0 && (
                      <span className="inline-flex items-center gap-0.5">
                        <Paperclip className="h-3 w-3" />
                        {instance.artifact_count}
                      </span>
                    )}
                  </div>

                  {instance.last_comment && (
                    <p className="mt-1 line-clamp-1 text-xs text-muted-foreground">
                      "{instance.last_comment}"
                    </p>
                  )}
                </div>
              );
            })
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

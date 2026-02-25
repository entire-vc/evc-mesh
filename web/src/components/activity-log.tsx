import { useCallback, useEffect, useState } from "react";
import {
  Activity,
  ArrowRight,
  Bot,
  Monitor,
  User,
} from "lucide-react";
import { api } from "@/lib/api";
import { formatRelative } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";

interface ActivityLogEntry {
  id: string;
  workspace_id: string;
  entity_type: string;
  entity_id: string;
  action: string;
  actor_id: string;
  actor_type: "user" | "agent" | "system";
  actor_name?: string;
  changes: Record<string, { old: unknown; new: unknown }>;
  created_at: string;
}

interface PaginatedActivityResponse {
  items: ActivityLogEntry[];
  total_count: number;
  page: number;
  page_size: number;
  has_more: boolean;
}

interface ActivityLogProps {
  taskId: string;
}

function ActorTypeIcon({ type }: { type: ActivityLogEntry["actor_type"] }) {
  if (type === "agent") {
    return <Bot className="h-4 w-4 text-violet-500" />;
  }
  if (type === "system") {
    return <Monitor className="h-4 w-4 text-muted-foreground" />;
  }
  return <User className="h-4 w-4 text-sky-500" />;
}

function formatValue(value: unknown): string {
  if (value === null || value === undefined) return "none";
  if (typeof value === "string") return value;
  if (typeof value === "number") return String(value);
  if (typeof value === "boolean") return value ? "true" : "false";
  return JSON.stringify(value);
}

function formatFieldName(field: string): string {
  return field
    .replace(/_/g, " ")
    .replace(/\bid\b/g, "ID")
    .replace(/^\w/, (c) => c.toUpperCase());
}

function formatActionDescription(entry: ActivityLogEntry): string {
  const changeKeys = Object.keys(entry.changes ?? {});

  if (changeKeys.length === 0) {
    // No detailed changes -- use action as-is
    return entry.action.replace(/_/g, " ");
  }

  // Build human-readable description from changes
  const descriptions = changeKeys.map((field) => {
    const change = entry.changes[field];
    if (!change) return `${formatFieldName(field)} changed`;
    const oldVal = formatValue(change.old);
    const newVal = formatValue(change.new);
    if (oldVal === "none") {
      return `${formatFieldName(field)} set to "${newVal}"`;
    }
    return `${formatFieldName(field)} changed from "${oldVal}" to "${newVal}"`;
  });

  return descriptions.join(", ");
}

export function ActivityLog({ taskId }: ActivityLogProps) {
  const [entries, setEntries] = useState<ActivityLogEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const [page, setPage] = useState(1);

  const fetchActivity = useCallback(
    async (pageNum: number, append: boolean) => {
      try {
        const data = await api<PaginatedActivityResponse>(
          `/api/v1/tasks/${taskId}/activity`,
          { params: { page: pageNum, page_size: 20 } },
        );
        const items = data.items ?? [];
        if (append) {
          setEntries((prev) => [...prev, ...items]);
        } else {
          setEntries(items);
        }
        setHasMore(data.has_more);
        setPage(data.page);
      } catch {
        // silently fail
      }
    },
    [taskId],
  );

  useEffect(() => {
    setLoading(true);
    fetchActivity(1, false).finally(() => setLoading(false));
  }, [fetchActivity]);

  const handleLoadMore = async () => {
    setLoadingMore(true);
    try {
      await fetchActivity(page + 1, true);
    } finally {
      setLoadingMore(false);
    }
  };

  if (loading) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-full" />
      </div>
    );
  }

  if (entries.length === 0) {
    return (
      <div className="flex flex-col items-center py-8 text-muted-foreground">
        <Activity className="mb-2 h-8 w-8" />
        <p className="text-sm">No activity recorded yet.</p>
      </div>
    );
  }

  return (
    <div className="space-y-0">
      {entries.map((entry, index) => {
        const isLast = index === entries.length - 1;
        const changeKeys = Object.keys(entry.changes ?? {});

        return (
          <div key={entry.id} className="relative flex gap-3 pb-4">
            {/* Timeline line */}
            {!isLast && (
              <div className="absolute left-[11px] top-7 h-[calc(100%-16px)] w-px bg-border" />
            )}

            {/* Icon */}
            <div className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full border border-border bg-background">
              <ActorTypeIcon type={entry.actor_type} />
            </div>

            {/* Content */}
            <div className="min-w-0 flex-1">
              <p className="text-sm">
                <span className="font-medium">
                  {entry.actor_name || entry.actor_type}
                </span>
                {" "}
                {formatActionDescription(entry)}
              </p>

              {/* Detailed changes */}
              {changeKeys.length > 0 && (
                <div className="mt-1.5 space-y-1">
                  {changeKeys.map((field) => {
                    const change = (entry.changes ?? {})[field];
                    if (!change) return null;
                    return (
                      <div
                        key={field}
                        className="flex items-center gap-1.5 text-xs text-muted-foreground"
                      >
                        <span className="font-medium">
                          {formatFieldName(field)}:
                        </span>
                        <code className="rounded bg-muted px-1 py-0.5">
                          {formatValue(change.old)}
                        </code>
                        <ArrowRight className="h-3 w-3" />
                        <code className="rounded bg-muted px-1 py-0.5">
                          {formatValue(change.new)}
                        </code>
                      </div>
                    );
                  })}
                </div>
              )}

              <span className="mt-1 block text-xs text-muted-foreground">
                {formatRelative(entry.created_at)}
              </span>
            </div>
          </div>
        );
      })}

      {hasMore && (
        <div className="flex justify-center pt-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void handleLoadMore()}
            disabled={loadingMore}
          >
            {loadingMore ? "Loading..." : "Load more"}
          </Button>
        </div>
      )}
    </div>
  );
}

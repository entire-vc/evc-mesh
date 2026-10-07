import { useRulesStore } from "@/stores/rules";
import { agentLabel } from "@/lib/agent-label";
import { AgentShortTag } from "@/components/agent-short-tag";
import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Activity,
  Bot,
  Monitor,
  User,
} from "lucide-react";
import { api } from "@/lib/api";
import { formatRelative } from "@/lib/utils";
import { useAgentStore } from "@/stores/agent";
import { useMemberStore } from "@/stores/member";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";

import { decodeActivityChanges, describeActivity } from "@/lib/activity";
import { ActivityDetails } from "@/components/activity-details";
import type { ActivityLog as ActivityLogEntry } from "@/types";

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

export function ActivityLog({ taskId }: ActivityLogProps) {
  const { agents } = useAgentStore();
  const directoryAgents = useRulesStore((s) => s.teamDirectory?.agents);
  const { workspaceMembers } = useMemberStore();
  const [entries, setEntries] = useState<ActivityLogEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const [page, setPage] = useState(1);

  // Build UUID → name map from agents and members
  const nameMap = useMemo(() => {
    const map = new Map<string, string>();
    for (const a of directoryAgents ?? []) {
      map.set(a.id, agentLabel(a.name, a.short_tag));
    }
    for (const a of agents) {
      map.set(a.id, agentLabel(a.name, a.short_tag));
    }
    for (const m of workspaceMembers) {
      map.set(m.user_id, m.user?.name || m.user?.email || m.user_id);
    }
    return map;
  }, [agents, directoryAgents, workspaceMembers]);

  const fetchActivity = useCallback(
    async (pageNum: number, append: boolean) => {
      try {
        const data = await api<PaginatedActivityResponse>(
          `/api/v1/tasks/${taskId}/activity`,
          { params: { page: pageNum, page_size: 20 } },
        );
        const items = data.items ?? [];
        const sortNewest = (arr: typeof items) =>
          [...arr].sort(
            (a, b) =>
              new Date(b.created_at).getTime() -
              new Date(a.created_at).getTime(),
          );
        if (append) {
          setEntries((prev) => sortNewest([...prev, ...items]));
        } else {
          setEntries(sortNewest(items));
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
        const details = decodeActivityChanges(entry.changes);

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
                  {entry.actor_name || entry.actor_type}<AgentShortTag id={entry.actor_id} type={entry.actor_type} />
                </span>
                {" "}
                {describeActivity(entry.action, details, nameMap)}
              </p>

              <ActivityDetails details={details} names={nameMap} />

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

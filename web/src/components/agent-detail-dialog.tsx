import { cn } from "@/lib/cn";
import { agentStatusConfig, agentTypeConfig } from "@/lib/agent-utils";
import { formatDate, formatRelative } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { Agent } from "@/types";

interface AgentDetailDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  agent: Agent | null;
}

export function AgentDetailDialog({
  open,
  onOpenChange,
  agent,
}: AgentDetailDialogProps) {
  if (!agent) return null;

  const typeConfig = agentTypeConfig[agent.agent_type];
  const statusConfig = agentStatusConfig[agent.status];

  const metadata = agent.metadata || {};
  const tasksCompleted =
    typeof metadata.tasks_completed === "number"
      ? metadata.tasks_completed
      : null;
  const totalErrors =
    typeof metadata.total_errors === "number" ? metadata.total_errors : null;
  const currentTask =
    metadata.current_task && typeof metadata.current_task === "object"
      ? (metadata.current_task as { id?: string; title?: string })
      : null;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        onClose={() => onOpenChange(false)}
        className="max-h-[85vh] overflow-y-auto"
      >
        <DialogHeader>
          <DialogTitle className="flex items-center gap-3">
            <span>{agent.name}</span>
            <Badge className={cn("text-xs", typeConfig.color)}>
              {typeConfig.label}
            </Badge>
          </DialogTitle>
        </DialogHeader>

        <div className="mt-4 space-y-5">
          {/* Status */}
          <DetailRow label="Status">
            <div className="flex items-center gap-2">
              <span
                className={cn("h-2.5 w-2.5 rounded-full", statusConfig.dotColor)}
              />
              <span className="text-sm">{statusConfig.label}</span>
            </div>
          </DetailRow>

          {/* Last Heartbeat */}
          <DetailRow label="Last Heartbeat">
            <span className="text-sm">
              {agent.last_heartbeat
                ? formatRelative(agent.last_heartbeat)
                : "Never"}
            </span>
          </DetailRow>

          {/* Registered */}
          <DetailRow label="Registered">
            <span className="text-sm">{formatDate(agent.created_at)}</span>
          </DetailRow>

          {/* API Key (masked) */}
          <DetailRow label="API Key">
            <code className="rounded bg-muted px-2 py-0.5 font-mono text-xs">
              {agent.api_key_hash
                ? `${agent.api_key_hash.substring(0, 12)}...`
                : "N/A"}
            </code>
          </DetailRow>

          {/* Current Task */}
          {currentTask && (
            <DetailRow label="Current Task">
              <span className="text-sm">
                {currentTask.title || currentTask.id || "In progress"}
              </span>
            </DetailRow>
          )}

          {/* Capabilities */}
          {(() => {
            const caps = Array.isArray(agent.capabilities) ? agent.capabilities : Object.keys(agent.capabilities ?? {});
            return caps.length > 0 ? (
            <div>
              <p className="mb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">
                Capabilities
              </p>
              <div className="flex flex-wrap gap-1.5">
                {caps.map((cap) => (
                  <Badge key={cap} variant="outline" className="text-xs">
                    {cap}
                  </Badge>
                ))}
              </div>
            </div>
            ) : null;
          })()}

          {/* Statistics */}
          {(tasksCompleted !== null || totalErrors !== null) && (
            <div>
              <p className="mb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">
                Statistics
              </p>
              <div className="grid grid-cols-2 gap-3">
                {tasksCompleted !== null && (
                  <div className="rounded-lg border border-border p-3">
                    <p className="text-xs text-muted-foreground">
                      Tasks Completed
                    </p>
                    <p className="text-lg font-semibold">{tasksCompleted}</p>
                  </div>
                )}
                {totalErrors !== null && (
                  <div className="rounded-lg border border-border p-3">
                    <p className="text-xs text-muted-foreground">
                      Total Errors
                    </p>
                    <p className="text-lg font-semibold">{totalErrors}</p>
                  </div>
                )}
              </div>
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function DetailRow({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </span>
      {children}
    </div>
  );
}

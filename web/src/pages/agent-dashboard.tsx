import { useCallback, useEffect, useState } from "react";
import { Bot, Plus } from "lucide-react";
import { cn } from "@/lib/cn";
import { agentStatusConfig, agentTypeConfig, getEffectiveStatus, isAgentStale } from "@/lib/agent-utils";
import { formatRelative } from "@/lib/utils";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAgentStore } from "@/stores/agent";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { RegisterAgentDialog } from "@/components/register-agent-dialog";
import { AgentDetailDialog } from "@/components/agent-detail-dialog";
import type { Agent } from "@/types";

export function AgentDashboardPage() {
  const { currentWorkspace } = useWorkspaceStore();
  const { agents, isLoading, fetchAgents } = useAgentStore();

  const [registerOpen, setRegisterOpen] = useState(false);
  const [selectedAgentId, setSelectedAgentId] = useState<string | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);

  // Always get the latest agent data from the store
  const selectedAgent = selectedAgentId
    ? agents.find((a) => a.id === selectedAgentId) ?? null
    : null;

  useEffect(() => {
    if (!currentWorkspace) return;
    fetchAgents(currentWorkspace.id);
    const interval = setInterval(() => fetchAgents(currentWorkspace.id), 30_000);
    return () => clearInterval(interval);
  }, [currentWorkspace, fetchAgents]);

  const handleAgentClick = useCallback((agent: Agent) => {
    setSelectedAgentId(agent.id);
    setDetailOpen(true);
  }, []);

  const handleRegisterClose = useCallback(
    (open: boolean) => {
      setRegisterOpen(open);
      // Refresh agent list after dialog closes (in case a new agent was registered)
      if (!open && currentWorkspace) {
        fetchAgents(currentWorkspace.id);
      }
    },
    [currentWorkspace, fetchAgents],
  );

  return (
    <div className="space-y-6">
      {/* Toolbar */}
      <div className="flex justify-end">
        <Button onClick={() => setRegisterOpen(true)}>
          <Plus className="h-4 w-4" />
          Register Agent
        </Button>
      </div>

      {/* Content */}
      {isLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Card key={i}>
              <CardHeader>
                <Skeleton className="h-5 w-32" />
                <Skeleton className="h-4 w-20" />
              </CardHeader>
              <CardContent>
                <Skeleton className="h-4 w-48" />
                <Skeleton className="mt-2 h-4 w-36" />
              </CardContent>
            </Card>
          ))}
        </div>
      ) : agents.length === 0 ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center py-12">
            <Bot className="mb-4 h-12 w-12 text-muted-foreground" />
            <h3 className="mb-2 text-lg font-semibold">
              No agents registered
            </h3>
            <p className="mb-4 text-center text-sm text-muted-foreground">
              Register your first agent to get started. Agents can be assigned
              tasks and work autonomously in your workspace.
            </p>
            <Button onClick={() => setRegisterOpen(true)}>
              <Plus className="h-4 w-4" />
              Register Agent
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {agents.map((agent) => (
            <AgentCard
              key={agent.id}
              agent={agent}
              onClick={() => handleAgentClick(agent)}
            />
          ))}
        </div>
      )}

      {/* Dialogs */}
      {currentWorkspace && (
        <RegisterAgentDialog
          open={registerOpen}
          onOpenChange={handleRegisterClose}
          workspaceId={currentWorkspace.id}
        />
      )}

      <AgentDetailDialog
        open={detailOpen}
        onOpenChange={setDetailOpen}
        agent={selectedAgent}
      />
    </div>
  );
}

function AgentCard({
  agent,
  onClick,
}: {
  agent: Agent;
  onClick: () => void;
}) {
  const typeConfig = agentTypeConfig[agent.agent_type];
  const effectiveStatus = getEffectiveStatus(agent);
  const statusConfig = agentStatusConfig[effectiveStatus];
  const stale = isAgentStale(agent);

  return (
    <Card
      className={cn(
        "cursor-pointer transition-shadow hover:shadow-md",
        stale && agent.status === "online" && "border-yellow-300 dark:border-yellow-700",
      )}
      onClick={onClick}
    >
      <CardHeader className="pb-3">
        <div className="flex items-start justify-between">
          <CardTitle className="text-base">{agent.name}</CardTitle>
          <Badge className={cn("text-xs", typeConfig.color)}>
            {typeConfig.label}
          </Badge>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {/* Status */}
        <div className="flex items-center gap-2">
          <span
            className={cn(
              "h-2 w-2 rounded-full",
              statusConfig.dotColor,
            )}
          />
          <span className="text-sm text-muted-foreground">
            {statusConfig.label}
            {stale && agent.status === "online" && " (stale)"}
          </span>
        </div>

        {/* Heartbeat message */}
        {agent.heartbeat_message && (
          <div className="text-xs italic text-muted-foreground truncate" title={agent.heartbeat_message}>
            {agent.heartbeat_message}
          </div>
        )}

        {/* Last Heartbeat */}
        <div className={cn("text-xs text-muted-foreground", stale && "text-yellow-600")}>
          {agent.last_heartbeat
            ? `Last seen ${formatRelative(agent.last_heartbeat)}`
            : "No heartbeat yet"}
        </div>

        {/* Capabilities */}
        {(() => {
          const caps = Array.isArray(agent.capabilities) ? agent.capabilities : Object.keys(agent.capabilities ?? {});
          return caps.length > 0 ? (
          <div className="flex flex-wrap gap-1">
            {caps.slice(0, 3).map((cap) => (
              <Badge key={cap} variant="outline" className="text-[10px]">
                {cap}
              </Badge>
            ))}
            {caps.length > 3 && (
              <Badge variant="outline" className="text-[10px]">
                +{caps.length - 3}
              </Badge>
            )}
          </div>
          ) : null;
        })()}

        {/* Role */}
        {agent.role && (
          <div className="text-xs text-muted-foreground">
            {agent.role}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

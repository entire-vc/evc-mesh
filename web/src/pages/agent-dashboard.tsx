import { useCallback, useEffect, useState } from "react";
import { Bot, Plus } from "lucide-react";
import { cn } from "@/lib/cn";
import { agentStatusConfig, agentTypeConfig } from "@/lib/agent-utils";
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
  const [selectedAgent, setSelectedAgent] = useState<Agent | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);

  useEffect(() => {
    if (currentWorkspace) {
      fetchAgents(currentWorkspace.id);
    }
  }, [currentWorkspace, fetchAgents]);

  const handleAgentClick = useCallback((agent: Agent) => {
    setSelectedAgent(agent);
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
  const statusConfig = agentStatusConfig[agent.status];

  return (
    <Card
      className="cursor-pointer transition-shadow hover:shadow-md"
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
          </span>
        </div>

        {/* Last Heartbeat */}
        <div className="text-xs text-muted-foreground">
          {agent.last_heartbeat
            ? `Last seen ${formatRelative(agent.last_heartbeat)}`
            : "No heartbeat yet"}
        </div>

        {/* Capabilities */}
        {agent.capabilities && agent.capabilities.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {agent.capabilities.slice(0, 3).map((cap) => (
              <Badge key={cap} variant="outline" className="text-[10px]">
                {cap}
              </Badge>
            ))}
            {agent.capabilities.length > 3 && (
              <Badge variant="outline" className="text-[10px]">
                +{agent.capabilities.length - 3}
              </Badge>
            )}
          </div>
        )}

        {/* API Key prefix */}
        <div className="text-xs text-muted-foreground">
          <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px]">
            {agent.api_key_hash
              ? `${agent.api_key_hash.substring(0, 16)}...`
              : "N/A"}
          </code>
        </div>
      </CardContent>
    </Card>
  );
}

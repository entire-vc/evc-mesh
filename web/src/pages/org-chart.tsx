import { useEffect, useMemo, useState } from "react";
import { useLocation } from "react-router";
import { Bot, Plus, User, UserPlus } from "lucide-react";
import { cn } from "@/lib/cn";
import { agentStatusConfig, isAgentStale } from "@/lib/agent-utils";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAgentStore } from "@/stores/agent";
import { useRulesStore } from "@/stores/rules";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { AgentDetailDialog } from "@/components/agent-detail-dialog";
import { RegisterAgentDialog } from "@/components/register-agent-dialog";
import { InviteMemberDialog } from "@/components/invite-member-dialog";
import type { Agent, OrgChartAgentNode, TeamDirectoryHuman } from "@/types";

// ---------------------------------------------------------------------------
// Unified tree node — can be an agent or a human
// ---------------------------------------------------------------------------

interface UnifiedNode {
  id: string;
  type: "agent" | "human";
  name: string;
  children: UnifiedNode[];
  agentData?: OrgChartAgentNode;
  humanData?: TeamDirectoryHuman;
}

/**
 * Build a single tree from agents and humans.
 * - Agents with parent_agent_id nest under their parent agent.
 * - Agents with supervisor_user_id nest under that human.
 * - Humans that supervise at least one agent appear as tree nodes.
 * - Remaining humans (no supervised agents) appear as standalone root nodes.
 * - Root agents have neither parent_agent_id nor supervisor_user_id.
 */
function buildUnifiedTree(
  agentTree: OrgChartAgentNode[],
  humans: TeamDirectoryHuman[],
): UnifiedNode[] {
  // Flatten the backend agent tree to a flat list (backend already built a tree,
  // but we rebuild from scratch to interleave humans).
  function flattenAgents(nodes: OrgChartAgentNode[]): OrgChartAgentNode[] {
    const result: OrgChartAgentNode[] = [];
    for (const n of nodes) {
      result.push(n);
      if (n.children?.length > 0) {
        result.push(...flattenAgents(n.children));
      }
    }
    return result;
  }

  const allAgents = flattenAgents(agentTree);

  // Maps for quick lookup
  const agentNodeMap = new Map<string, UnifiedNode>();
  const humanNodeMap = new Map<string, UnifiedNode>();

  // Create unified nodes for all agents
  for (const a of allAgents) {
    agentNodeMap.set(a.id, {
      id: a.id,
      type: "agent",
      name: a.name,
      children: [],
      agentData: { ...a, children: [] },
    });
  }

  // Create unified nodes for all humans
  for (const h of humans) {
    humanNodeMap.set(h.id, {
      id: h.id,
      type: "human",
      name: h.name,
      children: [],
      humanData: h,
    });
  }

  // Build parent-child relationships
  const childIds = new Set<string>();

  for (const a of allAgents) {
    const node = agentNodeMap.get(a.id)!;

    if (a.supervisor_user_id) {
      // Agent supervised by a human
      const parentNode = humanNodeMap.get(a.supervisor_user_id);
      if (parentNode) {
        parentNode.children.push(node);
        childIds.add(a.id);
      }
    } else if (a.parent_agent_id) {
      // Agent parented by another agent
      const parentNode = agentNodeMap.get(a.parent_agent_id);
      if (parentNode) {
        parentNode.children.push(node);
        childIds.add(a.id);
      }
    }
  }

  // Collect root nodes: agents and humans not nested under anyone
  const roots: UnifiedNode[] = [];

  // Humans who have children (supervise agents) go first
  for (const [, node] of humanNodeMap) {
    if (node.children.length > 0) {
      roots.push(node);
    }
  }

  // Root agents (not nested under any parent)
  for (const [id, node] of agentNodeMap) {
    if (!childIds.has(id)) {
      roots.push(node);
    }
  }

  // Humans with no supervised agents go at the end
  for (const [, node] of humanNodeMap) {
    if (node.children.length === 0) {
      roots.push(node);
    }
  }

  return roots;
}

// ---------------------------------------------------------------------------
// Card components
// ---------------------------------------------------------------------------

const statusBorderColors: Record<string, string> = {
  online: "border-l-green-500",
  busy: "border-l-yellow-500",
  offline: "border-l-gray-400",
  error: "border-l-red-500",
};

function AgentCard({
  agent,
  onClick,
}: {
  agent: OrgChartAgentNode;
  onClick?: () => void;
}) {
  const stale = agent.is_stale ?? isAgentStale(agent);
  const statusCfg = agentStatusConfig[agent.status as keyof typeof agentStatusConfig] ?? {
    label: agent.status,
    dotColor: "bg-gray-400",
  };
  const borderColor = statusBorderColors[agent.status] ?? "border-l-gray-400";

  return (
    <Card
      className={cn(
        "w-52 shrink-0 border-l-4 select-none hover:shadow-md transition-shadow",
        borderColor,
        stale && agent.status === "online" && "border-l-yellow-500",
        onClick ? "cursor-pointer" : "cursor-default",
      )}
      onClick={onClick}
    >
      <CardContent className="p-3 space-y-1.5">
        <div className="flex items-center gap-1.5 min-w-0">
          <Bot className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="font-medium text-sm truncate" title={agent.name}>
            {agent.name}
          </span>
        </div>

        {agent.role && (
          <p className="text-xs text-muted-foreground truncate">{agent.role}</p>
        )}

        <div className="flex items-center gap-1.5">
          <span className={cn("h-2 w-2 rounded-full shrink-0", statusCfg.dotColor)} />
          <span className="text-xs text-muted-foreground">
            {statusCfg.label}
            {stale && agent.status === "online" && " (stale)"}
          </span>
          {agent.max_concurrent_tasks > 0 && (
            <span className="ml-auto text-xs text-muted-foreground tabular-nums">
              {agent.current_tasks}/{agent.max_concurrent_tasks}
            </span>
          )}
        </div>

        {agent.heartbeat_message && (
          <p className="text-xs italic text-muted-foreground truncate" title={agent.heartbeat_message}>
            {agent.heartbeat_message}
          </p>
        )}

        {agent.projects && agent.projects.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {agent.projects.map((p) => (
              <Badge key={p} variant="secondary" className="text-xs px-1 py-0 h-4">
                {p}
              </Badge>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function HumanCard({ human }: { human: TeamDirectoryHuman }) {
  return (
    <Card className="w-52 shrink-0 border-l-4 border-l-blue-400 hover:shadow-md transition-shadow cursor-default">
      <CardContent className="p-3 space-y-1.5">
        <div className="flex items-center gap-1.5 min-w-0">
          <User className="h-3.5 w-3.5 shrink-0 text-blue-400" />
          <span className="font-medium text-sm truncate" title={human.name}>
            {human.name}
          </span>
        </div>

        <p className="text-xs text-muted-foreground truncate">{human.email}</p>

        {human.role && (
          <Badge variant="outline" className="text-xs px-1 py-0 h-4">
            {human.role}
          </Badge>
        )}

        {human.projects && human.projects.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {human.projects.map((p) => (
              <Badge key={p} variant="secondary" className="text-xs px-1 py-0 h-4">
                {p}
              </Badge>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Unified tree node renderer
// ---------------------------------------------------------------------------

function UnifiedTreeNodeView({
  node,
  onAgentClick,
}: {
  node: UnifiedNode;
  onAgentClick: (agentId: string) => void;
}) {
  const hasChildren = node.children.length > 0;

  return (
    <div className="flex flex-col items-center">
      {/* Card */}
      {node.type === "agent" && node.agentData ? (
        <AgentCard agent={node.agentData} onClick={() => onAgentClick(node.id)} />
      ) : node.humanData ? (
        <HumanCard human={node.humanData} />
      ) : null}

      {/* Connector lines to children */}
      {hasChildren && (
        <>
          <div className="w-px h-6 bg-border" />
          <div className="relative flex justify-center">
            {node.children.length > 1 && (
              <div
                className="absolute top-0 h-px bg-border"
                style={{
                  left: `calc(${(100 / node.children.length) * 0.5}%)`,
                  right: `calc(${(100 / node.children.length) * 0.5}%)`,
                }}
              />
            )}
            <div className="flex gap-8">
              {node.children.map((child) => (
                <div key={child.id} className="flex flex-col items-center">
                  <div className="w-px h-6 bg-border" />
                  <UnifiedTreeNodeView node={child} onAgentClick={onAgentClick} />
                </div>
              ))}
            </div>
          </div>
        </>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tree view — unified hierarchy
// ---------------------------------------------------------------------------

function TreeView({
  agentTree,
  humans,
  onAgentClick,
}: {
  agentTree: OrgChartAgentNode[];
  humans: TeamDirectoryHuman[];
  onAgentClick: (agentId: string) => void;
}) {
  const unifiedRoots = useMemo(
    () => buildUnifiedTree(agentTree, humans),
    [agentTree, humans],
  );

  if (unifiedRoots.length === 0) {
    return <p className="text-sm text-muted-foreground">No team members yet.</p>;
  }

  return (
    <div className="overflow-x-auto pb-4">
      <div className="inline-flex gap-10 items-start">
        {unifiedRoots.map((root) => (
          <UnifiedTreeNodeView key={root.id} node={root} onAgentClick={onAgentClick} />
        ))}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Grid view (grouped by project) — unified, no agent/human separation
// ---------------------------------------------------------------------------

function GridView({
  agentTree,
  humans,
  onAgentClick,
}: {
  agentTree: OrgChartAgentNode[];
  humans: TeamDirectoryHuman[];
  onAgentClick: (agentId: string) => void;
}) {
  function flattenTree(nodes: OrgChartAgentNode[]): OrgChartAgentNode[] {
    const result: OrgChartAgentNode[] = [];
    for (const n of nodes) {
      result.push(n);
      if (n.children.length > 0) {
        result.push(...flattenTree(n.children));
      }
    }
    return result;
  }

  const allAgents = flattenTree(agentTree);

  const projectSet = new Set<string>();
  for (const a of allAgents) {
    for (const p of a.projects ?? []) projectSet.add(p);
  }
  for (const h of humans) {
    for (const p of h.projects ?? []) projectSet.add(p);
  }
  const projects = Array.from(projectSet).sort();

  const unassignedAgents = allAgents.filter(
    (a) => !a.projects || a.projects.length === 0,
  );
  const unassignedHumans = humans.filter(
    (h) => !h.projects || h.projects.length === 0,
  );

  const hasUnassigned = unassignedAgents.length > 0 || unassignedHumans.length > 0;

  return (
    <div className="space-y-8">
      {projects.map((project) => {
        const projectAgents = allAgents.filter((a) => a.projects?.includes(project));
        const projectHumans = humans.filter((h) => h.projects?.includes(project));
        return (
          <div key={project}>
            <h3 className="text-sm font-semibold mb-3 flex items-center gap-2">
              <span className="inline-block h-2 w-2 rounded-full bg-primary" />
              {project}
              <span className="text-xs font-normal text-muted-foreground">
                ({projectAgents.length + projectHumans.length})
              </span>
            </h3>
            <div className="flex flex-wrap gap-4">
              {projectHumans.map((h) => (
                <HumanCard key={h.id} human={h} />
              ))}
              {projectAgents.map((a) => (
                <AgentCard key={a.id} agent={a} onClick={() => onAgentClick(a.id)} />
              ))}
            </div>
          </div>
        );
      })}

      {hasUnassigned && (
        <div>
          <h3 className="text-sm font-semibold mb-3 flex items-center gap-2 text-muted-foreground">
            <span className="inline-block h-2 w-2 rounded-full bg-muted-foreground" />
            No project
            <span className="text-xs font-normal">
              ({unassignedAgents.length + unassignedHumans.length})
            </span>
          </h3>
          <div className="flex flex-wrap gap-4">
            {unassignedHumans.map((h) => (
              <HumanCard key={h.id} human={h} />
            ))}
            {unassignedAgents.map((a) => (
              <AgentCard key={a.id} agent={a} onClick={() => onAgentClick(a.id)} />
            ))}
          </div>
        </div>
      )}

      {projects.length === 0 && !hasUnassigned && (
        <p className="text-sm text-muted-foreground">No team members yet.</p>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Skeleton loader
// ---------------------------------------------------------------------------

function OrgChartSkeleton() {
  return (
    <div className="space-y-4">
      <div className="flex gap-8 justify-center">
        <Skeleton className="h-28 w-52 rounded-lg" />
        <Skeleton className="h-28 w-52 rounded-lg" />
        <Skeleton className="h-28 w-52 rounded-lg" />
      </div>
      <div className="flex gap-8 justify-center">
        <Skeleton className="h-28 w-52 rounded-lg" />
        <Skeleton className="h-28 w-52 rounded-lg" />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

type ViewMode = "tree" | "grid";

export function OrgChartPage() {
  const { currentWorkspace } = useWorkspaceStore();
  const { orgChart, isOrgChartLoading, fetchOrgChart } = useRulesStore();
  const { agents, fetchAgents } = useAgentStore();

  // View mode from URL: /org-chart = tree, /org-chart/grid = grid
  const location = useLocation();
  const viewMode: ViewMode = location.pathname.endsWith("/grid") ? "grid" : "tree";

  // Dialog state
  const [selectedAgentId, setSelectedAgentId] = useState<string | null>(null);
  const [registerOpen, setRegisterOpen] = useState(false);
  const [inviteOpen, setInviteOpen] = useState(false);

  useEffect(() => {
    if (currentWorkspace) {
      void fetchOrgChart(currentWorkspace.id);
      void fetchAgents(currentWorkspace.id);
    }
  }, [currentWorkspace, fetchOrgChart, fetchAgents]);

  const agentTree = orgChart?.agent_tree ?? [];
  const humans = orgChart?.humans ?? [];
  const workspaceName = orgChart?.workspace ?? currentWorkspace?.name ?? "";

  const selectedAgent: Agent | null = selectedAgentId
    ? agents.find((a) => a.id === selectedAgentId) ?? null
    : null;

  const handleAgentClick = (agentId: string) => {
    setSelectedAgentId(agentId);
  };

  return (
    <div className="flex h-full flex-col">
      {/* Action bar */}
      <div className="flex items-center justify-between pb-4">
        <div>
          {workspaceName && (
            <p className="text-xs text-muted-foreground">{workspaceName}</p>
          )}
        </div>

        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5"
            onClick={() => setRegisterOpen(true)}
          >
            <Plus className="h-3.5 w-3.5" />
            Register Agent
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5"
            onClick={() => setInviteOpen(true)}
          >
            <UserPlus className="h-3.5 w-3.5" />
            Invite Member
          </Button>

        </div>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto">
        {isOrgChartLoading ? (
          <OrgChartSkeleton />
        ) : viewMode === "tree" ? (
          <TreeView agentTree={agentTree} humans={humans} onAgentClick={handleAgentClick} />
        ) : (
          <GridView agentTree={agentTree} humans={humans} onAgentClick={handleAgentClick} />
        )}
      </div>

      {/* Agent detail dialog */}
      <AgentDetailDialog
        open={selectedAgentId !== null}
        onOpenChange={(open) => {
          if (!open) setSelectedAgentId(null);
        }}
        agent={selectedAgent}
      />

      {/* Register agent dialog */}
      {currentWorkspace && (
        <>
          <RegisterAgentDialog
            open={registerOpen}
            onOpenChange={setRegisterOpen}
            workspaceId={currentWorkspace.id}
          />
          <InviteMemberDialog
            open={inviteOpen}
            onClose={() => setInviteOpen(false)}
            workspaceId={currentWorkspace.id}
          />
        </>
      )}
    </div>
  );
}

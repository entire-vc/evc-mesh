import { useEffect, useState } from "react";
import { Bot, LayoutGrid, Network, User } from "lucide-react";
import { cn } from "@/lib/cn";
import { agentStatusConfig, isAgentStale } from "@/lib/agent-utils";
import { useWorkspaceStore } from "@/stores/workspace";
import { useRulesStore } from "@/stores/rules";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type { OrgChartAgentNode, TeamDirectoryHuman } from "@/types";

// ---------------------------------------------------------------------------
// Agent card
// ---------------------------------------------------------------------------

const statusBorderColors: Record<string, string> = {
  online: "border-l-green-500",
  busy: "border-l-yellow-500",
  offline: "border-l-gray-400",
  error: "border-l-red-500",
};

function AgentCard({ agent }: { agent: OrgChartAgentNode }) {
  const stale = agent.is_stale ?? isAgentStale(agent);
  const statusCfg = agentStatusConfig[agent.status as keyof typeof agentStatusConfig] ?? {
    label: agent.status,
    dotColor: "bg-gray-400",
  };
  const borderColor = statusBorderColors[agent.status] ?? "border-l-gray-400";

  return (
    <Card
      className={cn(
        "w-52 shrink-0 border-l-4 cursor-default select-none hover:shadow-md transition-shadow",
        borderColor,
        stale && agent.status === "online" && "border-l-yellow-500",
      )}
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

// ---------------------------------------------------------------------------
// Human card
// ---------------------------------------------------------------------------

function HumanCard({ human }: { human: TeamDirectoryHuman }) {
  return (
    <Card className="w-52 shrink-0 border-l-4 border-l-blue-400 hover:shadow-md transition-shadow">
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
// Tree node — top-down org chart with connector lines
// ---------------------------------------------------------------------------

function OrgTreeNode({ node }: { node: OrgChartAgentNode }) {
  const hasChildren = node.children && node.children.length > 0;

  return (
    <div className="flex flex-col items-center">
      {/* Card */}
      <AgentCard agent={node} />

      {/* Connector lines to children */}
      {hasChildren && (
        <>
          {/* Vertical line down from card */}
          <div className="w-px h-6 bg-border" />

          {/* Horizontal rail + vertical drops */}
          <div className="relative flex justify-center">
            {/* Horizontal connector spanning all children */}
            {node.children.length > 1 && (
              <div
                className="absolute top-0 h-px bg-border"
                style={{
                  left: `calc(${(100 / node.children.length) * 0.5}% )`,
                  right: `calc(${(100 / node.children.length) * 0.5}% )`,
                }}
              />
            )}

            <div className="flex gap-8">
              {node.children.map((child) => (
                <div key={child.id} className="flex flex-col items-center">
                  {/* Vertical line down to child */}
                  <div className="w-px h-6 bg-border" />
                  <OrgTreeNode node={child} />
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
// Tree view — horizontal arrangement of root nodes
// ---------------------------------------------------------------------------

function TreeView({
  agentTree,
  humans,
}: {
  agentTree: OrgChartAgentNode[];
  humans: TeamDirectoryHuman[];
}) {
  return (
    <div className="space-y-10">
      {/* Agent tree */}
      {agentTree.length > 0 ? (
        <div>
          <h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-6">
            Agents
          </h3>
          <div className="overflow-x-auto pb-4">
            <div className="inline-flex gap-10 items-start">
              {agentTree.map((root) => (
                <OrgTreeNode key={root.id} node={root} />
              ))}
            </div>
          </div>
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">No agents in workspace.</p>
      )}

      {/* Humans */}
      {humans.length > 0 && (
        <div>
          <h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-4">
            Humans
          </h3>
          <div className="flex flex-wrap gap-4">
            {humans.map((h) => (
              <HumanCard key={h.id} human={h} />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Grid view (grouped by project)
// ---------------------------------------------------------------------------

function GridView({
  agentTree,
  humans,
}: {
  agentTree: OrgChartAgentNode[];
  humans: TeamDirectoryHuman[];
}) {
  // Flatten agent tree to a flat list.
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

  // Collect all unique project names.
  const projectSet = new Set<string>();
  for (const a of allAgents) {
    for (const p of a.projects ?? []) projectSet.add(p);
  }
  for (const h of humans) {
    for (const p of h.projects ?? []) projectSet.add(p);
  }
  const projects = Array.from(projectSet).sort();

  // Agents and humans with no projects go into an "Unassigned" bucket.
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
              {projectAgents.map((a) => (
                <AgentCard key={a.id} agent={a} />
              ))}
              {projectHumans.map((h) => (
                <HumanCard key={h.id} human={h} />
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
            {unassignedAgents.map((a) => (
              <AgentCard key={a.id} agent={a} />
            ))}
            {unassignedHumans.map((h) => (
              <HumanCard key={h.id} human={h} />
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
  const [viewMode, setViewMode] = useState<ViewMode>("tree");

  useEffect(() => {
    if (currentWorkspace) {
      void fetchOrgChart(currentWorkspace.id);
    }
  }, [currentWorkspace, fetchOrgChart]);

  const agentTree = orgChart?.agent_tree ?? [];
  const humans = orgChart?.humans ?? [];
  const workspaceName = orgChart?.workspace ?? currentWorkspace?.name ?? "";

  return (
    <div className="flex h-full flex-col">
      {/* Header bar */}
      <div className="flex items-center justify-between pb-4">
        <div>
          <h1 className="text-lg font-semibold">Team</h1>
          {workspaceName && (
            <p className="text-xs text-muted-foreground">{workspaceName}</p>
          )}
        </div>

        {/* View mode toggle */}
        <div className="flex items-center gap-1 rounded-lg border border-input bg-background p-1">
          <button
            type="button"
            onClick={() => setViewMode("tree")}
            className={cn(
              "flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
              viewMode === "tree"
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            <Network className="h-3.5 w-3.5" />
            Tree
          </button>
          <button
            type="button"
            onClick={() => setViewMode("grid")}
            className={cn(
              "flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium transition-colors",
              viewMode === "grid"
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            <LayoutGrid className="h-3.5 w-3.5" />
            By Project
          </button>
        </div>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto">
        {isOrgChartLoading ? (
          <OrgChartSkeleton />
        ) : viewMode === "tree" ? (
          <TreeView agentTree={agentTree} humans={humans} />
        ) : (
          <GridView agentTree={agentTree} humans={humans} />
        )}
      </div>
    </div>
  );
}

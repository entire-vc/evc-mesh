import { AgentShortTag } from "@/components/agent-short-tag";
import { agentLabel } from "@/lib/agent-label";
import { createContext, useContext, useEffect, useMemo, useState } from "react";
import { useLocation } from "react-router";
import { useTranslation } from "react-i18next";
import { Bot, Plus, UserPlus } from "lucide-react";
import { cn } from "@/lib/cn";
import { agentStatusConfig, asCapabilityList, displayStatus, isAgentStale } from "@/lib/agent-utils";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAgentStore } from "@/stores/agent";
import { useMemberStore } from "@/stores/member";
import { useRulesStore } from "@/stores/rules";
import { Avatar } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { ExecutionCardSummary } from "@/components/agent-execution-summary";
import { useAgentExecutions } from "@/hooks/use-agent-executions";
import { summarizeByAgent, type AgentExecutionSummary, type ExecutionLoad } from "@/lib/agent-executions";
import { useProjectStore } from "@/stores/project";
import { AgentDetailDialog } from "@/components/agent-detail-dialog";
import { InvitedAgentDialog } from "@/components/invited-agent-dialog";
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
  paused: "border-l-tone-neutral-400",
  online: "border-l-tone-success-500",
  busy: "border-l-tone-attention-500",
  offline: "border-l-tone-neutral-400",
  error: "border-l-tone-danger-500",
};

const STATUS_KEYS: Record<string, string> = {
  paused: "orgChart.status.paused",
  online: "orgChart.status.online",
  offline: "orgChart.status.offline",
  busy: "orgChart.status.busy",
  error: "orgChart.status.error",
};

/** First two capabilities as badges, the rest folded into "+N" (full list in title). */
function CapabilityBadges({ capabilities }: { capabilities: unknown }) {
  const caps = asCapabilityList(capabilities);
  if (caps.length === 0) return null;
  return (
    <div className="flex items-center gap-1 min-w-0" title={caps.join(", ")}>
      {caps.slice(0, 2).map((cap) => (
        <Badge key={cap} variant="outline" className="text-xs px-1 py-0 h-4 min-w-0 truncate">
          {cap}
        </Badge>
      ))}
      {caps.length > 2 && (
        <span className="text-xs text-muted-foreground shrink-0">+{caps.length - 2}</span>
      )}
    </div>
  );
}

function AgentCard({
  agent,
  onClick,
}: {
  agent: OrgChartAgentNode;
  onClick?: () => void;
}) {
  const { t } = useTranslation("integrations");
  // computed_status already folds heartbeat freshness in; a stale mark beside it would contradict it.
  const stale = !agent.computed_status && (agent.is_stale ?? isAgentStale(agent));
  // computed_status (live connection + heartbeat) wins over the raw column.
  const status = displayStatus(agent.status, agent.computed_status);
  const statusCfg = agentStatusConfig[status] ?? {
    label: status,
    dotColor: "bg-tone-neutral-400",
  };
  const borderColor = statusBorderColors[status] ?? "border-l-tone-neutral-400";
  const runnerModel = [agent.agent_type, agent.model].filter(Boolean).join(" · ");
  const { load, summaries } = useContext(ExecutionsContext);
  // With a projection the profile model is shown only as "profile" (unmanaged) or not at all.
  const showLegacyProfile = !hasProjection(load, summaries, agent.id);

  return (
    <Card
      className={cn(
        "w-52 shrink-0 border-l-4 select-none hover:shadow-md transition-shadow",
        borderColor,
        stale && status === "online" && "border-l-tone-attention-500",
        onClick ? "cursor-pointer" : "cursor-default",
      )}
      onClick={onClick}
    >
      <CardContent className="p-3 space-y-1.5">
        <div className="flex items-center gap-1.5 min-w-0">
          <Bot className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="font-medium text-sm truncate" title={agentLabel(agent.name, agent.short_tag)}>
            {agent.name}<AgentShortTag tag={agent.short_tag} />
          </span>
          {agent.is_home === false && (
            <Badge
              variant="outline"
              className="ml-auto shrink-0 px-1 py-0 h-4 text-[10px]"
              title={t("orgChart.invitedTitle")}
            >
              {t("orgChart.invited")}
            </Badge>
          )}
        </div>

        {agent.role && (
          <p className="text-xs text-muted-foreground truncate">{agent.role}</p>
        )}

        {showLegacyProfile && (agent.agent_type || agent.model) && (
          <p className="flex items-baseline gap-1 font-mono text-[11px] text-muted-foreground min-w-0">
            <span className="shrink-0 font-sans" data-testid="profile-model-label">
              {t("executions.profileLabel")}
            </span>
            <span className="truncate" title={runnerModel}>{runnerModel}</span>
          </p>
        )}

        <CardExecutions agentId={agent.id} />

        {agent.responsibility_zone && (
          <p className="text-xs text-muted-foreground truncate" title={agent.responsibility_zone}>
            {agent.responsibility_zone}
          </p>
        )}

        <CapabilityBadges capabilities={agent.capabilities} />

        <div className="flex items-center gap-1.5">
          <span className={cn("h-2 w-2 rounded-full shrink-0", statusCfg.dotColor)} />
          <span className="text-xs text-muted-foreground">
            {STATUS_KEYS[status] ? t(STATUS_KEYS[status]!) : statusCfg.label}
            {stale && status === "online" && ` ${t("orgChart.stale")}`}
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
    <Card className="w-52 shrink-0 border-l-4 border-l-tone-info-400 hover:shadow-md transition-shadow cursor-default">
      <CardContent className="p-3 space-y-1.5">
        <div className="flex items-center gap-1.5 min-w-0">
          <Avatar src={human.avatar_url || undefined} name={human.name || human.email} size="sm" />
          <span className="font-medium text-sm truncate" title={human.name}>
            {human.name}
          </span>
        </div>

        <p className="text-xs text-muted-foreground truncate">{human.email}</p>

        {(human.role || human.availability) && (
          <div className="flex flex-wrap items-center gap-1">
            {human.role && (
              <Badge variant="outline" className="text-xs px-1 py-0 h-4">
                {human.role}
              </Badge>
            )}
            {human.availability && (
              <Badge variant="secondary" className="text-xs px-1 py-0 h-4 capitalize">
                {human.availability}
              </Badge>
            )}
          </div>
        )}

        {human.responsibility_zone && (
          <p className="text-xs text-muted-foreground truncate" title={human.responsibility_zone}>
            {human.responsibility_zone}
          </p>
        )}

        <CapabilityBadges capabilities={human.capabilities} />

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
// Scoped execution projection shared by every card (one fetch per page)
// ---------------------------------------------------------------------------

interface ExecutionsCtx {
  load: ExecutionLoad | "loading";
  summaries: Map<string, AgentExecutionSummary>;
  projectName: (projectId: string) => string;
}

const ExecutionsContext = createContext<ExecutionsCtx>({
  load: "loading",
  summaries: new Map(),
  projectName: (id) => id,
});

/** Run block of a card; the legacy profile line stays only where there is no projection to trust. */
function CardExecutions({ agentId }: { agentId: string }) {
  const { t } = useTranslation("integrations");
  const { load, summaries, projectName } = useContext(ExecutionsContext);
  if (load === "loading") {
    return (
      <div role="status" aria-busy="true" aria-label={t("executions.loading")}>
        <Skeleton className="h-8 w-full" />
      </div>
    );
  }
  if (load.kind === "error") {
    return (
      <p data-testid="exec-error" role="status" className="text-[11px] text-tone-attention-600">
        {t("executions.error")}
      </p>
    );
  }
  if (load.kind === "forbidden") return null;
  const summary = summaries.get(agentId);
  if (!summary) return null;
  return <ExecutionCardSummary summary={summary} projectName={projectName} />;
}

function hasProjection(load: ExecutionsCtx["load"], summaries: ExecutionsCtx["summaries"], agentId: string) {
  return load !== "loading" && load.kind === "ok" && summaries.has(agentId);
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
  const { t } = useTranslation("integrations");
  const unifiedRoots = useMemo(
    () => buildUnifiedTree(agentTree, humans),
    [agentTree, humans],
  );

  if (unifiedRoots.length === 0) {
    return <p className="text-sm text-muted-foreground">{t("orgChart.empty")}</p>;
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
  const { t } = useTranslation("integrations");
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
            {t("orgChart.noProject")}
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
        <p className="text-sm text-muted-foreground">{t("orgChart.empty")}</p>
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

function overlayAgentEdits(tree: OrgChartAgentNode[], agents: Agent[]): OrgChartAgentNode[] {
  if (agents.length === 0) return tree;
  const byId = new Map(agents.map((a) => [a.id, a]));
  const walk = (nodes: OrgChartAgentNode[]): OrgChartAgentNode[] =>
    nodes.map((n) => {
      const a = byId.get(n.id);
      return {
        ...n,
        ...(a && { name: a.name, short_tag: a.short_tag, role: a.role, agent_type: a.agent_type, model: a.model ?? null }),
        children: walk(n.children),
      };
    });
  return walk(tree);
}

/** Paused agents leave the main tree; their live children are hoisted so none is lost. */
function splitPaused(tree: OrgChartAgentNode[]): { active: OrgChartAgentNode[]; paused: OrgChartAgentNode[] } {
  const paused: OrgChartAgentNode[] = [];
  const walk = (nodes: OrgChartAgentNode[]): OrgChartAgentNode[] =>
    nodes.flatMap((n) => {
      const kids = walk(n.children ?? []);
      if (n.computed_status === "paused") {
        paused.push({ ...n, children: [] });
        return kids;
      }
      return [{ ...n, children: kids }];
    });
  const active = walk(tree);
  return { active, paused };
}

function PausedSection({ agents, onAgentClick }: { agents: OrgChartAgentNode[]; onAgentClick: (agentId: string) => void }) {
  const { t } = useTranslation("integrations");
  if (agents.length === 0) return null;
  return (
    <details data-testid="org-chart-paused" className="mt-6 rounded-md border border-border p-3">
      <summary className="cursor-pointer text-sm font-medium">
        {t("orgChart.pausedSection", { count: agents.length })}
      </summary>
      <div className="mt-3 flex flex-wrap gap-4">
        {agents.map((a) => (
          <AgentCard key={a.id} agent={a} onClick={() => onAgentClick(a.id)} />
        ))}
      </div>
    </details>
  );
}

export function OrgChartPage() {
  const { t } = useTranslation("integrations");
  const { currentWorkspace } = useWorkspaceStore();
  const { orgChart, isOrgChartLoading, fetchOrgChart } = useRulesStore();
  const { agents, fetchAgents } = useAgentStore();
  const { myRole, fetchMyRole } = useMemberStore();
  const { projects, fetchProjects } = useProjectStore();
  const [projectFilter, setProjectFilter] = useState<string>("");
  const { load: executions } = useAgentExecutions(currentWorkspace?.id, { projectId: projectFilter || null });
  const summaries = useMemo(
    () => (executions !== "loading" && executions.kind === "ok" ? summarizeByAgent(executions.items) : new Map<string, AgentExecutionSummary>()),
    [executions],
  );
  const executionsCtx = useMemo<ExecutionsCtx>(
    () => ({
      load: executions,
      summaries,
      projectName: (id) => projects.find((p) => p.id === id)?.name ?? id,
    }),
    [executions, summaries, projects],
  );

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
      void fetchMyRole(currentWorkspace.id);
      void fetchProjects(currentWorkspace.id);
    }
  }, [currentWorkspace, fetchOrgChart, fetchAgents, fetchMyRole, fetchProjects]);

  // The org-chart payload is fetched separately from the agents list that the
  // detail dialog edits, so overlay the edited fields from the agents store —
  // otherwise cards stay stale until the next org-chart refetch.
  const agentTree = useMemo(
    () => overlayAgentEdits(orgChart?.agent_tree ?? [], agents),
    [orgChart, agents],
  );
  const { active: activeTree, paused: pausedAgents } = useMemo(() => splitPaused(agentTree), [agentTree]);
  const humans = orgChart?.humans ?? [];
  const workspaceName = orgChart?.workspace ?? currentWorkspace?.name ?? "";

  // Invited (guest) agents are not in the workspace's own agents list, so the
  // regular detail dialog cannot open them — they get their own card.
  const selectedGuest: OrgChartAgentNode | null = useMemo(() => {
    if (!selectedAgentId) return null;
    const walk = (nodes: OrgChartAgentNode[]): OrgChartAgentNode | null => {
      for (const n of nodes) {
        if (n.id === selectedAgentId) return n.is_home === false ? n : null;
        const hit = walk(n.children ?? []);
        if (hit) return hit;
      }
      return null;
    };
    return walk(agentTree);
  }, [agentTree, selectedAgentId]);

  const selectedAgent: Agent | null =
    selectedAgentId && !selectedGuest
      ? agents.find((a) => a.id === selectedAgentId) ?? null
      : null;

  // The agents list is paginated and can lag the org chart: an agent that is on
  // the chart but not in the store is loaded on click instead of a dead card.
  const { fetchAgent } = useAgentStore();
  useEffect(() => {
    if (selectedAgentId && !selectedGuest && !agents.some((a) => a.id === selectedAgentId)) {
      void fetchAgent(selectedAgentId).catch(() => setSelectedAgentId(null));
    }
  }, [selectedAgentId, selectedGuest, agents, fetchAgent]);

  const selectedComputedStatus = useMemo(() => {
    const walk = (nodes: OrgChartAgentNode[]): string | null => {
      for (const n of nodes) {
        if (n.id === selectedAgentId) return n.computed_status ?? null;
        const hit = walk(n.children ?? []);
        if (hit) return hit;
      }
      return null;
    };
    return walk(agentTree);
  }, [agentTree, selectedAgentId]);

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

        <div className="flex flex-wrap items-center justify-end gap-2">
          <Select
            aria-label={t("executions.project")}
            value={projectFilter}
            onChange={(e) => setProjectFilter(e.target.value)}
            className="h-8 w-44 text-xs"
          >
            <option value="">{t("executions.allProjects")}</option>
            {projects
              .filter((p) => !p.is_archived)
              .map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
          </Select>
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5"
            onClick={() => setRegisterOpen(true)}
          >
            <Plus className="h-3.5 w-3.5" />
            {t("orgChart.registerAgent")}
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5"
            onClick={() => setInviteOpen(true)}
          >
            <UserPlus className="h-3.5 w-3.5" />
            {t("orgChart.inviteMember")}
          </Button>

        </div>
      </div>

      {executions !== "loading" && executions.kind === "ok" && executions.partial && (
        <p role="status" className="pb-2 text-xs text-tone-attention-600">
          {t("executions.partial")}
        </p>
      )}

      {/* Content */}
      <div className="flex-1 overflow-y-auto">
        <ExecutionsContext.Provider value={executionsCtx}>
        {isOrgChartLoading ? (
          <OrgChartSkeleton />
        ) : viewMode === "tree" ? (
          <TreeView agentTree={activeTree} humans={humans} onAgentClick={handleAgentClick} />
        ) : (
          <GridView agentTree={activeTree} humans={humans} onAgentClick={handleAgentClick} />
        )}
        {!isOrgChartLoading && <PausedSection agents={pausedAgents} onAgentClick={handleAgentClick} />}
        </ExecutionsContext.Provider>
      </div>

      {/* Agent detail dialog */}
      <AgentDetailDialog
        open={selectedAgentId !== null && !selectedGuest}
        onOpenChange={(open) => {
          if (!open) {
            setSelectedAgentId(null);
            // An edit, pause or delete in the dialog must show on the chart.
            if (currentWorkspace) void fetchOrgChart(currentWorkspace.id);
          }
        }}
        agent={selectedAgent}
        computedStatus={selectedComputedStatus}
      />

      {/* Register agent dialog */}
      {currentWorkspace && (
        <>
          <InvitedAgentDialog
            open={selectedGuest !== null}
            onClose={() => setSelectedAgentId(null)}
            onChanged={() => void fetchOrgChart(currentWorkspace.id)}
            workspaceId={currentWorkspace.id}
            agent={selectedGuest}
            canManage={myRole === "owner" || myRole === "admin"}
          />
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

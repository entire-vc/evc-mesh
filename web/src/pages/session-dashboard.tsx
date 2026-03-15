import { useEffect } from "react";
import { Link, useParams } from "react-router";
import {
	Activity,
	AlertCircle,
	Bot,
	Clock,
	DollarSign,
	Info,
	Wifi,
	WifiOff,
} from "lucide-react";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import { agentStatusConfig, agentTypeConfig, isAgentStale } from "@/lib/agent-utils";
import { useWorkspaceStore } from "@/stores/workspace";
import { useAgentStore } from "@/stores/agent";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type { Agent } from "@/types";

// ---------------------------------------------------------------------------
// AgentSessionCard
// ---------------------------------------------------------------------------

interface AgentSessionCardProps {
	agent: Agent;
}

function AgentSessionCard({ agent }: AgentSessionCardProps) {
	const stale = isAgentStale(agent);
	const statusCfg = agentStatusConfig[agent.status];
	const typeCfg = agentTypeConfig[agent.agent_type] ?? {
		label: agent.agent_type,
		color: "bg-gray-100 text-gray-700",
	};

	// Tool count from heartbeat metadata (if the agent reports it).
	const toolCount: number | null =
		typeof agent.heartbeat_metadata?.tool_count === "number"
			? (agent.heartbeat_metadata.tool_count as number)
			: null;

	return (
		<Card className="flex flex-col gap-3 p-4">
			<div className="flex items-start justify-between gap-2">
				<div className="flex items-center gap-2 min-w-0">
					<div
						className={cn(
							"flex h-8 w-8 shrink-0 items-center justify-center rounded-lg",
							typeCfg.color,
						)}
					>
						<Bot className="h-4 w-4" />
					</div>
					<div className="min-w-0">
						<p className="truncate text-sm font-semibold">{agent.name}</p>
						<Badge variant="secondary" className={cn("mt-0.5 text-xs", typeCfg.color)}>
							{typeCfg.label}
						</Badge>
					</div>
				</div>

				{/* Status dot */}
				<div className="flex shrink-0 items-center gap-1.5">
					<span
						className={cn(
							"h-2.5 w-2.5 rounded-full",
							statusCfg.dotColor,
							stale && "opacity-50",
						)}
					/>
					<span className="text-xs text-muted-foreground">{statusCfg.label}</span>
					{stale && (
						<AlertCircle className="h-3 w-3 text-yellow-500" title="Heartbeat stale" />
					)}
				</div>
			</div>

			{/* Heartbeat info */}
			<div className="space-y-1.5 text-xs text-muted-foreground">
				<div className="flex items-center gap-1.5">
					{agent.last_heartbeat ? (
						<>
							<Wifi className="h-3 w-3 shrink-0" />
							<span>Last seen {formatRelative(agent.last_heartbeat)}</span>
						</>
					) : (
						<>
							<WifiOff className="h-3 w-3 shrink-0" />
							<span>Never connected</span>
						</>
					)}
				</div>

				{agent.heartbeat_message && (
					<div className="flex items-start gap-1.5">
						<Info className="h-3 w-3 mt-0.5 shrink-0" />
						<span className="line-clamp-2">{agent.heartbeat_message}</span>
					</div>
				)}

				{toolCount !== null && (
					<div className="flex items-center gap-1.5">
						<Activity className="h-3 w-3 shrink-0" />
						<span>{toolCount} tool calls in last session</span>
					</div>
				)}

				{agent.current_task_id && (
					<div className="flex items-center gap-1.5">
						<Clock className="h-3 w-3 shrink-0" />
						<span className="truncate">
							Working on task{" "}
							<code className="font-mono text-[10px]">
								{agent.current_task_id.slice(0, 8)}…
							</code>
						</span>
					</div>
				)}
			</div>
		</Card>
	);
}

// ---------------------------------------------------------------------------
// SessionDashboardPage
// ---------------------------------------------------------------------------

export function SessionDashboardPage() {
	const { wsSlug } = useParams<{ wsSlug: string }>();
	const { currentWorkspace } = useWorkspaceStore();
	const { agents, isLoading, fetchAgents } = useAgentStore();

	useEffect(() => {
		if (currentWorkspace?.id) {
			fetchAgents(currentWorkspace.id);
		}
	}, [currentWorkspace?.id, fetchAgents]);

	const onlineCount = agents.filter((a) => a.status === "online").length;
	const busyCount = agents.filter((a) => a.status === "busy").length;
	const offlineCount = agents.filter((a) =>
		a.status === "offline" || a.status === "error",
	).length;

	return (
		<div className="space-y-6">
			{/* Summary chips */}
			<div className="flex flex-wrap gap-3">
				<div className="flex items-center gap-2 rounded-lg border bg-card px-3 py-2 text-sm">
					<span className="h-2 w-2 rounded-full bg-green-500" />
					<span className="font-medium">{onlineCount}</span>
					<span className="text-muted-foreground">online</span>
				</div>
				<div className="flex items-center gap-2 rounded-lg border bg-card px-3 py-2 text-sm">
					<span className="h-2 w-2 rounded-full bg-yellow-500" />
					<span className="font-medium">{busyCount}</span>
					<span className="text-muted-foreground">busy</span>
				</div>
				<div className="flex items-center gap-2 rounded-lg border bg-card px-3 py-2 text-sm">
					<span className="h-2 w-2 rounded-full bg-gray-400" />
					<span className="font-medium">{offlineCount}</span>
					<span className="text-muted-foreground">offline / error</span>
				</div>
			</div>

			{/* Agent cards */}
			{isLoading ? (
				<div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
					{[1, 2, 3].map((i) => (
						<Skeleton key={i} className="h-40 w-full rounded-xl" />
					))}
				</div>
			) : agents.length === 0 ? (
				<div className="flex flex-col items-center gap-3 py-16 text-center text-muted-foreground">
					<Bot className="h-10 w-10 opacity-40" />
					<p className="text-sm">No agents registered in this workspace.</p>
					<Link
						to={wsSlug ? `/w/${wsSlug}/org-chart` : "/"}
						className="text-sm text-primary underline-offset-4 hover:underline"
					>
						Go to Team page to register agents
					</Link>
				</div>
			) : (
				<div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
					{agents.map((agent) => (
						<AgentSessionCard key={agent.id} agent={agent} />
					))}
				</div>
			)}

			{/* Cost Tracking section */}
			<Card>
				<CardHeader>
					<CardTitle className="flex items-center gap-2 text-base">
						<DollarSign className="h-4 w-4" />
						Cost Tracking
					</CardTitle>
				</CardHeader>
				<CardContent className="space-y-3 text-sm text-muted-foreground">
					<p>
						Cost data is available when agents use the{" "}
						<code className="rounded bg-muted px-1 py-0.5 text-xs font-mono">
							session_report
						</code>{" "}
						MCP tool at the end of each session.
					</p>
					<p>
						To enable cost reporting, configure your agent to call{" "}
						<code className="rounded bg-muted px-1 py-0.5 text-xs font-mono">
							session_report
						</code>{" "}
						with token usage and model information before exiting. Cost data will
						then appear here in a future update.
					</p>
					<div className="rounded-lg border border-dashed p-3 text-xs">
						<p className="font-medium text-foreground mb-1">Example MCP call:</p>
						<pre className="overflow-x-auto text-muted-foreground">
{`session_report({
  "tasks_completed": 3,
  "tokens_used": 12400,
  "model": "claude-sonnet-4-5",
  "cost_usd": 0.042
})`}
						</pre>
					</div>
				</CardContent>
			</Card>
		</div>
	);
}

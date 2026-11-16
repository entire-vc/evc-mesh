// One agent's card: type, status and presence ("last run" for ephemeral agents),
// the latest heartbeat (expandable) and the current task. Drawn by the
// Sessions page and by the dashboard's agents-status widget, so both show the
// same thing from GET /workspaces/:ws/agents/status.
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowUpRight, Bot, Clock, Info, Wifi, WifiOff } from "lucide-react";
import { TaskLink } from "@entire-vc/mesh-theme-sdk";
import i18n from "@/i18n";
import { AgentShortTag } from "@/components/agent-short-tag";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/utils";
import { agentTypeConfig } from "@/lib/agent-utils";
import { agentState } from "@/lib/agent-state";
import { useRulesStore } from "@/stores/rules";
import { useWorkspaceStore } from "@/stores/workspace";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface AgentStatusEntry {
	id: string;
	name: string;
	status: "online" | "busy" | "offline" | "error";
	agent_type?: string;
	heartbeat_status?: string;
	last_heartbeat_at: string | null;
	seconds_since_heartbeat: number | null;
	is_stale: boolean;
	heartbeat_message?: string;
	current_task_id?: string | null;
	current_task_title?: string;
}

export interface AgentsStatusResponse {
	agents: AgentStatusEntry[];
	stale_count: number;
	working_count: number;
	total_count: number;
}

// ---------------------------------------------------------------------------
// Status config
// ---------------------------------------------------------------------------

export const statusConfig: Record<
	AgentStatusEntry["status"],
	{ labelKey: string; dotColor: string }
> = {
	online: { labelKey: "integrations:sessions.status.online", dotColor: "bg-tone-success-500" },
	busy: { labelKey: "integrations:sessions.status.busy", dotColor: "bg-tone-attention-500" },
	offline: { labelKey: "integrations:sessions.status.offline", dotColor: "bg-tone-neutral-400" },
	error: { labelKey: "integrations:sessions.status.error", dotColor: "bg-tone-danger-500" },
};

// ---------------------------------------------------------------------------
// Presence label for ephemeral (offline) agents
// ---------------------------------------------------------------------------

const EPHEMERAL_THRESHOLD_MS = 24 * 60 * 60 * 1000;

// "last run" is told only from the agent's own heartbeat: an online agent whose
// heartbeat went stale. An offline row never sent one; its last_heartbeat is
// moved by any API call with its key (the runner polls every lane), so a fresh
// value there is not a run and the card says Offline.
export function getEphemeralPresenceLabel(agent: AgentStatusEntry): string {
	const state = agentState(agent);
	if (state !== "offline") return i18n.t(statusConfig[state].labelKey);
	if (!agent.last_heartbeat_at) return i18n.t("integrations:sessions.neverSeen");
	if (agent.status === "offline") return i18n.t("integrations:sessions.status.offline");
	const ageMs = Date.now() - new Date(agent.last_heartbeat_at).getTime();
	if (ageMs > EPHEMERAL_THRESHOLD_MS) return i18n.t("integrations:sessions.status.offline");
	return i18n.t("integrations:sessions.lastRun", { when: formatRelative(agent.last_heartbeat_at) });
}

// ---------------------------------------------------------------------------
// AgentSessionCard
// ---------------------------------------------------------------------------

// The JSX used to slice heartbeat_message at 80 and
// current_task_title at 60 chars, so the tail of server-provided text never
// reached the DOM and no CSS or interaction could recover it. Rendering now
// caps only at an absurd length; visible truncation is CSS line-clamp, and a
// long row becomes a tap-to-expand button (aria-expanded toggles the clamp).
const TEXT_CAP = 500;
const STATUS_EXPAND_THRESHOLD = 80; // where the old "…" used to appear
const TASK_EXPAND_THRESHOLD = 60;

function capped(s: string): string {
	return s.length > TEXT_CAP ? s.slice(0, TEXT_CAP) + "…" : s;
}

/**
 * The card's role tag (`Name · role`) comes from the team directory store, which
 * nothing on the dashboard loads. A list of cards calls this once so the tags
 * show wherever the cards are drawn, not only after another page filled the store.
 */
export function useAgentCardDirectory(): void {
	const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
	const loaded = useRulesStore((s) => s.teamDirectory !== null);
	useEffect(() => {
		if (wsId && !loaded) void useRulesStore.getState().fetchTeamDirectory(wsId);
	}, [wsId, loaded]);
}

interface AgentSessionCardProps {
	agent: AgentStatusEntry;
}

// Exported for component tests: the page-level data flow is mocked elsewhere,
// the card itself is what an internal ticket exercises.
export function AgentSessionCard({ agent }: AgentSessionCardProps) {
	const { t } = useTranslation("integrations");
	const [statusExpanded, setStatusExpanded] = useState(false);
	const [taskExpanded, setTaskExpanded] = useState(false);
	const state = agentState(agent);
	const statusCfg = statusConfig[state];
	const typeCfg = agentTypeConfig[agent.agent_type as keyof typeof agentTypeConfig] ?? {
		// An agent registered without a type comes back as "" — still the fallback, never an empty badge.
		label: agent.agent_type || t("sessions.agentFallback"),
		color: "bg-tone-neutral-100 text-tone-neutral-700",
	};
	const presenceLabel = getEphemeralPresenceLabel(agent);
	const isEffectivelyOffline = state === "offline" || state === "error";

	return (
		<Card className="flex flex-col gap-3 p-4">
			{/* The status used to sit in a fixed right column, so on a
			    narrow card it squeezed the name down to "L…" and wrapped the type
			    badge onto two lines. Name and status now share one wrapping row:
			    while both fit they stay on one line (status pushed right); when
			    they don't, the status drops under the name and the name gets the
			    whole width — it truncates only when it alone is wider than the
			    card. The badge never wraps. */}
			<div className="flex items-start gap-2">
				<div
					className={cn(
						"flex h-8 w-8 shrink-0 items-center justify-center rounded-lg",
						typeCfg.color,
					)}
				>
					<Bot className="h-4 w-4" />
				</div>
				<div className="min-w-0 flex-1">
					<div
						className="flex flex-wrap items-center justify-between gap-x-2 gap-y-0.5"
						data-testid="agent-card-head"
					>
						<p className="min-w-0 max-w-full truncate text-sm font-semibold">
							{agent.name}
							<AgentShortTag id={agent.id} />
						</p>
						{/* Status dot */}
						<div className="flex shrink-0 items-center gap-1.5">
							<span
								className={cn(
									"h-2.5 w-2.5 shrink-0 rounded-full",
									statusCfg.dotColor,
								)}
							/>
							<span className="whitespace-nowrap text-xs text-muted-foreground">{presenceLabel}</span>
						</div>
					</div>
					<Badge
						variant="secondary"
						className={cn("mt-0.5 whitespace-nowrap text-xs", typeCfg.color)}
					>
						{typeCfg.label}
					</Badge>
				</div>
			</div>

			{/* Presence info — only for active agents; offline already shows last-run in status */}
			<div className="space-y-1.5 text-xs text-muted-foreground">
				{!isEffectivelyOffline && (
				<div className="flex items-center gap-1.5">
					{agent.last_heartbeat_at ? (
						<>
							<Wifi className="h-3 w-3 shrink-0" />
							<span>{t("sessions.lastSeen", { when: formatRelative(agent.last_heartbeat_at) })}</span>
						</>
					) : (
						<>
							<WifiOff className="h-3 w-3 shrink-0" />
							<span>{t("sessions.neverConnected")}</span>
						</>
					)}
				</div>
				)}

				{agent.heartbeat_message && (
					<div className="flex items-start gap-1.5">
						<Info className="h-3 w-3 mt-0.5 shrink-0" />
						{agent.heartbeat_message.length > STATUS_EXPAND_THRESHOLD ? (
							<button
								type="button"
								aria-expanded={statusExpanded}
								onClick={() => setStatusExpanded((v) => !v)}
								className="flex min-w-0 flex-1 cursor-pointer items-center text-left"
							>
								{/* The clamp lives on this auto-height span, never on the
								    button: the 44px touch-area audit makes the button box
								    taller than two lines, and Blink then paints the top
								    sliver of a third line under the clamp — the clip edge
								    is the padding box, so padding cannot fix it either. An
								    auto-height clamp box is cut at exactly two lines
								    (verified pixel-level at 393; invisible to jsdom). */}
								<span
									className={cn(
										"min-w-0 break-words",
										!statusExpanded && "line-clamp-2",
									)}
								>
									{capped(agent.heartbeat_message)}
								</span>
							</button>
						) : (
							<span className="line-clamp-2">{agent.heartbeat_message}</span>
						)}
					</div>
				)}

				{agent.current_task_id && (
					<div className="flex items-start gap-1.5">
						<Clock className="h-3 w-3 mt-0.5 shrink-0" />
						<span className="min-w-0">
							{t("sessions.workingOn")}{" "}
							{agent.current_task_title &&
							agent.current_task_title.length > TASK_EXPAND_THRESHOLD ? (
								<>
									{/* Mobile (<768): tap the clamped text to read it in place;
									    navigation moves to a separate arrow link so the two
									    affordances never nest. */}
									<button
										type="button"
										aria-expanded={taskExpanded}
										onClick={() => setTaskExpanded((v) => !v)}
										className="flex min-w-0 cursor-pointer items-center text-left md:hidden"
									>
										{/* Same invariant as the status row above: the clamp
										    sits on the auto-height span so the 44px touch
										    area cannot expose a third-line sliver. */}
										<span
											className={cn(
												"min-w-0 break-words",
												!taskExpanded && "line-clamp-2",
											)}
										>
											{capped(agent.current_task_title)}
										</span>
									</button>
									{/* TaskLink (PRD 5.8): a plain click goes through the host's
									    openTask, a modified click keeps the real href. It takes no
									    aria-label, so the icon's name is visually hidden text. */}
									<TaskLink
										taskId={agent.current_task_id}
										className="text-primary underline-offset-2 hover:underline md:hidden"
									>
										<ArrowUpRight className="inline h-3 w-3" aria-hidden="true" />
										<span className="sr-only">{t("sessions.openTask")}</span>
									</TaskLink>
									{/* Desktop (≥768): the original single-line link, minus the
									    slice. Both display classes are !important: a plain
									    `hidden` loses to the mobile-scope rule
									    `.mesh-mobile-sessions a { display: inline-flex }`
									    (higher specificity), and a plain `md:inline-block`
									    then loses to our own `hidden!` — the bangs pair up so
									    the link is none below md and inline-block from md up.
									    inline-block, not inline: CSS truncate only clips
									    block-level boxes, which is what keeps the "…" at the
									    card edge on wide viewports. */}
									<TaskLink
										taskId={agent.current_task_id}
										className="hidden! md:inline-block! md:max-w-full md:truncate text-primary underline-offset-2 hover:underline"
									>
										{capped(agent.current_task_title)}
									</TaskLink>
								</>
							) : (
								<TaskLink
									taskId={agent.current_task_id}
									className="break-words whitespace-normal text-primary underline-offset-2 hover:underline md:truncate"
								>
									{agent.current_task_title
										? capped(agent.current_task_title)
										: agent.current_task_id.slice(0, 8) + "…"}
								</TaskLink>
							)}
						</span>
					</div>
				)}
			</div>
		</Card>
	);
}


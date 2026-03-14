import type { Agent, AgentStatus, AgentType } from "@/types";

export const agentTypeConfig: Record<
  AgentType,
  { label: string; color: string }
> = {
  claude_code: { label: "Claude Code", color: "bg-purple-100 text-purple-700" },
  openclaw: { label: "OpenClaw", color: "bg-blue-100 text-blue-700" },
  cline: { label: "Cline", color: "bg-green-100 text-green-700" },
  aider: { label: "Aider", color: "bg-orange-100 text-orange-700" },
  custom: { label: "Custom", color: "bg-gray-100 text-gray-700" },
};

export const agentStatusConfig: Record<
  AgentStatus,
  { label: string; dotColor: string }
> = {
  online: { label: "Online", dotColor: "bg-green-500" },
  offline: { label: "Offline", dotColor: "bg-gray-400" },
  busy: { label: "Busy", dotColor: "bg-yellow-500" },
  error: { label: "Error", dotColor: "bg-red-500" },
};

const STALE_THRESHOLD_MS = 15 * 60 * 1000; // 15 minutes

/** Returns true if the agent's heartbeat is older than 15 minutes. */
export function isAgentStale(agent: { last_heartbeat?: string | null }): boolean {
  if (!agent.last_heartbeat) return true;
  return Date.now() - new Date(agent.last_heartbeat).getTime() > STALE_THRESHOLD_MS;
}

/** Returns the effective display status considering staleness. */
export function getEffectiveStatus(agent: Agent): AgentStatus {
  if (agent.status === "online" && isAgentStale(agent)) return "offline";
  return agent.status;
}

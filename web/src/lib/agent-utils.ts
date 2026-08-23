import type { Agent, AgentStatus, AgentType } from "@/types";
import { formatRelative } from "@/lib/utils";

export const agentTypeConfig: Record<
  AgentType,
  { label: string; color: string }
> = {
  claude_code: { label: "Claude Code", color: "bg-purple-100 text-purple-700" },
  openclaw: { label: "OpenClaw", color: "bg-blue-100 text-blue-700" },
  cline: { label: "Cline", color: "bg-green-100 text-green-700" },
  aider: { label: "Aider", color: "bg-orange-100 text-orange-700" },
  custom: { label: "Custom", color: "bg-gray-100 text-gray-700" },
  hermes: { label: "Hermes", color: "bg-teal-100 text-teal-700" },
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

/** Returns the DB status as-is. Staleness is shown as a visual warning, not a status override. */
export function getEffectiveStatus(agent: Agent): AgentStatus {
  return agent.status;
}

const EPHEMERAL_THRESHOLD_MS = 24 * 60 * 60 * 1000; // 24h

/**
 * Returns a human-readable presence label for an agent card.
 * Offline agents with a recent heartbeat (< 24h) show "last run X ago" instead
 * of "Offline" — ephemeral agents are between spawns, not broken.
 * Offline + no heartbeat ever → "Never seen".
 * Offline + last heartbeat > 24h ago → "Offline" (genuinely inactive).
 */
export function getAgentPresenceLabel(
  agent: { status: AgentStatus; last_heartbeat: string | null },
): string {
  if (agent.status !== "offline") {
    return agentStatusConfig[agent.status]?.label ?? agent.status;
  }
  if (!agent.last_heartbeat) {
    return "Never seen";
  }
  const ageMs = Date.now() - new Date(agent.last_heartbeat).getTime();
  if (ageMs > EPHEMERAL_THRESHOLD_MS) {
    return "Offline";
  }
  return `last run ${formatRelative(agent.last_heartbeat)}`;
}

/**
 * Capabilities are stored as a JSON object ({}) in Postgres, but some API
 * responses shape it as string[] instead. Normalize either into a plain
 * string list for display/editing.
 */
export function asCapabilityList(v: unknown): string[] {
  if (Array.isArray(v)) return v;
  if (v && typeof v === "object") return Object.keys(v);
  return [];
}

/** Splits a comma-separated form field into trimmed, non-empty entries. */
export function splitList(v: string): string[] {
  return v
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

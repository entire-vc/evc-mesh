import type { Agent, AgentStatus, AgentType } from "@/types";

export const agentTypeConfig: Record<
  AgentType,
  { label: string; color: string }
> = {
  claude_code: { label: "Claude Code", color: "bg-tone-purple-100 text-tone-purple-700" },
  openclaw: { label: "OpenClaw", color: "bg-tone-info-100 text-tone-info-700" },
  cline: { label: "Cline", color: "bg-tone-success-100 text-tone-success-700" },
  aider: { label: "Aider", color: "bg-tone-high-100 text-tone-high-700" },
  custom: { label: "Custom", color: "bg-tone-neutral-100 text-tone-neutral-700" },
  hermes: { label: "Hermes", color: "bg-tone-teal-100 text-tone-teal-700" },
  codex: { label: "Codex", color: "bg-tone-slate-100 text-tone-slate-700" },
  cursor: { label: "Cursor", color: "bg-tone-indigo-100 text-tone-indigo-700" },
  copilot: { label: "GitHub Copilot", color: "bg-tone-zinc-100 text-tone-zinc-700" },
  gemini_cli: { label: "Gemini CLI", color: "bg-tone-sky-100 text-tone-sky-700" },
};

/**
 * Same lookup as agentTypeConfig, but safe for a value that isn't known to be
 * an AgentType at compile time (e.g. a Spark marketplace manifest's agent_type,
 * typed AgentType | string because a listing can name an unrecognized harness).
 * Falls back to the raw string as the label with a neutral color.
 */
export function agentTypeDisplay(agentType: string): { label: string; color: string } {
  return (
    (agentTypeConfig as Record<string, { label: string; color: string }>)[agentType] ?? {
      label: agentType,
      color: "bg-tone-neutral-100 text-tone-neutral-700",
    }
  );
}

export const agentStatusConfig: Record<
  AgentStatus,
  { label: string; dotColor: string }
> = {
  online: { label: "Online", dotColor: "bg-tone-success-500" },
  offline: { label: "Offline", dotColor: "bg-tone-neutral-400" },
  busy: { label: "Busy", dotColor: "bg-tone-attention-500" },
  error: { label: "Error", dotColor: "bg-tone-danger-500" },
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

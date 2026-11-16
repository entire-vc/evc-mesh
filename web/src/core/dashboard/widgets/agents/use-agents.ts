// Data hooks of the agent widgets. No fetch in a view: views read these.
//
// Refresh: a 30 s poll, as the Sessions page and the old dashboard card did.
// The event bus carries no agent status change today (heartbeats only update
// the agents row), so there is nothing to subscribe to; once such an event
// exists it plugs in here and the poll stays as the fallback.
import { useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { useWorkspaceStore } from "@/stores/workspace";
import type { TeamDirectory, TeamDirectoryAgent } from "@/types";
import type { Async } from "../../types";
import { agentState } from "@/lib/agent-state";
import { type AgentStatusEntry, type AgentsStatusResponse } from "./agent-card";

export const AGENTS_POLL_MS = 30_000;

export type AgentStatus = AgentStatusEntry["status"];
export const AGENT_STATUSES: readonly AgentStatus[] = ["online", "busy", "offline", "error"];

/**
 * One workspace-scoped read with polling. A failed first read is an error with
 * a retry (never "no agents"); a failed poll keeps the last good data, as the
 * Sessions page did. Data of another workspace is never shown as ready.
 */
function usePolled<T>(load: (wsId: string) => Promise<T>, refreshMs: number): Async<T> {
  const wsId = useWorkspaceStore((s) => s.currentWorkspace?.id);
  const [loaded, setLoaded] = useState<{ wsId: string; value: T } | { wsId: string; error: Error } | null>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!wsId) return;
    let live = true;
    const run = () =>
      load(wsId)
        .then((value) => live && setLoaded({ wsId, value }))
        .catch((e) => {
          if (!live) return;
          const error = e instanceof Error ? e : new Error(String(e));
          setLoaded((prev) => (prev && prev.wsId === wsId && "value" in prev ? prev : { wsId, error }));
        });
    void run();
    const id = setInterval(() => void run(), refreshMs);
    return () => {
      live = false;
      clearInterval(id);
    };
  }, [wsId, load, refreshMs, attempt]);
  return useMemo<Async<T>>(() => {
    if (loaded === null || loaded.wsId !== wsId) return { state: "loading" };
    if ("error" in loaded) return { state: "error", error: loaded.error, retry: () => { setLoaded(null); setAttempt((n) => n + 1); } };
    return { state: "ready", value: loaded.value };
  }, [loaded, wsId]);
}

// ---------------------------------------------------------------- agents-status / agents-summary

const loadStatus = async (wsId: string): Promise<AgentStatusEntry[]> => {
  const data = await api<AgentsStatusResponse>(`/api/v1/workspaces/${wsId}/agents/status`);
  return data?.agents ?? [];
};

/** Every agent of the workspace with status, presence, heartbeat, current task and type. */
export function useAgentsStatus(): Async<AgentStatusEntry[]> {
  return usePolled(loadStatus, AGENTS_POLL_MS);
}

export interface AgentsStatusFilter {
  /** Effective statuses to keep (stale counts as offline); empty or absent = all. */
  statuses?: readonly AgentStatus[];
  onlyWithTask?: boolean;
}

export function filterAgents(agents: readonly AgentStatusEntry[], f: AgentsStatusFilter): AgentStatusEntry[] {
  const keep = f.statuses && f.statuses.length > 0 ? new Set(f.statuses) : null;
  return agents.filter((a) => (!keep || keep.has(agentState(a))) && (!f.onlyWithTask || !!a.current_task_id));
}

export interface AgentsSummary {
  online: number;
  busy: number;
  /** offline and error together, stale included — the Sessions page's third chip. */
  offline: number;
  total: number;
}

export function summarizeAgents(agents: readonly AgentStatusEntry[]): AgentsSummary {
  const s: AgentsSummary = { online: 0, busy: 0, offline: 0, total: agents.length };
  for (const a of agents) {
    const st = agentState(a);
    if (st === "online") s.online++;
    else if (st === "busy") s.busy++;
    else s.offline++;
  }
  return s;
}

export function useAgentsSummary(): Async<AgentsSummary> {
  const r = useAgentsStatus();
  return useMemo<Async<AgentsSummary>>(() => (r.state === "ready" ? { state: "ready", value: summarizeAgents(r.value) } : r), [r]);
}

// ---------------------------------------------------------------- agents-active

export interface ActiveAgent {
  agent: TeamDirectoryAgent;
  status: AgentStatus;
}

/** The same agentState as the summary, the status grid and the Sessions page. */
export function resolveTeamAgentStatus(agent: TeamDirectoryAgent): AgentStatus {
  return agentState(agent);
}

// Only the workspace's own agents, the set GET /agents/status returns to the
// summary, the status grid and Sessions. /team also lists guests (is_home false):
// their one heartbeat is not tied to this workspace, so their Busy may be work
// elsewhere and counted here it broke busy(summary) = Busy(active).
// A server without is_home lists home agents only, so absent counts as home.
const loadTeam = async (wsId: string): Promise<ActiveAgent[]> => {
  const data = await api<TeamDirectory>(`/api/v1/workspaces/${wsId}/team`);
  return (data?.agents ?? [])
    .filter((agent) => agent.is_home !== false)
    .map((agent) => ({ agent, status: resolveTeamAgentStatus(agent) }))
    .filter((s) => s.status !== "offline" && s.status !== "error");
};

export const ACTIVE_LIMIT = { min: 1, max: 50, default: 5 } as const;

export function clampLimit(limit: unknown): number {
  const n = typeof limit === "number" && Number.isFinite(limit) ? Math.round(limit) : ACTIVE_LIMIT.default;
  return Math.min(ACTIVE_LIMIT.max, Math.max(ACTIVE_LIMIT.min, n));
}

/** Agents that are not offline, up to `limit`; `total` is how many are active in all. */
export function useActiveAgents(limit: number): Async<{ items: ActiveAgent[]; total: number }> {
  const r = usePolled(loadTeam, AGENTS_POLL_MS);
  const n = clampLimit(limit);
  return useMemo(
    () => (r.state === "ready" ? { state: "ready", value: { items: r.value.slice(0, n), total: r.value.length } } : r),
    [r, n],
  );
}

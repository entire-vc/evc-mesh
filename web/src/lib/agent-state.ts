// One agent state for every surface that counts or draws agents: the
// agents-summary, agents-active and agents-status widgets and the Sessions page.
//
// Two columns describe an agent and they mean different things:
//   - `status` (agents.status) is presence: POST /agents/heartbeat sets it to
//     online; an agent that never sent its own heartbeat stays offline.
//   - `heartbeat_status` is what the agent said it is doing in that heartbeat
//     (busy, working, idle, ...). Production keeps busy only here: `status`
//     holds online/offline and nothing else.
// Reading only one of them is how the summary counted 0 busy while Active
// agents showed Busy for the same agents.

export type AgentState = "online" | "busy" | "offline" | "error";

export interface AgentStateInput {
  status?: string | null;
  heartbeat_status?: string | null;
  is_stale?: boolean | null;
}

/** heartbeat_status values that mean the agent is doing work. */
const BUSY_HEARTBEATS: ReadonlySet<string> = new Set(["busy", "working"]);

/**
 * Busy = online and the agent's own heartbeat says busy or working; nothing
 * else makes an agent busy (a legacy `busy` row is a live row, read as online).
 * A stale heartbeat or an offline row is Offline whatever the heartbeat said:
 * an offline row's last_heartbeat is moved by any API call made with the
 * agent's key (the runner's poll), so its freshness is not a run.
 */
export function agentState(a: AgentStateInput): AgentState {
  const row = (a.status || "offline").toLowerCase();
  if (a.is_stale || row === "offline") return "offline";
  if (row === "error") return "error";
  const hb = (a.heartbeat_status || "").toLowerCase();
  if (hb === "error") return "error";
  if (BUSY_HEARTBEATS.has(hb)) return "busy";
  return "online";
}

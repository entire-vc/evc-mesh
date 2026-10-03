import { useAgentStore } from "@/stores/agent";
import { useRulesStore } from "@/stores/rules";

/** Scalar selectors keep unrelated heartbeats from re-rendering board cards. */
export function useAgentShortTag(id?: string | null, type?: string) {
  const saved = useAgentStore((s) => type === "agent" ? s.agents.find((a) => a.id === id)?.short_tag : undefined);
  const directory = useRulesStore((s) => type === "agent" ? s.teamDirectory?.agents.find((a) => a.id === id)?.short_tag : undefined);
  return saved === undefined ? directory : saved;
}

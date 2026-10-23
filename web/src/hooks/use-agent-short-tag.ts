import { useMemo } from "react";
import { useAgentStore } from "@/stores/agent";
import { useRulesStore } from "@/stores/rules";

/** Scalar selectors keep unrelated heartbeats from re-rendering board cards. */
export function useAgentShortTag(id?: string | null, type?: string) {
  const saved = useAgentStore((s) => id && type === "agent" ? s.agents.find((a) => a.id === id)?.short_tag : undefined);
  const directory = useRulesStore((s) => id && type === "agent" ? s.teamDirectory?.agents?.find((a) => a.id === id)?.short_tag : undefined);
  return saved === undefined ? directory : saved;
}

/**
 * id -> short tag for every agent that has one, resolved like useAgentShortTag. The selector
 * returns a string, so heartbeats that leave the tags alone do not re-render the caller.
 */
export function useAgentShortTagMap(): ReadonlyMap<string, string | null | undefined> {
  const key = useAgentStore((s) => JSON.stringify(s.agents.map((a) => [a.id, a.short_tag])));
  const dirKey = useRulesStore((s) => JSON.stringify(s.teamDirectory?.agents?.map((a) => [a.id, a.short_tag]) ?? []));
  return useMemo(() => {
    const map = new Map<string, string | null | undefined>();
    for (const [id, tag] of JSON.parse(dirKey) as [string, string | null | undefined][]) map.set(id, tag);
    for (const [id, tag] of JSON.parse(key) as [string, string | null | undefined][]) if (tag !== undefined) map.set(id, tag);
    return map;
  }, [key, dirKey]);
}

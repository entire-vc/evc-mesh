import { useEffect, useMemo } from "react";
import type { MentionEntry } from "@/components/markdown-renderer";
import { useRulesStore } from "@/stores/rules";
import { useWorkspaceStore } from "@/stores/workspace";

/**
 * The slug -> kind map the renderers use to decide what is a mention.
 *
 * Call it ONCE per screen and pass the map down, rather than per rendered
 * comment: fetchTeamDirectory is unconditional, so a hook mounted in a list item
 * would issue one request per comment on screen.
 *
 * Agents AND humans, unlike the task comment list, which builds the same map
 * from agents alone. That asymmetry was visible: a comment reading
 * "@pavel — @daedalus" highlighted the agent and left the person as prose, so
 * the one delivery that definitely happened looked like it had not. It needed
 * the directory to carry a human's username, which it now does.
 *
 * A slug the directory does not know stays plain text. That is the honest
 * rendering of "this addresses nobody" — and since the server refuses an
 * unresolvable mention outright, the only way to get here is a name that
 * stopped resolving after the fact.
 */
export function useMentionDirectory(): {
  mentionables: Map<string, MentionEntry>;
  wsSlug: string | undefined;
} {
  const currentWorkspace = useWorkspaceStore((s) => s.currentWorkspace);
  const teamDirectory = useRulesStore((s) => s.teamDirectory);
  const fetchTeamDirectory = useRulesStore((s) => s.fetchTeamDirectory);

  const workspaceId = currentWorkspace?.id;
  useEffect(() => {
    if (!workspaceId || teamDirectory) return;
    void fetchTeamDirectory(workspaceId);
    // teamDirectory is deliberately not a dependency beyond the null check
    // above: re-running on every directory change would refetch forever.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, fetchTeamDirectory]);

  const mentionables = useMemo(() => {
    const map = new Map<string, MentionEntry>();
    if (!teamDirectory) return map;
    for (const agent of teamDirectory.agents) {
      if (agent.slug) map.set(agent.slug, { kind: "agent" });
    }
    for (const human of teamDirectory.humans) {
      if (human.username) map.set(human.username, { kind: "user" });
    }
    return map;
  }, [teamDirectory]);

  return { mentionables, wsSlug: currentWorkspace?.slug };
}

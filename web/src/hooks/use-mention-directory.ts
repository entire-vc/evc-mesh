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
 * Agents and humans both, unlike the task comment list, which builds the same
 * map from agents alone and therefore leaves a mentioned person as plain text
 * even though the server delivered the notification. Human slugs come from the
 * mentionables endpoint's own source — the username — which the team directory
 * does not carry, so a person is matched on the handle the directory does
 * expose and falls back to plain text when there is none. That is the correct
 * failure: unhighlighted reads as "this is not a mention", which is wrong-but-
 * visible, where a wrong link would be wrong-and-followed.
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
      const slug = (human as { username?: string }).username;
      if (slug) map.set(slug, { kind: "user" });
    }
    return map;
  }, [teamDirectory]);

  return { mentionables, wsSlug: currentWorkspace?.slug };
}

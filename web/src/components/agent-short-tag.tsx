import { useAgentShortTag } from "@/hooks/use-agent-short-tag";

interface AgentShortTagProps {
  tag?: string | null;
  id?: string | null;
  type?: string | null;
}

/** Explicit null/empty tags override the directory; human names stay unchanged. */
export function AgentShortTag({ tag, id, type = "agent" }: AgentShortTagProps) {
  const savedTag = useAgentShortTag(tag === undefined ? id : undefined, type ?? undefined);
  return <AgentShortTagView tag={tag === undefined ? savedTag : tag} type={type} />;
}

/** Pure variant: draws the tag it is given, never touches a store (slot views use it). */
export function AgentShortTagView({ tag, type = "agent" }: Pick<AgentShortTagProps, "tag" | "type">) {
  const resolvedTag = type === "agent" ? tag : null;
  return resolvedTag ? <span className="font-normal text-muted-foreground"> · {resolvedTag}</span> : null;
}

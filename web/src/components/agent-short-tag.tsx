import { useAgentShortTag } from "@/hooks/use-agent-short-tag";

interface AgentShortTagProps {
  tag?: string | null;
  id?: string | null;
  type?: string | null;
}

/** Explicit null/empty tags override the directory; human names stay unchanged. */
export function AgentShortTag({ tag, id, type = "agent" }: AgentShortTagProps) {
  const savedTag = useAgentShortTag(tag === undefined ? id : undefined, type ?? undefined);
  const resolvedTag = type === "agent" ? (tag === undefined ? savedTag : tag) : null;
  return resolvedTag ? <span className="font-normal text-muted-foreground"> · {resolvedTag}</span> : null;
}

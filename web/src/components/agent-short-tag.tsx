export function AgentShortTag({ tag }: { tag?: string | null }) {
  return tag ? <span className="font-normal text-muted-foreground"> · {tag}</span> : null;
}

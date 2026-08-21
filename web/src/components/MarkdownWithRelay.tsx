import { Fragment } from "react";
import { MarkdownView, type MentionEntry } from "@/components/markdown-view";
import { RelayDocCard } from "@/components/RelayDocCard";

interface Segment {
  type: "text" | "relay";
  value: string;
  label?: string;
}

// Split markdown content into text segments and relay:// URL segments.
// Handles both bare relay:// and markdown-link syntax [label](relay://...).
function splitRelaySegments(content: string): Segment[] {
  const segments: Segment[] = [];
  // Match: [label](relay://...) OR bare relay://...
  const re = /\[([^\]]*)\]\(relay:\/\/([^)]+)\)|relay:\/\/[^\s\n"')]+/g;
  let lastIndex = 0;
  let match: RegExpExecArray | null;

  while ((match = re.exec(content)) !== null) {
    if (match.index > lastIndex) {
      segments.push({ type: "text", value: content.slice(lastIndex, match.index) });
    }

    if (match[0].startsWith("[")) {
      // Markdown link: [label](relay://...)
      const label = match[1] ?? "";
      const path = match[2] ?? "";
      segments.push({ type: "relay", value: `relay://${path}`, label: label || undefined });
    } else {
      segments.push({ type: "relay", value: match[0] });
    }

    lastIndex = match.index + match[0].length;
  }

  if (lastIndex < content.length) {
    segments.push({ type: "text", value: content.slice(lastIndex) });
  }

  return segments;
}

interface MarkdownWithRelayProps {
  content: string;
  className?: string;
  mentionables?: Map<string, MentionEntry>;
  wsSlug?: string;
  projId?: string;
}

// Drop-in replacement for MarkdownView that renders relay:// URLs as the
// document itself, opened in our own editor (D10). The splitting is unchanged:
// the links are a pseudo-scheme in free text with no entity behind them, so the
// strings people already wrote keep working and only what is drawn changed.
export function MarkdownWithRelay({
  content,
  className,
  mentionables,
  wsSlug,
  projId,
}: MarkdownWithRelayProps) {
  const segments = splitRelaySegments(content);

  // No relay:// URLs — delegate entirely to MarkdownView for identical output
  if (segments.every((s) => s.type === "text")) {
    return (
      <MarkdownView
        content={content}
        className={className}
        mentionables={mentionables}
        wsSlug={wsSlug}
      />
    );
  }

  return (
    <div className={className}>
      {segments.map((seg, idx) =>
        seg.type === "relay" ? (
          <RelayDocCard key={seg.value} relayUrl={seg.value} label={seg.label} projId={projId} />
        ) : seg.value.trim() ? (
          <MarkdownView
            key={idx}
            content={seg.value}
            mentionables={mentionables}
            wsSlug={wsSlug}
          />
        ) : (
          <Fragment key={idx} />
        ),
      )}
    </div>
  );
}

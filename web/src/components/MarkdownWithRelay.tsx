import { Fragment } from "react";
import { MarkdownRenderer, type MentionEntry } from "@/components/markdown-renderer";
import { RelayPreviewCard } from "@/components/RelayPreviewCard";

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

// Drop-in replacement for MarkdownRenderer that renders relay:// URLs as preview cards.
export function MarkdownWithRelay({
  content,
  className,
  mentionables,
  wsSlug,
  projId,
}: MarkdownWithRelayProps) {
  const segments = splitRelaySegments(content);

  // No relay:// URLs — delegate entirely to MarkdownRenderer for identical output
  if (segments.every((s) => s.type === "text")) {
    return (
      <MarkdownRenderer
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
          <RelayPreviewCard key={seg.value} relayUrl={seg.value} label={seg.label} projId={projId} />
        ) : seg.value.trim() ? (
          <MarkdownRenderer
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

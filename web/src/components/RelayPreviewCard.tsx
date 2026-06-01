import { ExternalLink, FileText } from "lucide-react";

// Extract a human-readable doc name from relay:// URL.
function extractDocName(relayUrl: string): string {
  try {
    const withoutScheme = relayUrl.replace(/^relay:\/\//, "");
    const parts = withoutScheme.split("/");
    const last = parts[parts.length - 1];
    if (!last) return relayUrl;
    // Decode URI, strip extension
    return decodeURIComponent(last).replace(/\.[a-z]+$/i, "");
  } catch {
    return relayUrl;
  }
}

interface RelayPreviewCardProps {
  relayUrl: string;
  // Optional label from markdown link syntax [label](relay://...)
  label?: string;
}

export function RelayPreviewCard({ relayUrl, label }: RelayPreviewCardProps) {
  const docName = label || extractDocName(relayUrl);

  return (
    <div className="my-3 overflow-hidden rounded-lg border border-border bg-card shadow-sm">
      <div className="flex items-center gap-2 px-3 py-2">
        <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="flex-1 truncate text-xs font-medium text-foreground">{docName}</span>
        <a
          href={relayUrl}
          className="flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          title="Open in Obsidian (Team Relay)"
        >
          Open
          <ExternalLink className="h-3 w-3" />
        </a>
      </div>
    </div>
  );
}

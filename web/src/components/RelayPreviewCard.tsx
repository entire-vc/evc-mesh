import { useEffect, useRef, useState } from "react";
import { ExternalLink, FileText, Loader2 } from "lucide-react";

// Timeout before giving up on iframe load (handles silent X-Frame-Options blocks).
const LOAD_TIMEOUT_MS = 6000;

// Resolve relay:// URL to a public HTTP(S) URL for the iframe src.
// Format: relay://<host>/<path> → https://<host>/<path>
// Override VITE_RELAY_PUBLISH_BASE_URL if Publishing TR serves from a different base.
function resolveRelayToIframeSrc(relayUrl: string): string {
  const base = (import.meta.env.VITE_RELAY_PUBLISH_BASE_URL as string | undefined)?.replace(
    /\/$/,
    "",
  );
  if (base) {
    // Strip relay scheme + host, keep path
    const withoutSchemeAndHost = relayUrl.replace(/^relay:\/\/[^/]+/, "");
    return base + withoutSchemeAndHost;
  }
  return relayUrl.replace(/^relay:\/\//, "https://");
}

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
  const [loadState, setLoadState] = useState<"loading" | "loaded" | "failed">("loading");
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const iframeSrc = resolveRelayToIframeSrc(relayUrl);
  const docName = label || extractDocName(relayUrl);

  useEffect(() => {
    setLoadState("loading");
    timeoutRef.current = setTimeout(() => {
      setLoadState((prev) => (prev === "loading" ? "failed" : prev));
    }, LOAD_TIMEOUT_MS);
    return () => {
      if (timeoutRef.current) clearTimeout(timeoutRef.current);
    };
  }, [relayUrl]);

  return (
    <div className="my-3 overflow-hidden rounded-lg border border-border bg-card shadow-sm">
      {/* Card header — always visible, click opens full doc */}
      <div className="flex items-center gap-2 border-b border-border bg-muted/40 px-3 py-2">
        <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="flex-1 truncate text-xs font-medium text-foreground">{docName}</span>
        <a
          href={iframeSrc}
          target="_blank"
          rel="noopener noreferrer"
          className="flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          title="Open in Team Relay"
        >
          Open
          <ExternalLink className="h-3 w-3" />
        </a>
      </div>

      {/* Body */}
      {loadState === "failed" ? (
        // Graceful fallback: plain link, no broken embed
        <div className="flex items-center gap-2 px-3 py-4 text-xs text-muted-foreground">
          <FileText className="h-4 w-4 shrink-0" />
          <a
            href={iframeSrc}
            target="_blank"
            rel="noopener noreferrer"
            className="text-primary underline underline-offset-2 hover:text-primary/80"
          >
            📄 {docName} — Open in Team Relay
          </a>
        </div>
      ) : (
        <div className="relative" style={{ height: "280px" }}>
          {loadState === "loading" && (
            <div className="absolute inset-0 flex items-center justify-center bg-card">
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
            </div>
          )}
          <iframe
            src={iframeSrc}
            title={docName}
            // Minimal sandbox: allow scripts for Publishing TR to render,
            // allow-same-origin is safe here (relay host ≠ mesh host).
            sandbox="allow-scripts allow-same-origin allow-popups allow-forms"
            onLoad={() => {
              if (timeoutRef.current) clearTimeout(timeoutRef.current);
              setLoadState("loaded");
            }}
            onError={() => {
              if (timeoutRef.current) clearTimeout(timeoutRef.current);
              setLoadState("failed");
            }}
            className="h-full w-full border-0"
            style={{ opacity: loadState === "loaded" ? 1 : 0 }}
          />
        </div>
      )}
    </div>
  );
}

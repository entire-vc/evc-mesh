import { useEffect, useRef, useState } from "react";
import { AlertCircle, ExternalLink, FileText } from "lucide-react";

const LOAD_TIMEOUT_MS = 6000;

function extractDocName(relayUrl: string): string {
  try {
    const withoutScheme = relayUrl.replace(/^relay:\/\//, "");
    const parts = withoutScheme.split("/");
    const last = parts[parts.length - 1];
    if (!last) return relayUrl;
    return decodeURIComponent(last).replace(/\.[a-z]+$/i, "");
  } catch {
    return relayUrl;
  }
}

function relayToHttps(relayUrl: string): string {
  return relayUrl.replace(/^relay:\/\//, "https://");
}

interface RelayPreviewCardProps {
  relayUrl: string;
  label?: string;
}

type IframeState = "loading" | "loaded" | "failed";

export function RelayPreviewCard({ relayUrl, label }: RelayPreviewCardProps) {
  const docName = label || extractDocName(relayUrl);
  const httpsUrl = relayToHttps(relayUrl);
  const [iframeState, setIframeState] = useState<IframeState>("loading");
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    timeoutRef.current = setTimeout(() => {
      setIframeState((s) => (s === "loading" ? "failed" : s));
    }, LOAD_TIMEOUT_MS);
    return () => {
      if (timeoutRef.current) clearTimeout(timeoutRef.current);
    };
  }, []);

  function handleLoad() {
    if (timeoutRef.current) clearTimeout(timeoutRef.current);
    setIframeState("loaded");
  }

  function handleError() {
    if (timeoutRef.current) clearTimeout(timeoutRef.current);
    setIframeState("failed");
  }

  return (
    <div className="my-3 overflow-hidden rounded-lg border border-border bg-card shadow-sm">
      <div className="flex items-center gap-2 px-3 py-2">
        <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="flex-1 truncate text-xs font-medium text-foreground">{docName}</span>
        <a
          href={httpsUrl}
          target="_blank"
          rel="noopener noreferrer"
          className="flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          Open
          <ExternalLink className="h-3 w-3" />
        </a>
      </div>

      {iframeState !== "failed" ? (
        <iframe
          src={httpsUrl}
          title={docName}
          sandbox="allow-scripts allow-same-origin"
          className="h-64 w-full border-t border-border"
          onLoad={handleLoad}
          onError={handleError}
        />
      ) : (
        <div className="flex items-center gap-2 border-t border-border px-3 py-2 text-xs text-muted-foreground">
          <AlertCircle className="h-3.5 w-3.5 shrink-0" />
          <span>Preview not available.</span>
          <a
            href={httpsUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="ml-auto flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-[11px] transition-colors hover:text-foreground"
          >
            Open in Team Relay
            <ExternalLink className="h-3 w-3" />
          </a>
        </div>
      )}
    </div>
  );
}

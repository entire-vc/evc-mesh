import { useEffect, useState } from "react";
import { ChevronDown, ChevronRight, ExternalLink, FileText, FolderOpen } from "lucide-react";
import { DocEditor } from "@/components/doc-editor";
import { api } from "@/lib/api";

/**
 * A Team Relay document, opened in our own editor.
 *
 * ## What this replaces, and why
 *
 * Until now a `relay://` link rendered as an `<iframe>` holding Team Relay's own
 * rendered HTML page, `sandbox="allow-scripts allow-same-origin"`, fixed at 256
 * pixels tall, with a six-second timeout after which the reader got "Preview not
 * available" and nothing else. It was a second web page inside ours: its own
 * fonts, its own theme, its own scrollbar, and no way for anything in Docs to
 * see the text.
 *
 * Now the source is fetched — server-side, so the integration key never reaches
 * the browser — and rendered by the same editor every other document in Docs
 * uses. By the time it is on the page it is not a preview of somebody else's
 * page; it is a document.
 *
 * ## What is deliberately unchanged
 *
 * The `relay://` links themselves. There is no entity behind them — they are a
 * pseudo-scheme inside free text in task descriptions and comments, already
 * scattered across production, with no table, no id and no version. So nothing
 * migrates: the same string keeps working, and what changes is only what is
 * drawn when it is met. A migration here would have to rewrite prose that people
 * wrote, to gain nothing a reader can see.
 */

interface RelayDocCardProps {
  relayUrl: string;
  label?: string;
  /** Without it there is no integration to read through, and only the link shows. */
  projId?: string;
}

interface RelayDocument {
  available: boolean;
  name?: string;
  path?: string;
  content?: string;
}

function isFolderUrl(relayUrl: string): boolean {
  const withoutScheme = relayUrl.replace(/^relay:\/\//, "");
  const last = withoutScheme.split("/").pop() ?? "";
  return last !== "" && !/\.[a-z0-9]+$/i.test(last);
}

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

export function RelayDocCard({ relayUrl, label, projId }: RelayDocCardProps) {
  const isFolder = isFolderUrl(relayUrl);
  const docName = label || extractDocName(relayUrl);
  const httpsUrl = relayToHttps(relayUrl);
  const Icon = isFolder ? FolderOpen : FileText;

  const [doc, setDoc] = useState<RelayDocument | null>(null);
  const [expanded, setExpanded] = useState(true);

  useEffect(() => {
    // A folder has no document to open, and no project means no integration to
    // read one through. Neither is an error; both just leave the link.
    if (!projId || isFolder) {
      setDoc({ available: false });
      return;
    }
    let cancelled = false;
    api<RelayDocument>(
      `/api/v1/projects/${projId}/tr/document?relay_url=${encodeURIComponent(relayUrl)}`,
    )
      .then((data) => {
        if (!cancelled) setDoc(data);
      })
      // The endpoint already answers `available: false` for every ordinary
      // "cannot read this" case, so reaching here means the request itself
      // failed. Same outcome for the reader: the link still works.
      .catch(() => {
        if (!cancelled) setDoc({ available: false });
      });
    return () => {
      cancelled = true;
    };
  }, [projId, relayUrl, isFolder]);

  const body = doc?.available ? (doc.content ?? "") : "";
  const hasBody = body.trim() !== "";

  return (
    <div className="my-3 overflow-hidden rounded-lg border border-border bg-card shadow-sm">
      <div className="flex items-center gap-2 px-3 py-2">
        {hasBody ? (
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            aria-expanded={expanded}
            aria-label={expanded ? "Collapse document" : "Expand document"}
            className="shrink-0 text-muted-foreground hover:text-foreground"
          >
            {expanded ? (
              <ChevronDown className="h-3.5 w-3.5" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5" />
            )}
          </button>
        ) : (
          <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        )}
        <span className="flex-1 truncate text-xs font-medium text-foreground">
          {doc?.available && doc.name ? doc.name : docName}
        </span>
        <a
          href={httpsUrl}
          target="_blank"
          rel="noopener noreferrer"
          title={isFolder ? "Open folder in Team Relay" : "Open in Team Relay"}
          className="flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          {isFolder ? "Open folder" : "Open"}
          <ExternalLink className="h-3 w-3" />
        </a>
      </div>

      {hasBody && expanded && (
        // The same editor, read-only, that renders our own documents — which is
        // the whole point of the unit. No fixed height: the document is as long
        // as it is, and the 256px window was the most visible thing wrong with
        // the iframe.
        <div className="max-h-96 overflow-y-auto border-t border-border px-3 py-2">
          <DocEditor value={body} onChange={() => {}} readOnly />
        </div>
      )}
    </div>
  );
}

import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { cn } from "@/lib/cn";
import {
  handleArtifactLinkClick,
  resolveArtifactImages,
} from "@/lib/artifact-links";
import { type MentionEntry, linkMentions } from "@/lib/milkdown/mentions";
import { renderMarkdownFragment } from "@/lib/milkdown/render";
import { applyLinkTargets } from "@/lib/milkdown/view-links";
import "@/components/doc-editor.css";

export type { MentionEntry };

export interface MarkdownViewProps {
  content: string;
  className?: string;
  mentionables?: Map<string, MentionEntry>;
  wsSlug?: string;
}

/**
 * Rendered markdown, for everything that is read rather than edited: task
 * descriptions, comments, activity previews, stored memories.
 *
 * This is the read half of the one markdown implementation in the app. The
 * write half is the editor; both go through the same schema (lib/milkdown), so
 * a task description cannot look like one thing on the page and another in the
 * box you edit it in — which is precisely what the two deleted renderers did to
 * each other, having been written twice and drifted.
 *
 * Three behaviours are the app's rather than the markdown's, and they are
 * applied here, to the rendered tree:
 *
 *  - `@slug` becomes a profile link (lib/milkdown/mentions);
 *  - a link into this app navigates in place instead of opening a tab;
 *  - an internal attachment image gets a fresh presigned URL, because it cannot
 *    be a plain `src` (lib/artifact-links).
 */
export function MarkdownView({
  content,
  className,
  mentionables,
  wsSlug,
}: MarkdownViewProps) {
  const navigate = useNavigate();
  const hostRef = useRef<HTMLDivElement>(null);
  // Drives the "render produced nothing" case. Starts null so the first paint
  // does not flash an empty box before the parse lands.
  const [empty, setEmpty] = useState<boolean | null>(null);

  useEffect(() => {
    let cancelled = false;
    void renderMarkdownFragment(content).then((fragment) => {
      const host = hostRef.current;
      // Two guards, not one: `cancelled` covers a newer render having started,
      // and the host may be gone if the component unmounted while parsing.
      if (cancelled || !host) return;
      linkMentions(fragment, mentionables, wsSlug);
      applyLinkTargets(fragment);
      host.replaceChildren(fragment);
      setEmpty(!host.firstChild);
      void resolveArtifactImages(host);
    });
    return () => {
      cancelled = true;
    };
  }, [content, mentionables, wsSlug]);

  const handleClick = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      // An internal attachment carries no usable href and is opened by
      // resolving a fresh URL — it must be checked before the router branch,
      // which would otherwise navigate the app to an API path.
      if (handleArtifactLinkClick(e)) return;
      const link = (e.target as HTMLElement).closest("a[href]");
      if (!(link instanceof HTMLAnchorElement)) return;
      // A modified click is the reader deliberately asking for a new tab;
      // taking that over would be worse than the bug this avoids.
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
      const href = link.getAttribute("href") ?? "";
      // Root-relative only. A protocol-relative `//host/x` is another origin
      // wearing a leading slash; handing it to the router would render a route
      // that does not exist.
      if (!href.startsWith("/") || href.startsWith("//")) return;
      e.preventDefault();
      void navigate(href);
    },
    [navigate],
  );

  // Matches the deleted renderer's contract: nothing to show means no box at
  // all, so callers can lay out around it without an empty div.
  if (empty === true) return null;

  return (
    <div
      ref={hostRef}
      className={cn("mesh-doc-prose text-sm text-foreground", className)}
      onClick={handleClick}
    />
  );
}

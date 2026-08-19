import { useCallback, useEffect, useMemo, useRef } from "react";
import { useNavigate } from "react-router";
import { cn } from "@/lib/cn";
import {
  handleArtifactLinkClick,
  isArtifactDownloadPath,
  renderArtifactAwareImage,
  renderArtifactAwareLink,
  resolveArtifactImages,
} from "@/lib/artifact-links";

export interface MentionEntry {
  kind: "agent" | "user";
}

interface MarkdownRendererProps {
  content: string;
  className?: string;
  mentionables?: Map<string, MentionEntry>;
  wsSlug?: string;
}

// Escape HTML to prevent XSS in rendered markdown
function escapeHtml(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

// Render inline markdown: bold, italic, code, links, images, @mentions
/**
 * Is this a path into this app rather than somewhere else?
 *
 * Root-relative only: a protocol-relative `//host/x` is another origin wearing a
 * leading slash, and treating it as internal would hand it to the router, which
 * would then render a route that does not exist. A bare `#fragment` is not a
 * route either.
 */
function isInternalRoute(url: string): boolean {
  return url.startsWith("/") && !url.startsWith("//");
}

function renderInline(
  text: string,
  mentionables?: Map<string, MentionEntry>,
  wsSlug?: string,
): string {
  let result = escapeHtml(text);

  // Images: ![alt](url) — alt is already HTML-escaped by the escapeHtml(text) above
  result = result.replace(
    /!\[([^\]]*)\]\(([^)]+)\)/g,
    (_match, alt: string, url: string) => renderArtifactAwareImage(alt, url),
  );

  // Links: [text](url) — only allow safe protocols, except our own internal
  // artifact links which route through click-time resolution instead of href
  result = result.replace(
    /\[([^\]]+)\]\(([^)]+)\)/g,
    (_match, linkText: string, url: string) => {
      if (isArtifactDownloadPath(url)) {
        return renderArtifactAwareLink(linkText, url);
      }
      const decoded = url.replace(/&amp;/g, "&");
      const isSafe = /^(https?:\/\/|\/|#|mailto:)/i.test(decoded);
      if (!isSafe) return `${linkText} (${url})`;
      // A link to a route of this app is not an external link and must not be
      // treated as one. Rendered with target="_blank" it leaves the app, full
      // page load and all, in a tab the reader did not ask for — measured, and
      // the reason a document link inserted into a task description could not
      // simply be a link until now. Marked for the click handler below, which
      // navigates in place.
      if (isInternalRoute(decoded)) {
        return `<a href="${url}" class="internal-link text-primary underline underline-offset-2 hover:text-primary/80">${linkText}</a>`;
      }
      return `<a href="${url}" target="_blank" rel="noopener noreferrer" class="text-primary underline underline-offset-2 hover:text-primary/80">${linkText}</a>`;
    },
  );

  // Bold: **text** or __text__
  result = result.replace(
    /\*\*([^*]+)\*\*|__([^_]+)__/g,
    (_match, g1: string | undefined, g2: string | undefined) =>
      `<strong>${g1 ?? g2 ?? ""}</strong>`,
  );

  // Italic: *text* or _text_
  result = result.replace(
    /\*([^*]+)\*|_([^_]+)_/g,
    (_match, g1: string | undefined, g2: string | undefined) =>
      `<em>${g1 ?? g2 ?? ""}</em>`,
  );

  // Inline code: protect with placeholders so @mentions inside backticks are skipped
  const codeMap: string[] = [];
  result = result.replace(/`([^`]+)`/g, (_match, code: string) => {
    const placeholder = `\x00CODE${codeMap.length}\x00`;
    codeMap.push(
      `<code class="rounded bg-muted px-1 py-0.5 font-mono text-[0.85em]">${code}</code>`,
    );
    return placeholder;
  });

  // @mention: slug boundary lookahead/behind, lowercase, length 3-42
  if (mentionables && wsSlug) {
    result = result.replace(
      /(?<![a-zA-Z0-9_])@([a-z0-9][a-z0-9-]{1,40}[a-z0-9]?)(?![a-z0-9-])/g,
      (_match, slug: string) => {
        const entry = mentionables.get(slug);
        if (!entry) return `@${slug}`;
        const href = `/w/${wsSlug}/team/${entry.kind}/${slug}`;
        return `<a class="mention-link inline-block whitespace-nowrap rounded px-1 py-0.5 text-blue-600 bg-blue-50 hover:underline dark:text-blue-400 dark:bg-blue-900/30" href="${href}" data-mention-slug="${slug}" aria-label="Open team profile for @${slug}">@${slug}</a>`;
      },
    );
  }

  // Restore code placeholders
  result = result.replace(/\x00CODE(\d+)\x00/g, (_match, idx: string) => {
    return codeMap[parseInt(idx, 10)] ?? "";
  });

  return result;
}

function renderMarkdown(
  raw: string,
  mentionables?: Map<string, MentionEntry>,
  wsSlug?: string,
): string {
  if (!raw.trim()) return "";

  const lines = raw.split("\n");
  const html: string[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];
    if (line === undefined) {
      i++;
      continue;
    }

    // Fenced code block: ``` — @mentions inside are NOT processed (uses escapeHtml directly)
    if (line.trimStart().startsWith("```")) {
      const fence = "```";
      const lang = line.trim().slice(3).trim();
      const codeLines: string[] = [];
      i++;
      while (i < lines.length) {
        const codeLine = lines[i];
        if (codeLine === undefined || codeLine.trimStart().startsWith(fence)) break;
        codeLines.push(escapeHtml(codeLine));
        i++;
      }
      html.push(
        `<pre class="overflow-x-auto rounded-lg bg-muted px-4 py-3 font-mono text-sm my-2"${
          lang ? ` data-lang="${escapeHtml(lang)}"` : ""
        }><code>${codeLines.join("\n")}</code></pre>`,
      );
      i++; // skip closing ```
      continue;
    }

    // Heading: # ## ###
    const headingMatch = /^(#{1,6})\s+(.+)$/.exec(line);
    if (headingMatch) {
      const level = headingMatch[1]?.length ?? 1;
      const headingText = renderInline(headingMatch[2] ?? "", mentionables, wsSlug);
      const sizeClasses: Record<number, string> = {
        1: "text-2xl font-bold mt-4 mb-2",
        2: "text-xl font-semibold mt-3 mb-2",
        3: "text-lg font-semibold mt-2 mb-1",
        4: "text-base font-semibold mt-2 mb-1",
        5: "text-sm font-semibold mt-1 mb-0.5",
        6: "text-xs font-semibold mt-1 mb-0.5",
      };
      html.push(
        `<h${level} class="${sizeClasses[level] ?? "font-semibold"}">${headingText}</h${level}>`,
      );
      i++;
      continue;
    }

    // Horizontal rule: --- or ***
    if (/^(-{3,}|\*{3,})$/.test(line.trim())) {
      html.push(`<hr class="my-4 border-border" />`);
      i++;
      continue;
    }

    // Unordered list: - item or * item
    if (/^[\s]*[-*]\s+/.test(line)) {
      const listItems: string[] = [];
      while (i < lines.length) {
        const listLine = lines[i];
        if (!listLine || !/^[\s]*[-*]\s+/.test(listLine)) break;
        const itemText = listLine.replace(/^[\s]*[-*]\s+/, "");
        listItems.push(
          `<li class="ml-4 list-disc">${renderInline(itemText, mentionables, wsSlug)}</li>`,
        );
        i++;
      }
      html.push(`<ul class="my-1 space-y-0.5">${listItems.join("")}</ul>`);
      continue;
    }

    // Ordered list: 1. item
    if (/^[\s]*\d+\.\s+/.test(line)) {
      const listItems: string[] = [];
      while (i < lines.length) {
        const listLine = lines[i];
        if (!listLine || !/^[\s]*\d+\.\s+/.test(listLine)) break;
        const itemText = listLine.replace(/^[\s]*\d+\.\s+/, "");
        listItems.push(
          `<li class="ml-4 list-decimal">${renderInline(itemText, mentionables, wsSlug)}</li>`,
        );
        i++;
      }
      html.push(`<ol class="my-1 space-y-0.5">${listItems.join("")}</ol>`);
      continue;
    }

    // Blockquote: > text
    if (line.startsWith("> ")) {
      const quoteLines: string[] = [];
      while (i < lines.length) {
        const quoteLine = lines[i];
        if (!quoteLine?.startsWith("> ")) break;
        quoteLines.push(renderInline(quoteLine.slice(2), mentionables, wsSlug));
        i++;
      }
      html.push(
        `<blockquote class="my-2 border-l-4 border-border pl-4 text-muted-foreground italic">${quoteLines.join("<br />")}</blockquote>`,
      );
      continue;
    }

    // Empty line: paragraph break
    if (line.trim() === "") {
      html.push(`<div class="h-2"></div>`);
      i++;
      continue;
    }

    // Paragraph
    html.push(`<p class="leading-relaxed">${renderInline(line, mentionables, wsSlug)}</p>`);
    i++;
  }

  return html.join("");
}

export function MarkdownRenderer({
  content,
  className,
  mentionables,
  wsSlug,
}: MarkdownRendererProps) {
  const navigate = useNavigate();
  const containerRef = useRef<HTMLDivElement>(null);

  const html = useMemo(
    () => renderMarkdown(content, mentionables, wsSlug),
    [content, mentionables, wsSlug],
  );

  // Resolve internal artifact images to a fresh presigned URL after each
  // render — the src can't be a plain URL (see artifact-links.ts).
  useEffect(() => {
    if (containerRef.current) {
      void resolveArtifactImages(containerRef.current);
    }
  }, [html]);

  // Intercept mention-link clicks (SPA navigation) and internal artifact-link
  // clicks (fetch a fresh presigned URL instead of following a dead href).
  const handleClick = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      if (handleArtifactLinkClick(e)) return;
      const target = e.target as HTMLElement;
      // Mentions and in-app routes both navigate in place. They are one branch
      // because they are one behaviour — the class names differ only so the
      // markup says which kind of link it is.
      const link = target.closest("a.mention-link, a.internal-link");
      if (link instanceof HTMLAnchorElement) {
        // A modified click is the reader deliberately asking for a new tab or
        // window; taking that over would be worse than the bug this fixes.
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
        e.preventDefault();
        void navigate(link.getAttribute("href") ?? "/");
      }
    },
    [navigate],
  );

  if (!html) {
    return null;
  }

  return (
    <div
      ref={containerRef}
      className={cn("text-sm text-foreground", className)}
      onClick={handleClick}
      // Safe: we control the sanitization in renderMarkdown via escapeHtml
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}

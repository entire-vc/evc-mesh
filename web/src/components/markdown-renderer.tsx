import { useMemo } from "react";
import { cn } from "@/lib/cn";

interface MarkdownRendererProps {
  content: string;
  className?: string;
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

// Render inline markdown: bold, italic, code, links, images
function renderInline(text: string): string {
  let result = escapeHtml(text);

  // Images: ![alt](url) — alt and url are already HTML-escaped from escapeHtml above
  result = result.replace(
    /!\[([^\]]*)\]\(([^)]+)\)/g,
    (_match, alt: string, url: string) => {
      // Restore & in URL so src works correctly (escapeHtml turned & into &amp;)
      const srcUrl = url.replace(/&amp;/g, "&");
      return `<img src="${srcUrl}" alt="${alt}" style="max-width:100%;border-radius:4px;" />`;
    },
  );

  // Links: [text](url) — only allow safe protocols
  result = result.replace(
    /\[([^\]]+)\]\(([^)]+)\)/g,
    (_match, linkText: string, url: string) => {
      const decoded = url.replace(/&amp;/g, "&");
      const isSafe = /^(https?:\/\/|\/|#|mailto:)/i.test(decoded);
      if (!isSafe) return `${linkText} (${url})`;
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

  // Inline code: `code`
  result = result.replace(
    /`([^`]+)`/g,
    (_match, code: string) =>
      `<code class="rounded bg-muted px-1 py-0.5 font-mono text-[0.85em]">${code}</code>`,
  );

  return result;
}

function renderMarkdown(raw: string): string {
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

    // Fenced code block: ```
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
      const headingText = renderInline(headingMatch[2] ?? "");
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
          `<li class="ml-4 list-disc">${renderInline(itemText)}</li>`,
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
          `<li class="ml-4 list-decimal">${renderInline(itemText)}</li>`,
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
        quoteLines.push(renderInline(quoteLine.slice(2)));
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
    html.push(`<p class="leading-relaxed">${renderInline(line)}</p>`);
    i++;
  }

  return html.join("");
}

export function MarkdownRenderer({ content, className }: MarkdownRendererProps) {
  const html = useMemo(() => renderMarkdown(content), [content]);

  if (!html) {
    return null;
  }

  return (
    <div
      className={cn("text-sm text-foreground", className)}
      // Safe: we control the sanitization in renderMarkdown via escapeHtml
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}

/**
 * YAML frontmatter at the top of a document body — found, shown, never changed.
 *
 * Every document the team writes opens with a `---` … `---` block of metadata
 * (created, updated, author, status, tags …). Handed to the markdown renderer
 * as-is, the closing `---` under a run of text is a setext heading underline, so
 * the whole block collapsed into one bold H2 at the top of the page.
 *
 * The block is therefore split off before the body reaches the editor and glued
 * back, byte for byte, on the way out: `raw + body === source` always holds, so
 * the editor can never re-serialise it.
 * Nothing here rewrites the frontmatter — it stays in the markdown, which is
 * where exports, `get_doc` and sync expect to find it.
 */

export interface FrontmatterEntry {
  key: string;
  /** A scalar as written (quotes removed), or the items of a YAML list. */
  value: string | string[];
}

export interface SplitDocument {
  /**
   * The frontmatter exactly as written, both fences and the blank lines after
   * the closing one included. Empty when the document has none.
   */
  raw: string;
  /** Everything after `raw`. */
  body: string;
  /** The parsed key/value pairs, in source order. Empty when there are none. */
  entries: FrontmatterEntry[];
  /** The lines between the fences, for when `entries` cannot represent them. */
  inner: string;
}

const OPEN = /^---[ \t]*\r?$/;
const CLOSE = /^(---|\.\.\.)[ \t]*\r?$/;

/**
 * Split a document into its frontmatter and its body.
 *
 * Only a `---` on the very first line opens a block — a `---` anywhere else is
 * a thematic break, as it always was. An opening fence that is never closed is
 * not frontmatter either: the document renders exactly as before.
 */
export function splitFrontmatter(source: string): SplitDocument {
  const none: SplitDocument = { raw: "", body: source, entries: [], inner: "" };
  // A byte-order mark in front of the fence still means the fence is line one.
  const bom = source.startsWith("﻿") ? 1 : 0;
  const lines = source.slice(bom).split("\n");
  if (lines.length < 2 || !OPEN.test(lines[0] ?? "")) return none;

  let close = -1;
  for (let i = 1; i < lines.length; i++) {
    if (CLOSE.test(lines[i] ?? "")) {
      close = i;
      break;
    }
  }
  if (close === -1) return none;

  // Blank lines after the closing fence belong to the block: the editor drops
  // leading blank lines when it serialises the body, so leaving them in `body`
  // would rewrite the spacing under the frontmatter on every save.
  let end = close + 1;
  while (end < lines.length - 1 && (lines[end] ?? "").trim() === "") end++;

  const rawLines = lines.slice(0, end);
  // Rebuild the exact prefix: every line we consumed plus the "\n" after it.
  const rawLength = bom + rawLines.reduce((n, l) => n + l.length + 1, 0);
  const raw = source.slice(0, Math.min(rawLength, source.length));
  const inner = lines.slice(1, close).join("\n");

  return {
    raw,
    body: source.slice(raw.length),
    entries: parseEntries(inner),
    inner,
  };
}

function unquote(value: string): string {
  const v = value.trim();
  if (v.length >= 2) {
    const q = v[0];
    if ((q === '"' || q === "'") && v[v.length - 1] === q) return v.slice(1, -1);
  }
  return v;
}

/** `[a, "b c", d]` → `["a", "b c", "d"]`. Null when it is not a flow list. */
function flowList(value: string): string[] | null {
  const v = value.trim();
  if (!v.startsWith("[") || !v.endsWith("]")) return null;
  const inside = v.slice(1, -1).trim();
  if (!inside) return [];
  return inside.split(",").map(unquote).filter((s) => s !== "");
}

const KEY_LINE = /^([^\s#\-][^:]*?):(?:\s+(.*)|\s*)$/;
const ITEM_LINE = /^\s+-\s+(.*)$|^-\s+(.*)$/;

/**
 * Read the flat subset of YAML that document metadata is written in: `key:
 * value`, `key: [a, b]` and a key followed by `- item` lines. Anything deeper
 * (nested maps, multi-line strings) is kept as text on the key it belongs to
 * rather than guessed at — this is a display, not a YAML implementation.
 */
export function parseEntries(inner: string): FrontmatterEntry[] {
  const entries: FrontmatterEntry[] = [];
  let current: { key: string; scalar: string; items: string[]; extra: string[] } | null =
    null;

  const flush = () => {
    if (!current) return;
    const { key, scalar, items, extra } = current;
    let value: string | string[];
    if (items.length && !scalar) value = items;
    else {
      const list = flowList(scalar);
      value = list ?? [unquote(scalar), ...extra].filter((s) => s !== "").join(" ");
    }
    entries.push({ key, value });
    current = null;
  };

  for (const line of inner.split("\n")) {
    const text = line.replace(/\r$/, "");
    if (!text.trim() || text.trimStart().startsWith("#")) continue;

    const key = KEY_LINE.exec(text);
    if (key) {
      flush();
      current = { key: (key[1] ?? "").trim(), scalar: key[2] ?? "", items: [], extra: [] };
      continue;
    }
    if (!current) continue;
    const item = ITEM_LINE.exec(text);
    if (item && !current.scalar) current.items.push(unquote(item[1] ?? item[2] ?? ""));
    else current.extra.push(text.trim());
  }
  flush();
  return entries;
}

/** Re-attach the frontmatter to an edited body. */
export function joinFrontmatter(raw: string, body: string): string {
  return raw + body;
}

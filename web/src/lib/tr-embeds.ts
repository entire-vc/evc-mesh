import { documentAttachmentDownloadPath } from "@/lib/artifact-links";
import type { DocumentAttachment } from "@/lib/document-attachments";

/**
 * Serve-time rewrite of a Team Relay document body's image references.
 *
 * A mounted copy's markdown keeps its share-relative references (`![[pic.png]]`,
 * `![alt](pic.png)`) exactly as the source wrote them — the stored body is
 * pushed back to the original on every edit (WriteBack), so anything baked
 * into it would corrupt the source on the next save. Instead, the images the
 * mount sync imported as attachments are listed here, and only the value
 * handed to the READ-ONLY renderer is rewritten to the attachment download
 * path. Never pass the result through setDraft.
 *
 * Pure on purpose: no fetches, no state — everything it needs arrives as
 * arguments, so it is testable without a Mesh instance and cheap enough to
 * run on every render.
 */

/**
 * Team Relay's own image extension list (their renderer's rule, copied the
 * same way the Go side copied it) plus pdf — the file types the mount sync
 * imports. Everything else referenced by an embed stays untouched.
 */
const EMBEDDABLE_EXTENSIONS = new Set([
  "png", "jpg", "jpeg", "gif", "svg", "webp", "bmp", "ico", "pdf",
]);

// `![[target]]` / `![[target|size]]` — Team Relay's wiki-embed syntax. The
// target is split on the FIRST pipe (a size suffix), mirroring the Go parser
// (internal/integration/teamrelay/embed.go) so both sides extract the same
// path from the same bytes.
const WIKI_EMBED_RE = /!\[\[([^\]]+)\]\]/g;

// `![alt](target)` — standard markdown image syntax, bare or angle-bracketed
// target. A title after the target is tolerated. The extension-less alt may
// be empty.
const MD_IMAGE_RE = /!\[([^\]]*)\]\((?:<([^>]*)>|([^)\s]+))[^)]*\)/g;

function isEmbeddable(target: string): boolean {
  const dot = target.lastIndexOf(".");
  if (dot < 0) return false;
  return EMBEDDABLE_EXTENSIONS.has(target.slice(dot + 1).toLowerCase());
}

/**
 * True for targets that plausibly name a file ON the share: no scheme, not
 * rooted, not a fragment. Mirrors isLocalSharePath on the Go side — the two
 * must agree, or one side rewrites a reference the other never imported.
 */
function isLocalSharePath(target: string): boolean {
  if (!target || target.startsWith("/") || target.startsWith("#")) return false;
  const colon = target.indexOf(":");
  if (colon > 0 && !target.slice(0, colon).includes("/")) return false;
  return true;
}

function cleanSharePath(target: string): string {
  const trimmed = target.replace(/^\/+/, "");
  const segments: string[] = [];
  for (const seg of trimmed.split("/")) {
    if (seg === "" || seg === ".") continue;
    if (seg === "..") {
      segments.pop();
      continue;
    }
    segments.push(seg);
  }
  return segments.join("/");
}

/**
 * The share paths one reference may name, in the same resolution order the
 * mount sync used when it imported attachments: a target carrying a directory
 * is share-relative as written; explicit ./ and ../ paths resolve against
 * the document first, with a root fallback. A bare name is tried next to the referencing
 * document first, then at the share root. The attachment set only contains
 * the path that actually resolved, so the first candidate with an attachment
 * is the one the importer chose.
 */
function candidates(target: string, docSourcePath: string | null): string[] {
  const cleaned = cleanSharePath(target);
  if (!cleaned) return [];
  const docDir = docSourcePath?.includes("/")
    ? docSourcePath.slice(0, docSourcePath.lastIndexOf("/"))
    : "";
  if (target.startsWith("./") || target.startsWith("../")) {
    const relative = cleanSharePath(`${docDir}/${target}`);
    if (relative !== cleaned) return [relative, cleaned];
  }
  if (target.includes("/")) return [cleaned];
  if (!docDir) return [cleaned];
  return [`${docDir}/${cleaned}`, cleaned];
}

function basename(p: string): string {
  return p.slice(p.lastIndexOf("/") + 1);
}

function isPdf(target: string): boolean {
  return target.toLowerCase().endsWith(".pdf");
}

/**
 * Rewrite every resolvable image/pdf reference in body to its attachment's
 * download path. Images become `![name](path)` markdown images; a pdf becomes
 * a plain `[name](path)` link — browsers cannot inline a pdf in an <img>, and
 * a link that downloads on click is strictly more useful than a broken image
 * box. References with no matching attachment are left exactly as written: a
 * dead embed on the source side stays visibly dead rather than being silently
 * hidden.
 */
export function applyTrEmbedAttachments(
  body: string,
  docSourcePath: string | null,
  attachments: DocumentAttachment[],
): string {
  if (!body || attachments.length === 0) return body;

  const bySource = new Map<string, DocumentAttachment>();
  for (const att of attachments) {
    if (att.source_path) bySource.set(att.source_path, att);
  }
  if (bySource.size === 0) return body;

  const resolve = (target: string): DocumentAttachment | undefined => {
    for (const cand of candidates(target, docSourcePath)) {
      const att = bySource.get(cand);
      if (att) return att;
    }
    return undefined;
  };

  const link = (att: DocumentAttachment, alt: string, asImage: boolean) =>
    asImage
      ? `![${alt}](${documentAttachmentDownloadPath(att.id)})`
      : `[${alt}](${documentAttachmentDownloadPath(att.id)})`;

  let out = body.replace(WIKI_EMBED_RE, (_full, inner: string) => {
    const [rawTarget] = inner.split("|");
    const target = (rawTarget ?? "").trim();
    if (!isEmbeddable(target) || !isLocalSharePath(target)) return _full;
    const att = resolve(target);
    if (!att) return _full;
    return link(att, basename(target), !isPdf(target));
  });

  out = out.replace(MD_IMAGE_RE, (full: string, alt: string, angled: string | undefined, bare: string | undefined) => {
    const target = (angled ?? bare ?? "").trim();
    if (!target || !isEmbeddable(target) || !isLocalSharePath(target)) return full;
    const att = resolve(target);
    if (!att) return full;
    return link(att, alt, !isPdf(target));
  });

  return out;
}

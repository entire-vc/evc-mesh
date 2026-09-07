/**
 * toPreviewText — flatten markdown to plain text for a two-line list preview.
 *
 * The notification bell renders `comment_body` verbatim, so a comment that
 * opens with a rule and a bold marker arrives on screen as literally
 *
 *     --- ❓ **Blocking @pavel**: нужен один из двух вариантов, чтобы...
 *
 * In a clamped two-line preview that markup is pure cost: `---` renders as a
 * rule nowhere and occupies a whole line, and every `**` doubles as noise while
 * conveying an emphasis the preview cannot show anyway.
 *
 * This deliberately does NOT render markdown. Rendering would mean bold text,
 * headings and links inside an 80px-wide row — more visual weight, not less.
 * The goal is the sentence, in one flat run of text.
 *
 * Kept conservative on purpose: it strips the markers that actually show up in
 * our comment bodies and leaves anything ambiguous alone. A preview that drops
 * a character it should have kept is worse than one that keeps a stray symbol,
 * because the reader cannot tell the difference — so when in doubt, keep.
 */
export function toPreviewText(md: string): string {
  let out = md;

  // Code first, and PARKED rather than unwrapped in place.
  //
  // The obvious version — strip the fences, then run the link and emphasis
  // rules over everything — corrupts code, because code is full of text that
  // looks like markup. Caught on review: a fenced block containing
  // `arr[0](x)` came out as `arr0`, the link rule having read `[0](x)` as a
  // link. That is the one failure this helper must not have: it promises to
  // keep the code's text, and silently dropped characters instead.
  //
  // So code is lifted out behind placeholders, the markup rules run on what is
  // left, and the code is put back verbatim at the end. The placeholder uses a
  // private-use codepoint so nothing in a real comment can collide with it.
  const parked: string[] = [];
  const park = (text: string) => {
    parked.push(text);
    return `\uE000${parked.length - 1}\uE001`;
  };

  // Fenced blocks: keep the body, drop the fence line (and its language tag).
  out = out.replace(/```[^\n]*\n?([\s\S]*?)```/g, (_m, body) => park(body));
  // An unterminated fence — the preview is a truncated body often enough that
  // this is the common case, not the exotic one.
  out = out.replace(/```[^\n]*\n?([\s\S]*)$/g, (_m, body) => park(body));
  // Inline code.
  out = out.replace(/`([^`\n]+)`/g, (_m, body) => park(body));

  // Images before links — an image is `![alt](url)` and the link rule below
  // would otherwise leave a stray leading `!`.
  out = out.replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1");
  // Links: keep the label, drop the target. The URL is never useful here and is
  // frequently longer than the whole preview.
  out = out.replace(/\[([^\]]*)\]\([^)]*\)/g, "$1");

  // Thematic breaks on their own line — these are what produced the leading
  // `---` in the reported screenshot.
  out = out.replace(/^\s*([-*_])\1{2,}\s*$/gm, " ");

  // Leading block markers: heading hashes, blockquote carets, list bullets.
  // Anchored to line starts so a mid-sentence hyphen or hash survives.
  out = out.replace(/^\s{0,3}#{1,6}\s+/gm, "");
  out = out.replace(/^\s{0,3}>\s?/gm, "");
  out = out.replace(/^\s{0,3}[-*+]\s+/gm, "");
  out = out.replace(/^\s{0,3}\d+[.)]\s+/gm, "");

  // Emphasis. Requires non-space immediately inside the markers so that a bare
  // `**` or an arithmetic `2 * 3` is left alone.
  out = out.replace(/(\*\*|__)(?=\S)([\s\S]*?\S)\1/g, "$2");
  out = out.replace(/(\*|_)(?=\S)([^*_\n]*?\S)\1/g, "$2");

  // Put the code back, verbatim.
  out = out.replace(/\uE000(\d+)\uE001/g, (_m, i) => parked[Number(i)] ?? "");

  // Collapse all whitespace — newlines included, since the preview is one run
  // of text that the CSS clamps, not a place where line breaks mean anything.
  return out.replace(/\s+/g, " ").trim();
}

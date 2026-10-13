package teamrelay

import (
	"regexp"
	"strings"
)

// embedTokenRe matches Team Relay's `![[target|size]]` embed syntax — the same
// pattern apps/web-publish/src/lib/markdown.ts tokenizes it with
// (`^!\[\[([^\]]+)\]\]`, applied per-position in their tokenizer; here applied
// globally to find every occurrence in a body). It is the same regex on
// purpose, not an approximation: a target that reaches this parser through a
// differently-shaped match is a target that resolves to a different file — or
// to nothing — on Team Relay's side than on ours.
//
// A bare `[[...]]` (no leading `!`) is a wikilink, not an embed, and is not
// matched here. Team Relay's own renderer emits a dead link
// (`data-wikilink-disabled`) for that form on real documents — copying its
// resolution rule would ship working-looking links that don't work on either
// side. Parsing `[[...]]` targets is left to a later task; this client does
// not resolve them.
var embedTokenRe = regexp.MustCompile(`!\[\[([^\]]+)\]\]`)

// EmbedTarget is one `![[...]]` token, parsed exactly as Team Relay's
// tokenizer parses it.
type EmbedTarget struct {
	// Target is the file path as written inside the brackets, trimmed but
	// otherwise untouched: no path.Clean, no case-folding, no extension
	// guessing, no slash collapsing. Any of those would resolve to the same
	// file as Team Relay on paths that don't need it and a DIFFERENT file — or
	// none — on the ones that do.
	Target string
	// Size is the text after the first `|`, trimmed, or "" if there was no `|`.
	// A target that itself contains `|` keeps every pipe after the first as
	// part of Size, not Target — this is a straight substring split on the
	// FIRST byte '|', not a delimiter-based field split.
	Size string
}

// FindEmbedTargets scans a document body for every `![[...]]` occurrence and
// parses each one, in the order they appear.
func FindEmbedTargets(body string) []EmbedTarget {
	matches := embedTokenRe.FindAllStringSubmatch(body, -1)
	out := make([]EmbedTarget, 0, len(matches))
	for _, m := range matches {
		out = append(out, ParseEmbedTarget(m[1]))
	}
	return out
}

// ParseEmbedTarget parses the inner content of one `![[...]]` token — what
// embedTokenRe's capture group holds, without the surrounding `![[`/`]]`.
func ParseEmbedTarget(inner string) EmbedTarget {
	if idx := strings.IndexByte(inner, '|'); idx >= 0 {
		return EmbedTarget{
			Target: strings.TrimSpace(inner[:idx]),
			Size:   strings.TrimSpace(inner[idx+1:]),
		}
	}
	return EmbedTarget{Target: strings.TrimSpace(inner)}
}

// imageExtensions is Team Relay's own list (apps/web-publish/src/lib/markdown.ts),
// copied verbatim — video/audio extensions render as a placeholder on their
// side, not a player, and everything else is treated as a note-embed, not an
// attachment fetch.
var imageExtensions = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true,
	"svg": true, "webp": true, "bmp": true, "ico": true,
}

// IsImageEmbed reports whether target's extension is one Team Relay's
// renderer treats as an image, vs. a note-embed or a video/audio placeholder.
// The extension is lowercased before the lookup — their renderer does
// `target.split('.').pop().toLowerCase()`, so `photo.PNG` is an image embed
// exactly as `photo.png` is.
func IsImageEmbed(target string) bool {
	idx := strings.LastIndexByte(target, '.')
	if idx < 0 {
		return false
	}
	return imageExtensions[strings.ToLower(target[idx+1:])]
}

// markdownImageRe matches standard markdown image syntax, `![alt](target)`,
// in the two CommonMove spellings a target may take: a bare `path` with no
// spaces, or `<a path with spaces>` in angle brackets (a title after the path
// — `![alt](pic.png "Logo")` — matches too, because the bare-path group stops
// at the whitespace before it).
//
// Team Relay's own renderer understands this form alongside `![[...]]`, and
// real shares mix the two, so an importer that read only the wiki form would
// silently miss every image a non-Obsidian editor embedded.
var markdownImageRe = regexp.MustCompile(`!\[[^\]]*\]\((?:<([^>]*)>|([^)\s]+))[^)]*\)`)

// embeddableExtensions is imageExtensions plus pdf, minus svg: the file types
// the Mesh embed importer will copy into attachment storage. Everything else
// referenced by an embed stays unreferenced by us — a video the relay only
// placeholders anyway, or a note-embed whose target is another document, not
// a binary. SVG renders as an image on their side but is deliberately not ours
// to store: it is XML that can carry script, so serving it from the Mesh origin
// is an XSS vector, and byte sniffing reports it as text/xml, never image/*.
var embeddableExtensions = func() map[string]bool {
	m := make(map[string]bool, len(imageExtensions)+1)
	for k, v := range imageExtensions {
		m[k] = v
	}
	delete(m, "svg")
	m["pdf"] = true
	return m
}()

// IsEmbeddableFile is IsImageEmbed widened by pdf: whether target names a file
// the embed importer may copy. Kept beside IsImageEmbed rather than folded into
// it because the two answer different questions — "renders as an image there"
// (their renderer's rule) vs. "we are willing to store and serve it" (ours).
func IsEmbeddableFile(target string) bool {
	idx := strings.LastIndexByte(target, '.')
	if idx < 0 {
		return false
	}
	return embeddableExtensions[strings.ToLower(target[idx+1:])]
}

// ImageRef is one image-bearing reference in a body, from either syntax:
// `![[pic.png]]` (Wiki, with Target as written between the brackets) or
// `![alt](pic.png)` (Alt carrying the alt text, "" when absent).
type ImageRef struct {
	Target string
	Alt    string
	Wiki   bool
}

// FindImageRefs scans a body for every reference to an embeddable file, from
// both syntaxes, in order of appearance. References that are not local share
// paths — http(s) URLs, mailto:, `#` anchors, absolute `/rooted` paths — are
// skipped: they point outside the share, so there is nothing on the share's
// files-index to resolve them against. Targets without an embeddable extension
// are skipped the same way (IsEmbeddableFile is the single rule for that).
func FindImageRefs(body string) []ImageRef {
	out := make([]ImageRef, 0, 4)
	for _, t := range FindEmbedTargets(body) {
		if !IsEmbeddableFile(t.Target) {
			continue
		}
		if !isLocalSharePath(t.Target) {
			continue
		}
		out = append(out, ImageRef{Target: t.Target, Wiki: true})
	}
	for _, m := range markdownImageRe.FindAllStringSubmatch(body, -1) {
		target := m[1]
		if target == "" {
			target = m[2]
		}
		target = strings.TrimSpace(target)
		if target == "" || !IsEmbeddableFile(target) || !isLocalSharePath(target) {
			continue
		}
		out = append(out, ImageRef{Target: target, Alt: markdownImageAlt(m[0])})
	}
	return out
}

// isLocalSharePath reports whether target plausibly names a file ON THE SHARE:
// no scheme (`http:`, `mailto:`, `obsidian:`), not rooted (`/etc/passwd`), not
// a fragment. The files-index keys are share-relative paths, which is the only
// shape a candidate resolution can look up.
func isLocalSharePath(target string) bool {
	if target == "" || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "#") {
		return false
	}
	if i := strings.Index(target, ":"); i > 0 {
		// A colon before any slash reads as a scheme (`https:`, `data:`). A
		// colon AFTER a slash is just a Windows-ish or titled filename
		// ("Notes/draft:v2.png") — not a scheme.
		if !strings.Contains(target[:i], "/") {
			return false
		}
	}
	return true
}

// markdownImageAlt pulls the alt text out of one full `![alt](target)` match.
// The regex already guaranteed the shape; this only trims the `![`/](` hull.
func markdownImageAlt(full string) string {
	open := strings.Index(full, "[")
	close_ := strings.Index(full, "](")
	if open < 0 || close_ <= open {
		return ""
	}
	return full[open+1 : close_]
}

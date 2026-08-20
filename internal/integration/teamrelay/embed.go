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

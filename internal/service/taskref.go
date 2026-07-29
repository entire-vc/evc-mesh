package service

import (
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// This file recognises the ways a human or an agent actually writes a Mesh task
// reference into a pull request. Until 2026-07-29 the webhook understood exactly
// one spelling — MESH-<full uuid> — which nobody writes, so 444 deliveries over
// seven days produced zero rows in vcs_links and the merge-train sign-off gate
// had nothing to join on.
//
// The deliberate asymmetry here: a bare UUID or a bare hex token is NOT a
// reference. Only a token carrying evidence that it means a task counts —
// the MESH- prefix, a /t/<id> URL path, a Refs/Closes-style keyword, a '#'
// sigil, or a branch-name segment. A pull request body is full of SHAs, IDs
// and issue numbers; matching them would resolve to the wrong task, which is
// worse than not resolving at all.

// TaskRefKind names the spelling a reference was found in. It is carried through
// to the logs so a future reader can tell which form is actually earning its
// keep and which is dead weight.
type TaskRefKind string

const (
	RefKindMeshPrefix  TaskRefKind = "mesh-prefix"  // MESH-<uuid>
	RefKindURL         TaskRefKind = "url"          // https://mesh.entire.host/t/<uuid|short>
	RefKindKeywordUUID TaskRefKind = "keyword-uuid" // Refs <uuid>
	RefKindShortID     TaskRefKind = "short-id"     // #<8-12 hex> or Refs <8-12 hex>
	RefKindBranch      TaskRefKind = "branch"       // linus/<8-12 hex>-slug
)

// TaskRef is one candidate reference. Exactly one of Full / Short is set:
// Full is ready to look up by ID, Short needs a prefix resolution that only
// the database can do.
type TaskRef struct {
	Kind   TaskRefKind
	Full   uuid.UUID
	Short  string // lowercase hex, 6–12 chars
	Raw    string // the matched text, for logging
	Source string // "title" | "body" | "branch"
}

// TaskRefSource is a named chunk of text to scan.
type TaskRefSource struct {
	Name string
	Text string
}

const uuidPat = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`

// refKeywords are the words that turn an adjacent bare token into a claim about
// a task. Kept deliberately short: every addition widens the false-positive
// surface, and a wrong task is worse than no task.
const refKeywords = `refs?|closes?|closed|fixes?|fixed|resolves?|resolved|task|mesh`

var (
	// MESH-<uuid>. The original (and only) form the webhook understood; kept
	// first in priority so existing producers are never re-interpreted.
	reMeshPrefix = regexp.MustCompile(`(?i)\bMESH-(` + uuidPat + `)\b`)

	// Any host serving /t/<id> — that path shape is how Mesh links a task, and
	// the id is either a full UUID or a short prefix. The host is not pinned:
	// staging, a port, and a future rename all keep working, and a wrong host
	// simply fails to resolve rather than linking the wrong task.
	reTaskURL = regexp.MustCompile(`(?i)\bhttps?://[a-z0-9.\-]+(?::\d+)?/t/(` + uuidPat + `|[0-9a-f]{6,12})\b`)

	// Refs <uuid> / Closes: <uuid> — a bare UUID only counts with a keyword in
	// front of it, so a UUID quoted from a log or a payload is not a reference.
	reKeywordUUID = regexp.MustCompile(`(?i)\b(?:` + refKeywords + `)\b[\s:=#\-]{0,4}(` + uuidPat + `)\b`)

	// #<8-12 hex> — the form agents write ("Refs #82377a26"). The leading class
	// rejects a match inside a longer token and inside an HTML entity (&#8212;).
	reHashShort = regexp.MustCompile(`(?i)(?:^|[^0-9a-z_/&])#([0-9a-f]{8,12})\b`)

	// Refs #82377a26 — a keyword AND the sigil. The sigil is not redundant here:
	// dropping it makes "Fixes a1b2c3d4" a reference, which is the kernel's
	// "Fixes: <sha>" trailer naming a commit, and makes "my task abcdef123456"
	// one too. Both are ordinary English/VCS prose with nothing to do with Mesh,
	// and either would attach the PR to whatever task sits under that hex
	// prefix. What a keyword buys is not a looser shape but a looser alphabet:
	// with it, an all-digit short id is allowed (see isShortID).
	reKeywordShort = regexp.MustCompile(`(?i)\b(?:` + refKeywords + `)\b[\s:=]{0,3}#([0-9a-f]{6,12})\b`)
)

// branchDelims splits a branch name into segments. Our convention is
// <agent>/<slug>, but branches cut from a task often carry its id in a segment.
const branchDelims = "/_-."

// ExtractTaskRefs returns every candidate reference found in the sources, in
// resolution order: most explicit spelling first, and within a spelling, in the
// order the sources were passed (title before body before branch). The caller
// resolves them in order and takes the first that names a real task — ordering
// by explicitness means an accidental match in the body can never outrank a
// deliberate MESH- token in the title.
func ExtractTaskRefs(sources ...TaskRefSource) []TaskRef {
	var (
		out  []TaskRef
		seen = map[string]bool{}
	)
	add := func(r TaskRef) {
		// Keyed on the target, not the spelling: "MESH-<uuid>" also satisfies
		// the keyword-uuid pattern, and one task found twice is one candidate.
		// First writer wins, so the recorded Kind is the most explicit one.
		key := r.Full.String() + "|" + r.Short
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, r)
	}

	// Pass 1–3: full-UUID forms, which need no database round-trip to be certain.
	for _, re := range []struct {
		re   *regexp.Regexp
		kind TaskRefKind
	}{
		{reMeshPrefix, RefKindMeshPrefix},
		{reTaskURL, RefKindURL},
		{reKeywordUUID, RefKindKeywordUUID},
	} {
		for _, src := range sources {
			for _, m := range re.re.FindAllStringSubmatch(src.Text, -1) {
				tok := strings.ToLower(m[1])
				if id, err := uuid.Parse(tok); err == nil {
					add(TaskRef{Kind: re.kind, Full: id, Raw: m[0], Source: src.Name})
				} else if re.kind == RefKindURL && isShortID(tok, false) {
					// /t/<short> — the UI's own short link. A /t/ path cannot be
					// mistaken for an issue number, so digits alone are fine.
					add(TaskRef{Kind: RefKindURL, Short: tok, Raw: m[0], Source: src.Name})
				}
			}
		}
	}

	// Pass 4: short ids in the text. A keyword in front of the token ("Refs
	// 12345678") is enough evidence on its own; a bare "#12345678" is not, so
	// only that form insists on a hex letter to tell an id from an issue number.
	for _, form := range []struct {
		re            *regexp.Regexp
		requireLetter bool
	}{
		{reHashShort, true},
		{reKeywordShort, false},
	} {
		for _, src := range sources {
			for _, m := range form.re.FindAllStringSubmatchIndex(src.Text, -1) {
				tok := strings.ToLower(src.Text[m[2]:m[3]])
				if !isShortID(tok, form.requireLetter) {
					continue
				}
				// A short id immediately followed by '-' is the head of a UUID
				// that the full-UUID passes above have already considered;
				// taking it as a prefix here would resolve a truncation.
				if m[3] < len(src.Text) && src.Text[m[3]] == '-' {
					continue
				}
				add(TaskRef{Kind: RefKindShortID, Short: tok, Raw: src.Text[m[0]:m[1]], Source: src.Name})
			}
		}
	}

	// Pass 5: branch-name segments, last because a branch is named for humans
	// and any id in it is incidental.
	for _, src := range sources {
		if src.Name != "branch" {
			continue
		}
		for _, seg := range strings.FieldsFunc(src.Text, func(r rune) bool {
			return strings.ContainsRune(branchDelims, r)
		}) {
			seg = strings.ToLower(seg)
			// A branch segment carries no keyword, so the letter rule applies:
			// "release/20260729" is a date, not a task.
			if isShortID(seg, true) && len(seg) >= 8 {
				add(TaskRef{Kind: RefKindBranch, Short: seg, Raw: seg, Source: src.Name})
			}
		}
	}

	return out
}

// isShortID reports whether tok can be a Mesh short id: 6–12 lowercase hex
// chars, and — when requireLetter is set — carrying at least one a–f digit.
//
// About one task UUID in forty starts with eight decimal digits, so the letter
// rule is a real blind spot, not a free win. It is applied only where the
// surrounding text gives no other evidence: a bare "#12345678" is far more
// likely to be an issue number than a task, and linking the wrong task is worse
// than linking none. Anywhere the intent is explicit — a keyword, a /t/ path, a
// MESH- prefix — the digits-only id resolves normally.
func isShortID(tok string, requireLetter bool) bool {
	if len(tok) < 6 || len(tok) > 12 {
		return false
	}
	letter := false
	for _, c := range tok {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
			letter = true
		default:
			return false
		}
	}
	return letter || !requireLetter
}

// TruncateForLog shortens s for a log line, collapsing newlines so one PR body
// or commit message cannot become forty lines of journal. Exported because the
// handler logs the same kind of untrusted text on the push path.
func TruncateForLog(s string, n int) string { return truncate(s, n) }

// truncate shortens s for a log line, collapsing newlines so one PR body cannot
// become forty lines of journal.
func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

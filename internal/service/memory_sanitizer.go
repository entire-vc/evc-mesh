package service

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"
)

// ── Write-path sanitizer for memory content ──────────────────────────────────
//
// Memory written here is injected into OTHER agents' context at session start
// (wake-up recall, fiddler prompt "MANDATORY THIRD STEP"). That makes the write
// path a delivery mechanism: a record containing "ignore previous instructions"
// reaches everyone who recalls it, and a record containing a live credential
// republishes that credential into every session that reads it. Neither needs
// malice — an agent pasting a log excerpt does both by accident.
//
// So content is REFUSED at write time with a named reason rather than stored
// and quietly mangled. A refusal the caller can read is recoverable in one
// retry; a silent redaction teaches nothing and a silent accept teaches worse.
//
// ── What this does NOT do (read this before trusting it) ─────────────────────
//
// This is a partial control and must not be described as "memory is now safe".
// Our own measurement (#e4e118ad, 13 697 transcripts) found 20 secrets by SHAPE
// versus 1 202 (field, value) pairs across 271 field NAMES — a ~60x undercount.
// STRIPE_WEBHOOK_SECRET, JWT_SECRET, GOG_KEYRING_PASSWORD and the password
// inside a DATABASE_URL have no recognisable shape at all.
//
// Concretely: a bare secret VALUE with no field name and no known prefix — the
// exact shape of the Casdoor client_secret pasted into a chat as one line
// (#d2f79c73) — matches NONE of the rules below. The incident that motivates
// this work would not have been caught by this work. The half of the rule set
// that matches an ASSIGNMENT (`*_SECRET=<value>`) is the valuable half; the
// half that matches a shape (`sk-`, `ghp_`, PEM) is narrow by construction.
//
// ── Precision ────────────────────────────────────────────────────────────────
//
// Every threshold below was tuned against the live corpus (4 051 rows, prod
// 2026-08-20) rather than guessed, because a guard on the write path that
// misfires blocks the fleet's memory. The retrospective counts are in the task
// thread (#f78232c4). Where a naive rule produced false positives on real
// records, the rule was narrowed and the narrowing is commented at its site.

// memorySanitizerDisabledEnv is an operational kill switch. The sanitizer sits
// on the write path of every agent's memory, so a misfire in prod blocks the
// whole fleet from remembering anything. Default is ON (fail-closed); setting
// this to "1"/"true" disables it and logs loudly on every skipped write.
const memorySanitizerDisabledEnv = "MESH_MEMORY_SANITIZER_DISABLED"

// contentViolation names one reason a memory was refused.
type contentViolation struct {
	// Label is the short machine-stable rule name (e.g. "secret-assignment").
	Label string
	// Reason explains what was found and what the caller should do about it.
	Reason string
}

// Error renders the violation in the form the caller sees:
//
//	"<label>: <reason>; memory was not written"
//
// The trailing clause matters: without it a caller reading only the first
// clause cannot tell whether the record was stored-and-flagged or refused.
func (v contentViolation) Error() string {
	return fmt.Sprintf("%s: %s; memory was not written", v.Label, v.Reason)
}

// roleTagRegex matches an LLM role tag used to fake a turn boundary.
//
// The leading boundary guard is not decoration. The unguarded form
// `</?(system|developer|assistant|tool)>` fires on our own placeholder
// notation — `evcAgent-<tool>/<ver>` and `mcp__<server>__<tool>` both appear in
// live records — because `<tool>` is also how we write "substitute a name
// here". Requiring the `<` to not follow an identifier character removes both
// without weakening the real case, where the tag stands alone.
var roleTagRegex = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_./-])</?(system|developer|assistant|tool)>`)

// instructionOverrideRegex matches an imperative to disregard prior context
// within 50 characters of what is to be disregarded.
//
// The target word list is deliberately previous/prior/system/developer and NOT
// above/earlier: "earlier" is ordinary narrative prose in our corpus ("unlike
// the earlier same-day case…", "the earlier lesson that…") and including it
// produced false positives on records with no imperative in them at all.
var instructionOverrideRegex = regexp.MustCompile(
	`(?is)\b(ignore|disregard|override|forget)\b.{0,50}?\b(previous|prior|system|developer)\b`)

// privateKeyRegex matches the universal boilerplate header of a PEM private
// key block, which is identical across every key of a given type.
var privateKeyRegex = regexp.MustCompile(`-{5}BEGIN [A-Z0-9 ]*PRIVATE KEY-{5}`)

// apiTokenRegexes match credentials whose issuer gives them a recognisable
// prefix AND a long unbroken alphanumeric run.
//
// The unbroken-run requirement is what makes the prefix usable. `sk-` alone
// matches 19 live records, nearly all of them Spark catalog slugs
// (`sk-concept-embedding`) or deliberately truncated keys quoted in an incident
// write-up (`sk-ant-oat01-t…`) — a truncated key is evidence, not a leak.
// Hyphenated slugs are made of short words; an issued token carries a long
// unbroken run. Requiring 20+ consecutive alphanumerics separates them cleanly:
// zero live records match, while a real sk-ant-api03/sk-or-v1 key does.
var apiTokenRegexes = []struct {
	re   *regexp.Regexp
	what string
}{
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]*[A-Za-z0-9]{20,}`), "an OpenAI/Anthropic-style `sk-` API key"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{32,}`), "a GitHub personal-access/OAuth token"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}`), "a GitHub fine-grained personal-access token"},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{20,}`), "a Slack token"},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "an AWS access-key ID"},
}

// secretAssignmentRegex matches `SOMETHING_SECRET=<literal value>`.
//
// This is the half of the rule set that earns its place: it keys on the field
// NAME, so it catches credentials with no recognisable shape. On the live
// corpus it surfaces three real leaked credentials (a Casdoor client secret, a
// Mesh agent key, and two prod DB passwords) that no shape-based rule sees.
//
// The value must be a literal. The character class excludes `$`, `<` and `"`,
// so `TOKEN=$(cat …)`, `TOKEN=${VAR}`, `TOKEN=<gh>` and the bare
// `grep "^GITHUB_TOKEN=" .env` do not match — those name a secret without
// carrying one, and refusing them would be pure noise. The 12-character floor
// keeps short sentinels (`=undefined`, `=true`) out.
var secretAssignmentRegex = regexp.MustCompile(
	`(?i)\b[A-Za-z][A-Za-z0-9_]*_(?:PASSWORD|PASSWD|SECRET|TOKEN|API_KEY|APIKEY|ACCESS_KEY|PRIVATE_KEY)\s*=\s*["']?[A-Za-z0-9+/_.~-]{12,}`)

// isAllowedInvisible reports whether an otherwise-invisible rune is one we
// deliberately permit.
//
// U+200D ZERO WIDTH JOINER and U+FE0E/U+FE0F (the text and emoji presentation
// selectors) are load-bearing in emoji sequences — U+FE0F alone appears in 195
// live records. Permitting them costs nothing: text smuggling needs an
// ALPHABET of at least two invisible codepoints to encode a payload, and these
// three are the only invisibles that survive this filter. A single permitted
// joiner cannot carry a message on its own.
func isAllowedInvisible(r rune) bool {
	return r == '\u200d' || r == '\ufe0e' || r == '\ufe0f' // ZWJ, VS15, VS16
}

// isDisallowedInvisible reports whether r is an invisible or direction-altering
// rune that has no legitimate place in a memory body.
//
// Covered: every Cf (format) rune — which is bidi overrides U+202A–U+202E and
// isolates U+2066–U+2069 (the Trojan-Source class, where rendered order differs
// from stored order), zero-width space/non-joiner, and the Unicode Tag block
// U+E0000–U+E007F which encodes arbitrary ASCII invisibly — plus the soft
// hyphen, Mongolian vowel separator, and the Variation Selectors Supplement
// U+E0100–U+E01EF, the modern selector-based smuggling channel.
//
// Measured against the live corpus this rejects 2 records (both a stray soft
// hyphen); no record uses any other invisible.
func isDisallowedInvisible(r rune) bool {
	if isAllowedInvisible(r) {
		return false
	}
	switch {
	case r == '\u00ad', r == '\u180e': // soft hyphen, Mongolian vowel separator
		return true
	// Variation selectors 1–16. VS15/VS16 are carved back out by
	// isAllowedInvisible above; the range is written whole here on purpose, so
	// that deleting the allowance actually changes behaviour rather than
	// leaving a comment that claims a guarantee the code never provided.
	case r >= '\ufe00' && r <= '\ufe0f':
		return true
	case r >= 0xE0100 && r <= 0xE01EF: // variation selectors supplement
		return true
	case unicode.Is(unicode.Cf, r): // includes bidi controls and the tag block
		return true
	}
	return false
}

// describeRune renders a rune as `U+XXXX` for the refusal message, so the
// caller can find and delete the exact character instead of guessing.
func describeRune(r rune) string {
	return fmt.Sprintf("U+%04X", r)
}

// scanMemoryContent returns the first violation found in content, or nil if the
// content is clean. It returns the FIRST violation rather than all of them:
// the caller's next action is identical either way (fix and retry), and one
// named reason reads better than a list.
func scanMemoryContent(content string) *contentViolation {
	for _, r := range content {
		if isDisallowedInvisible(r) {
			return &contentViolation{
				Label: "invisible-character",
				Reason: fmt.Sprintf(
					"content contains the invisible or direction-altering character %s, "+
						"which can hide text from a reader while an agent still acts on it — remove it",
					describeRune(r)),
			}
		}
	}

	if m := roleTagRegex.FindStringSubmatch(content); m != nil {
		return &contentViolation{
			Label: "role-tag",
			Reason: fmt.Sprintf(
				"content contains the role tag <%s>, which can fake a turn boundary in the "+
					"context of any agent that recalls this memory — escape it or describe it in words",
				strings.ToLower(m[2])),
		}
	}

	if instructionOverrideRegex.MatchString(content) {
		return &contentViolation{
			Label: "instruction-override",
			Reason: "content instructs a reader to ignore or override previous/system instructions; " +
				"memory is injected into other agents' context, so this would be read as a live " +
				"instruction — rephrase it as a quotation or a description",
		}
	}

	if privateKeyRegex.MatchString(content) {
		return &contentViolation{
			Label: "private-key",
			Reason: "content contains a PEM private-key block — store the key in a secret manager " +
				"and record only its location here",
		}
	}

	for _, t := range apiTokenRegexes {
		if t.re.MatchString(content) {
			return &contentViolation{
				Label: "api-token",
				Reason: fmt.Sprintf(
					"content looks like it contains %s — rotate it if it is live, and record only "+
						"where the credential lives, not its value", t.what),
			}
		}
	}

	if m := secretAssignmentRegex.FindString(content); m != "" {
		name := m
		if i := strings.IndexAny(name, "="); i >= 0 {
			name = strings.TrimSpace(name[:i])
		}
		return &contentViolation{
			Label: "secret-assignment",
			Reason: fmt.Sprintf(
				"content assigns a literal value to %s — record where the secret lives "+
					"(env file, secret manager) instead of its value", name),
		}
	}

	return nil
}

// memorySanitizerDisabled reports whether the operational kill switch is set.
func memorySanitizerDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(memorySanitizerDisabledEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

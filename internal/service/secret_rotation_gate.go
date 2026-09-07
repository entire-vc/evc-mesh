package service

import (
	"regexp"
	"sort"
	"strings"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// secretRotationClassify ports bob/scripts/fleet_secret_rotation.py's classify() to Go
// tier-for-tier (task #bb1aaa09, tail of #e8388213). Pavel's 2026-09-07 decision — "все
// таски про ротацию ключей надо отменять... иначе это ветряная мельница" — is enforced
// on the AGENT lanes by a PreToolUse hook (secret-rotation-gate-guard.py), which cannot
// see a "❓ Blocking @pavel" marker posted through the Mesh WEB UI: there is no
// PreToolUse there. This is the server-side half that closes that gap, in
// enforceBlockingTriage — the ONE place a marker turns into an armed human_gate.
//
// KEEP IN LOCKSTEP WITH fleet_secret_rotation.py's classify(). Order is load-bearing,
// not cosmetic: opt-out beats everything, exclusions are checked BEFORE any positive
// signal (so the alert announcing the leak-COUNTER itself stopped running can never
// suppress itself — see fleet_secret_rotation.py's module doc for the two measured
// traps: a "[ALERT] ...logrotate" card and a "[ALERT] ...secret-leak-watch" card, both
// covered in secret_rotation_gate_test.go).
var (
	// secretRotationLabels: an explicit "this is a secret-leak/rotation card". Any one
	// label is enough — authoritative, no title inspection needed.
	secretRotationLabels = map[string]struct{}{
		"secret-leak": {}, "secret-rotation": {}, "rotation": {}, "secret-hygiene": {},
	}

	// secretRotationExcludeLabels: never suppress on these, whatever the title says.
	// severity:critical is the ACTIVE-EXPLOITATION escape hatch — Pavel's decision
	// defers rotation of a credential that merely leaked, not one someone is USING.
	// Deliberately NOT the bare "incident" label: most cards of this class already
	// carry it as a description of how the leak happened, not as an exploitation
	// claim — excluding on it would void the whole mechanism.
	secretRotationExcludeLabels = map[string]struct{}{
		"alert": {}, "auto-incident": {}, "kind:human-verify": {}, "kind:human": {},
		"human-verify": {}, "severity:critical": {},
	}

	// secretRotationMetaLabelPrefixes: a card of the audit PLAN is work about this
	// mechanism, not an instance of it.
	secretRotationMetaLabelPrefixes = []string{"plan:"}

	// secretRotationNoiseTokens are substrings that carry the verb/noun words but not
	// the meaning — job and file names — stripped from the title before matching so
	// "logrotate" is never read as the verb "rotate".
	secretRotationNoiseTokens = []string{
		"secret-leak-watch", "secret-container-guard", "logrotate", "log-rotate",
		"log rotation", "ротация логов", "secret-rotation-parker",
	}

	// secretRotationVerbRe / secretRotationNounRe mirror fleet_secret_rotation.py's
	// _VERB_RE / _NOUN_RE. The Cyrillic terms are deliberately bare substrings (no \b):
	// Go's regexp \b anchor is ASCII-only (unlike Python's re, which is Unicode-aware
	// by default for str patterns) — a literal port of the ONE Cyrillic term that DID
	// carry \b in Python ("\bотозв") would silently never match in Go, because Go's \b
	// treats every Cyrillic letter as a non-word character, so the boundary condition
	// between a space and a Cyrillic letter is never satisfied. Dropping that boundary
	// trades a little precision for recall, which is the same direction the module's
	// own design already prefers (a false PARK is visible and reversible via one
	// label; a missed one is silent and keeps pushing Pavel).
	//
	// [\p{L}\p{N}_]* stands in for Python's Unicode-aware \w* (Go's \w shorthand is
	// ASCII-only): needed so e.g. "учётных" continues past "учётн" to "данных".
	secretRotationVerbRe = regexp.MustCompile(`(?i)ротац|ротир|ротов|утёк|утек|утечк|засветил|засвеч|напечата|` +
		`\brotate\b|\brotated\b|\brotation\b|\bleak\b|\bleaked\b|\bleaking\b|` +
		`\bexposed\b|\bprinted\b|\brevoke\b|отозв`)

	secretRotationNounRe = regexp.MustCompile(`(?i)токен|ключ|парол|кред|секрет|` +
		`учётн[\p{L}\p{N}_]*\s*данн|учетн[\p{L}\p{N}_]*\s*данн|учётк|учетк|` +
		`\btoken\b|\bkeys?\b|\bpassword\b|\bcredential[\p{L}\p{N}_]*\b|\bcreds?\b|\bsecret[\p{L}\p{N}_]*\b|` +
		`\bpat\b|\boauth\b|\bapi[- _]?key\b|` +
		`_(?:token|key|secret|password|pat)\b`)
)

// secretRotationNotSecretRotationLabel is the one-label undo — named in the
// suppression comment so a false positive is reversible by whoever spots it without
// reading any code.
const secretRotationNotSecretRotationLabel = "not-secret-rotation"

// secretRotationStripNoise removes job/file names before title matching — mirrors
// fleet_secret_rotation.py's _strip_noise.
func secretRotationStripNoise(title string) string {
	out := title
	for _, tok := range secretRotationNoiseTokens {
		out = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(tok)).ReplaceAllString(out, " ")
	}
	return out
}

// secretRotationTitleMatches requires BOTH a leak/rotation verb and a credential noun,
// after noise stripping — one signal alone is not enough ("logrotate" has the verb and
// no noun; an alert naming our own "secret-leak-watch" job has both only because it
// names the job).
func secretRotationTitleMatches(title string) bool {
	clean := secretRotationStripNoise(title)
	return secretRotationVerbRe.MatchString(clean) && secretRotationNounRe.MatchString(clean)
}

// secretRotationClassify returns (tier, reason). tier is "" (not this class), "label"
// (an explicit label — authoritative), or "title" (inferred from a verb+noun title).
func secretRotationClassify(task *domain.Task) (tier, reason string) {
	labels := make(map[string]struct{}, len(task.Labels))
	for _, l := range task.Labels {
		labels[strings.ToLower(strings.TrimSpace(l))] = struct{}{}
	}

	if _, ok := labels[secretRotationNotSecretRotationLabel]; ok {
		return "", "opt-out label \"" + secretRotationNotSecretRotationLabel + "\""
	}

	var hitExclude []string
	for l := range secretRotationExcludeLabels {
		if _, ok := labels[l]; ok {
			hitExclude = append(hitExclude, l)
		}
	}
	if len(hitExclude) > 0 {
		sort.Strings(hitExclude)
		return "", "excluded by label(s) " + strings.Join(hitExclude, ",")
	}

	var hitMeta []string
	for l := range labels {
		for _, prefix := range secretRotationMetaLabelPrefixes {
			if strings.HasPrefix(l, prefix) {
				hitMeta = append(hitMeta, l)
				break
			}
		}
	}
	if len(hitMeta) > 0 {
		sort.Strings(hitMeta)
		return "", "audit-plan card, not a leak instance (" + strings.Join(hitMeta, ",") + ")"
	}

	var hitLabel []string
	for l := range secretRotationLabels {
		if _, ok := labels[l]; ok {
			hitLabel = append(hitLabel, l)
		}
	}
	if len(hitLabel) > 0 {
		sort.Strings(hitLabel)
		return "label", "label(s) " + strings.Join(hitLabel, ",")
	}

	if secretRotationTitleMatches(task.Title) {
		return "title", "title carries a rotation/leak verb and a credential noun"
	}

	return "", "no rotation label, no verb+noun title"
}

// secretRotationSuppressionReason is non-empty iff a "❓ Blocking @user" marker on this
// task must NOT arm human_gate — Pavel's 2026-09-07 decision covers it, and re-asking
// is a repeat of an already-answered question, not a new one.
func secretRotationSuppressionReason(task *domain.Task) string {
	tier, reason := secretRotationClassify(task)
	if tier == "" {
		return ""
	}
	return "secret-leak/rotation (" + tier + ": " + reason + ") — Pavel decided 2026-09-07, see task #e8388213"
}

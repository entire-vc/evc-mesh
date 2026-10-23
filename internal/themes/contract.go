// Package themes is the backend of Mesh themes (ADR themes-three-layers): the
// kind=theme manifest checks the JSON Schema cannot express, installation of a
// theme's assets, the instance/workspace choice, and the CSS and contrast
// derived from a theme's tokens.
package themes

import (
	"regexp"
	"strconv"
	"strings"
)

// KindTheme is the manifest kind of a theme.
const KindTheme = "theme"

// TokensContractV1 is the contract version this build understands.
const TokensContractV1 = "1"

// tokenKind is how a token's value is validated before it reaches a stylesheet.
type tokenKind int

const (
	kindColor  tokenKind = iota // "H S% L%", as brandkit.css writes them
	kindLength                  // 0.5rem, 4px
	kindFont                    // a font-family list
)

// tokenContractV1 is the token set of decisions/adr-themes-three-layers/token-contract-v1:
// 57 colour tokens and 4 others.
var tokenContractV1 = buildTokenContractV1()

func buildTokenContractV1() map[string]tokenKind {
	m := map[string]tokenKind{}
	add := func(kind tokenKind, names ...string) {
		for _, n := range names {
			m[n] = kind
		}
	}
	add(kindColor,
		// A. existing, 28
		"background", "foreground", "card", "card-foreground", "popover", "popover-foreground",
		"primary", "primary-foreground", "secondary", "secondary-foreground",
		"muted", "muted-foreground", "accent", "accent-foreground",
		"destructive", "destructive-foreground", "success", "success-foreground", "warning", "warning-foreground",
		"border", "input", "ring",
		"sidebar-background", "sidebar-foreground", "sidebar-primary", "sidebar-accent", "sidebar-border",
		// B. new, 29
		"sidebar-primary-foreground", "sidebar-accent-foreground", "info", "info-foreground",
		"priority-urgent", "priority-high", "priority-medium", "priority-low", "priority-none",
		"status-backlog", "status-triage", "status-todo", "status-in-progress", "status-review", "status-done", "status-cancelled",
	)
	for i := 1; i <= 5; i++ {
		add(kindColor, "chart-"+strconv.Itoa(i))
	}
	for i := 1; i <= 8; i++ {
		add(kindColor, "avatar-"+strconv.Itoa(i))
	}
	// C. other, 4
	add(kindLength, "radius", "space-unit")
	add(kindFont, "font-sans", "font-mono")
	return m
}

var (
	colorValue  = regexp.MustCompile(`^\d{1,3}(\.\d+)?\s+\d{1,3}(\.\d+)?%\s+\d{1,3}(\.\d+)?%$`)
	lengthValue = regexp.MustCompile(`^\d+(\.\d+)?(rem|em|px)$`)
	// A font list is names, quoted names and commas; nothing that could end the
	// declaration or open a url().
	fontValue = regexp.MustCompile(`^[A-Za-z0-9 ,'"._-]{1,200}$`)
)

// IsToken reports whether name is a token of contract v1.
func IsToken(name string) bool {
	_, ok := tokenContractV1[name]
	return ok
}

// TokenCount is the size of the contract (61 for v1).
func TokenCount() int { return len(tokenContractV1) }

// checkTokenValue validates one value against the token's kind.
func checkTokenValue(name, value string) bool {
	kind, ok := tokenContractV1[name]
	if !ok {
		return false
	}
	value = strings.TrimSpace(value)
	switch kind {
	case kindColor:
		return colorValue.MatchString(value)
	case kindLength:
		return lengthValue.MatchString(value)
	default:
		return fontValue.MatchString(value)
	}
}

// slotContracts is the embedded copy of the SDK's slot-contracts.json: the
// current version of every themable slot. A theme asks for a major range
// (^N); the core serves the current major and the previous one (ADR D6).
// dashboard.widget.<name> slots share the entry "dashboard.widget".
var slotContracts = map[string]string{
	"board.layout":     "1.0.0",
	"board.column":     "1.0.0",
	"board.card":       "1.0.0",
	"task.row":         "1.0.0",
	"task.view":        "1.0.0",
	"dashboard.layout": "1.0.0",
	"dashboard.widget": "1.0.0",
	"nav.sidebar":      "1.0.0",
	"nav.topbar":       "1.0.0",
	"doc.tree":         "1.0.0",
	"doc.view":         "1.0.0",
	"auth.screen":      "1.0.0",
}

// slotKey maps a slot name to its slotContracts entry; ok is false for a slot
// that does not exist (task.form among them: the form is frozen, §1r.K).
func slotKey(slot string) (string, bool) {
	if strings.HasPrefix(slot, "dashboard.widget.") {
		return "dashboard.widget", true
	}
	_, ok := slotContracts[slot]
	return slot, ok
}

// semver is a x.y.z version.
type semver struct{ major, minor, patch int }

func parseSemver(v string) (semver, bool) {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var n [3]int
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return semver{}, false
		}
		n[i] = x
	}
	return semver{n[0], n[1], n[2]}, true
}

// parseCaret reads "^N" into N; 0 means the range is not a caret major.
func parseCaret(r string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(r, "^"))
	if !strings.HasPrefix(r, "^") || err != nil || n <= 0 {
		return 0
	}
	return n
}

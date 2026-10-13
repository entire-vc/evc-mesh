// Package i18n is the server-side message catalog. A notification event carries
// a message code and language-neutral parameters; the text is produced here,
// per recipient, at the moment it is delivered or displayed — never at the
// moment the event is raised, when the recipient's language is not yet known.
package i18n

import (
	"fmt"
	"strings"
)

// Supported locales. Kept in step with domain.SupportedLocales (a new language is
// a new catalog map here plus the migration widening the CHECK constraints).
const (
	EN = "en"
	RU = "ru"

	Default = EN
)

var catalogs = map[string]map[string]string{
	EN: catalogEN,
	RU: catalogRU,
}

// Normalize maps any input to a supported locale, falling back to Default.
func Normalize(locale string) string {
	if _, ok := catalogs[locale]; ok {
		return locale
	}
	return Default
}

// Has reports whether code has an entry in the English catalog, which is the
// reference every other language is measured against.
func Has(code string) bool {
	_, ok := catalogEN[code]
	return ok
}

// T renders code for locale with {name} placeholders replaced from params.
//
// Lookup order: the locale's own entry, then English. A code unknown to both
// returns "" so the caller can fall back to the text the event already carries
// rather than showing a raw key to a person.
//
// When params holds a numeric "count", a plural-category suffix is tried first
// (code+".one" / ".few" / ".many" / ".other"), then the bare code.
func T(locale, code string, params map[string]any) string {
	locale = Normalize(locale)
	keys := []string{code}
	if n, ok := countParam(params); ok {
		cat := pluralCategory(locale, n)
		keys = []string{code + "." + cat, code + ".other", code}
	}
	for _, l := range []string{locale, Default} {
		for _, k := range keys {
			if tpl, ok := catalogs[l][k]; ok {
				return render(locale, tpl, params)
			}
		}
	}
	return ""
}

func render(locale, tpl string, params map[string]any) string {
	if len(params) == 0 {
		return tpl
	}
	pairs := make([]string, 0, len(params)*2)
	for k, v := range params {
		val := fmt.Sprint(v)
		// An unknown actor is stored as an empty name; "Someone" is chosen here,
		// in the reader's language, not when the event was raised.
		if k == "actor" && val == "" {
			val = catalogs[locale]["notif.actor.someone"]
		}
		pairs = append(pairs, "{"+k+"}", val)
	}
	return strings.NewReplacer(pairs...).Replace(tpl)
}

func countParam(params map[string]any) (int, bool) {
	switch v := params["count"].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64: // after a JSON round trip
		return int(v), true
	}
	return 0, false
}

func pluralCategory(locale string, n int) string {
	if n < 0 {
		n = -n
	}
	if locale == RU {
		switch {
		case n%10 == 1 && n%100 != 11:
			return "one"
		case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
			return "few"
		default:
			return "many"
		}
	}
	if n == 1 {
		return "one"
	}
	return "other"
}

package domain

// Supported interface languages. The list lives here once; the web client
// mirrors it through its locale files, so a third language is a new entry plus
// a translation file (and a migration widening the CHECK constraints).
const (
	LocaleEN = "en"
	LocaleRU = "ru"

	DefaultLocale = LocaleEN
)

// SupportedLocales returns the accepted locale codes in display order.
func SupportedLocales() []string {
	return []string{LocaleEN, LocaleRU}
}

// IsSupportedLocale reports whether code is an accepted locale.
func IsSupportedLocale(code string) bool {
	for _, l := range SupportedLocales() {
		if l == code {
			return true
		}
	}
	return false
}

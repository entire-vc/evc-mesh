package domain

import "testing"

func TestIsSupportedLocale(t *testing.T) {
	for _, ok := range []string{"en", "ru"} {
		if !IsSupportedLocale(ok) {
			t.Errorf("%q must be supported", ok)
		}
	}
	for _, bad := range []string{"", "de", "EN", "en-US", "ru ", "xx"} {
		if IsSupportedLocale(bad) {
			t.Errorf("%q must be rejected", bad)
		}
	}
	if !IsSupportedLocale(DefaultLocale) {
		t.Error("default locale must be supported")
	}
}

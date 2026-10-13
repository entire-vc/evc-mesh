package service

import (
	"regexp"
	"testing"
)

func TestSystemCodesUniqueAndWellFormed(t *testing.T) {
	re := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	seen := map[string]bool{}
	for _, c := range SystemCodes {
		if !re.MatchString(c) {
			t.Errorf("code %q is not snake_case", c)
		}
		if seen[c] {
			t.Errorf("duplicate code %q", c)
		}
		seen[c] = true
	}
	if len(SystemCodes) < 25 {
		t.Errorf("SystemCodes shrank to %d entries; codes are a contract, never remove one", len(SystemCodes))
	}
}

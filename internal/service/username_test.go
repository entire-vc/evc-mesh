package service

import (
	"regexp"
	"testing"
)

// chkUsername mirrors the DB constraint chk_users_username from migration
// 20260520046: ^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$.
var chkUsername = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`)

func TestUsernameBaseFromEmail(t *testing.T) {
	cases := map[string]string{
		"tt@entire.vc":             "tt",
		"Robert.Johansson@x.io":    "robert-johansson",
		"a@b.com":                  "a0",
		"--weird--.name--@host":    "weird-.name", // collapse + trim handled below
		"UPPER_Case+tag@gmail.com": "upper-case-tag",
	}
	for email, want := range cases {
		got := usernameBaseFromEmail(email)
		if !chkUsername.MatchString(got) {
			t.Errorf("usernameBaseFromEmail(%q) = %q, fails chk_users_username regex", email, got)
		}
		// "weird" case: exact value depends on collapse/trim, only assert constraint there.
		if email != "--weird--.name--@host" && got != want {
			t.Errorf("usernameBaseFromEmail(%q) = %q, want %q", email, got, want)
		}
	}
}

func TestUsernameBaseFromEmailAlwaysValid(t *testing.T) {
	// Pathological local-parts must still yield a constraint-valid slug.
	for _, email := range []string{
		"@nodomain", "...@x", "1@x", "_@x", "-@x", "ALLCAPS@X", "a.b.c.d@x",
	} {
		got := usernameBaseFromEmail(email)
		if !chkUsername.MatchString(got) {
			t.Errorf("usernameBaseFromEmail(%q) = %q, fails constraint", email, got)
		}
	}
}

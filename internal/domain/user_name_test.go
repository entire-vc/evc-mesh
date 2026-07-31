package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A "placeholder" name is what every path that provisioned an account without
// asking left behind: display_name holding the address. The UI needs to tell it
// apart from a real name, because rendering name-over-address prints the same
// string twice and reads as a bug rather than as missing data.
func TestIsPlaceholderName(t *testing.T) {
	cases := []struct {
		label, name, email string
		want               bool
	}{
		{"a real name", "Jane Cooper", "jane@example.com", false},
		{"the address verbatim", "jane@example.com", "jane@example.com", true},
		{"the address in another case", "JANE@Example.com", "jane@example.com", true},
		{"the address with padding", "  jane@example.com  ", "jane@example.com", true},
		{"empty", "", "jane@example.com", true},
		{"whitespace only", "   \t", "jane@example.com", true},
		{"a name that merely contains the address", "Jane <jane@example.com>", "jane@example.com", false},
		{"a name with no address to compare against", "Jane", "", false},
		{"nothing at all", "", "", true},
		{"a different address", "someone@else.com", "jane@example.com", false},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			assert.Equal(t, tc.want, IsPlaceholderName(tc.name, tc.email))
		})
	}
}

func TestUser_NameIsPlaceholder(t *testing.T) {
	unnamed := &User{Email: "sid@webs-company.ru", Name: "sid@webs-company.ru"}
	assert.True(t, unnamed.NameIsPlaceholder())

	named := &User{Email: "sid@webs-company.ru", Name: "Sid Vicious"}
	assert.False(t, named.NameIsPlaceholder())
}

func TestUserBrief_NameIsPlaceholder(t *testing.T) {
	unnamed := UserBrief{Email: "sid@webs-company.ru", Name: "sid@webs-company.ru"}
	assert.True(t, unnamed.NameIsPlaceholder())

	named := UserBrief{Email: "sid@webs-company.ru", Name: "Sid Vicious", Username: "sid"}
	assert.False(t, named.NameIsPlaceholder())
}

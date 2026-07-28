package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The mock user repository matches emails byte-for-byte, exactly like the old
// `WHERE email = $1` query did. That is deliberate: it means these tests fail
// unless Register and Login both canonicalize the address themselves, rather
// than leaning on the database's lower(email) index to paper over it.

func TestNormalizeEmail(t *testing.T) {
	cases := map[string]string{
		"carol@example.com":     "carol@example.com",
		"Carol@Example.COM":     "carol@example.com",
		"  dave@example.com  ":  "dave@example.com",
		"\tErin@Example.com\n":  "erin@example.com",
		"":                      "",
		"   ":                   "",
		"MiXeD.Case+tag@EX.org": "mixed.case+tag@ex.org",
	}
	for in, want := range cases {
		assert.Equal(t, want, NormalizeEmail(in), "NormalizeEmail(%q)", in)
	}
}

func TestRegister_StoresNormalizedEmail(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	user, _, err := svc.Register(context.Background(), "  Carol@Example.COM  ", "StrongP4ss", "Carol")
	require.NoError(t, err)
	assert.Equal(t, "carol@example.com", user.Email,
		"the stored address must be trimmed and lowercased")
	assert.Equal(t, "carol", user.Username,
		"username derivation must run on the normalized local-part")
}

func TestLogin_IsCaseAndWhitespaceInsensitive(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	_, _, err := svc.Register(context.Background(), "Carol@Example.COM", "StrongP4ss", "Carol")
	require.NoError(t, err)

	// Every spelling of the same address must reach the same account. Before
	// normalization, only the exact registration spelling worked and the rest
	// returned 401.
	for _, spelling := range []string{
		"carol@example.com",
		"Carol@Example.COM",
		"CAROL@EXAMPLE.COM",
		"  carol@example.com  ",
	} {
		user, tokens, loginErr := svc.Login(context.Background(), spelling, "StrongP4ss")
		require.NoError(t, loginErr, "login with %q must succeed", spelling)
		require.NotNil(t, tokens)
		assert.Equal(t, "carol@example.com", user.Email)
		assert.NotEmpty(t, tokens.AccessToken)
	}
}

func TestRegister_RejectsCaseVariantOfExistingEmail(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	_, _, err := svc.Register(context.Background(), "Carol@Example.COM", "StrongP4ss", "Carol")
	require.NoError(t, err)

	// This is the second half of the bug: the case-variant used to be accepted
	// and became a shadow account nobody could reliably log in to.
	for _, spelling := range []string{
		"carol@example.com",
		"CAROL@example.com",
		" carol@example.com ",
	} {
		_, _, dupErr := svc.Register(context.Background(), spelling, "StrongP4ss", "Impostor")
		require.ErrorIs(t, dupErr, ErrEmailAlreadyExists, "registering %q again must conflict", spelling)
	}
}

func TestRegister_WhitespacePaddedEmailCanLogIn(t *testing.T) {
	svc, _, _, _, _ := newTestService()

	// " dave@example.com " used to register fine (mail.ParseAddress tolerates
	// the padding) and then never match on login.
	_, _, err := svc.Register(context.Background(), " dave@example.com ", "StrongP4ss", "Dave")
	require.NoError(t, err)

	_, tokens, err := svc.Login(context.Background(), "dave@example.com", "StrongP4ss")
	require.NoError(t, err)
	require.NotNil(t, tokens)
	assert.NotEmpty(t, tokens.AccessToken)
}

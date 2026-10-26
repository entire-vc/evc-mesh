package postgres

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInheritCredentialRefs(t *testing.T) {
	previous := []byte(`{"schema_version":2,"accounts":{"a":{"provider":"p","credential_ref":"cred:a"},"b":{"provider":"p","credential_ref":"cred:b"}}}`)
	input := []byte(`{"schema_version":2,"accounts":{"a":{"provider":"p","credential_ref":""},"b":{"provider":"p"},"c":{"provider":"p","credential_ref":""},"d":{"provider":"p","credential_ref":"cred:d-new"}}}`)
	var out struct {
		Accounts map[string]map[string]any `json:"accounts"`
	}
	require.NoError(t, json.Unmarshal(inheritCredentialRefs(input, previous), &out))
	require.Equal(t, "cred:a", out.Accounts["a"]["credential_ref"], "blank ref inherits")
	require.Equal(t, "cred:b", out.Accounts["b"]["credential_ref"], "missing ref inherits")
	require.Equal(t, "", out.Accounts["c"]["credential_ref"], "a new account has nothing to inherit")
	require.Equal(t, "cred:d-new", out.Accounts["d"]["credential_ref"], "an explicit ref is kept")

	// A changed ref on an existing account wins over the stored one.
	changed := []byte(`{"accounts":{"a":{"credential_ref":"cred:rotated"}}}`)
	require.JSONEq(t, string(changed), string(inheritCredentialRefs(changed, previous)))
	// Malformed input is passed through for the schema to refuse.
	require.Equal(t, []byte(`[1]`), inheritCredentialRefs([]byte(`[1]`), previous))
	require.Equal(t, []byte(`{"accounts":{"a":null}}`), inheritCredentialRefs([]byte(`{"accounts":{"a":null}}`), previous))
}

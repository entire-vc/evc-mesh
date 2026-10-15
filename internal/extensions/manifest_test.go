package extensions

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManifest_InstanceSecretKeys(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want []string
	}{
		"ref": {`{"id":"a","version":"1","runtime":"builtin","settings":{"instance":{"$ref":"#/schemas/c"}},
			"schemas":{"c":{"properties":{"t":{"x-secret":true},"u":{},"s":{"x-secret":true}}}}}`, []string{"s", "t"}},
		"inline":       {`{"id":"a","version":"1","runtime":"builtin","settings":{"instance":{"properties":{"k":{"x-secret":true}}}}}`, []string{"k"}},
		"no-secrets":   {`{"id":"a","version":"1","runtime":"builtin","settings":{"instance":{"properties":{"k":{}}}}}`, nil},
		"no-settings":  {`{"id":"a","version":"1","runtime":"builtin"}`, nil},
		"dangling-ref": {`{"id":"a","version":"1","runtime":"builtin","settings":{"instance":{"$ref":"#/schemas/x"}},"schemas":{}}`, nil},
	}
	for name, tc := range cases {
		m, err := ParseManifest([]byte(tc.raw))
		require.NoError(t, err, name)
		require.Equal(t, tc.want, m.InstanceSecretKeys(), name)
	}
}

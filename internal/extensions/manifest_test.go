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

func TestManifest_SecretKeysPerLevel(t *testing.T) {
	m, err := ParseManifest([]byte(`{"id":"a","version":"1","runtime":"builtin",
		"settings":{"instance":{"properties":{"i":{"x-secret":true}}},"workspace":{"$ref":"#/schemas/w"},"project":{"properties":{"p":{"x-secret":true}}}},
		"schemas":{"w":{"properties":{"w":{"x-secret":true},"n":{}}}}}`))
	require.NoError(t, err)
	require.Equal(t, []string{"i"}, m.SecretKeys("instance"))
	require.Equal(t, []string{"w"}, m.SecretKeys("workspace"))
	require.Equal(t, []string{"p"}, m.SecretKeys("project"))
	require.Nil(t, m.SecretKeys("nope"))
}

func TestManifest_HasInstanceFields(t *testing.T) {
	base := `{"schema":1,"id":"x","name":"X","version":"1.0.0","kind":"integration","runtime":"builtin","publisher":"p","description":"d","category":"sync","levels":{"instance":true,"workspace":true,"project":false},`
	for name, tc := range map[string]struct {
		tail string
		want bool
	}{
		"inline fields":  {`"settings":{"instance":{"type":"object","properties":{"a":{"type":"string"}}}}}`, true},
		"ref with field": {`"settings":{"instance":{"$ref":"#/schemas/c"}},"schemas":{"c":{"type":"object","properties":{"a":{"type":"string"}}}}}`, true},
		"empty schema":   {`"settings":{"instance":{"type":"object","properties":{},"additionalProperties":false}}}`, false},
		"ref to empty":   {`"settings":{"instance":{"$ref":"#/schemas/c"}},"schemas":{"c":{"type":"object","properties":{}}}}`, false},
		"no settings":    {`"settings":{}}`, false},
	} {
		m, err := ParseManifest([]byte(base + tc.tail))
		require.NoError(t, err, name)
		require.Equal(t, tc.want, m.HasInstanceFields(), name)
	}
}

func TestManifest_SettingsSchema(t *testing.T) {
	parse := func(raw string) Manifest {
		m, err := ParseManifest([]byte(raw))
		require.NoError(t, err)
		return m
	}
	inline := parse(`{"id":"a","version":"1","runtime":"external","settings":{"instance":{"type":"object"}}}`)
	require.JSONEq(t, `{"type":"object"}`, string(inline.SettingsSchema("instance")))

	// The shared schemas block travels with the level, so nested refs resolve.
	ref := parse(`{"id":"a","version":"1","runtime":"external","settings":{"instance":{"$ref":"#/schemas/i"}},"schemas":{"i":{"type":"object"}}}`)
	require.JSONEq(t, `{"$ref":"#/schemas/i","schemas":{"i":{"type":"object"}}}`, string(ref.SettingsSchema("instance")))

	for name, raw := range map[string]string{
		"level-absent":  `{"id":"a","version":"1","runtime":"external","settings":{"workspace":{"type":"object"}}}`,
		"level-null":    `{"id":"a","version":"1","runtime":"external","settings":{"instance":null}}`,
		"no-settings":   `{"id":"a","version":"1","runtime":"external"}`,
		"not-an-object": `{"id":"a","version":"1","runtime":"external","settings":{"instance":true}}`,
		"bad-settings":  `{"id":"a","version":"1","runtime":"external","settings":[1]}`,
	} {
		require.Nil(t, parse(raw).SettingsSchema("instance"), name)
	}
}

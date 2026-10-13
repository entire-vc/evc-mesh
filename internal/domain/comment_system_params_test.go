package domain

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The coverage gate measures each package by its own tests, so SystemParams is
// exercised here rather than only through the repository tests.
func TestSystemParams_Scan(t *testing.T) {
	var p SystemParams
	require.NoError(t, p.Scan(nil))
	assert.Nil(t, p)

	require.NoError(t, p.Scan([]byte(`{"actor":"a"}`)))
	assert.JSONEq(t, `{"actor":"a"}`, string(p))

	require.NoError(t, p.Scan(`{"n":1}`))
	assert.JSONEq(t, `{"n":1}`, string(p))

	assert.Error(t, p.Scan(42))
}

func TestSystemParams_Value(t *testing.T) {
	v, err := SystemParams(nil).Value()
	require.NoError(t, err)
	assert.Nil(t, v, "unset params must be stored as SQL NULL")

	v, err = SystemParams(`{"a":1}`).Value()
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"a":1}`), v)
}

func TestSystemParams_JSONRoundTrip(t *testing.T) {
	type wrap struct {
		P SystemParams `json:"p"`
	}
	b, err := json.Marshal(wrap{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"p":null}`, string(b), "old comments serialize null")

	var w wrap
	require.NoError(t, json.Unmarshal([]byte(`{"p":{"k":"v"}}`), &w))
	assert.JSONEq(t, `{"k":"v"}`, string(w.P))
	b, err = json.Marshal(w)
	require.NoError(t, err)
	assert.JSONEq(t, `{"p":{"k":"v"}}`, string(b))

	require.NoError(t, json.Unmarshal([]byte(`{"p":null}`), &w))
	assert.Nil(t, w.P)
}

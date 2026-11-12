package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// The PUT body is exactly one JSON object: anything after it, including a
// stray closing bracket that json.Decoder.More does not see, is refused.
func TestDecodeRepositoryAccessPut_OneObjectOnly(t *testing.T) {
	decode := func(body string) error {
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
		c := echo.New().NewContext(req, httptest.NewRecorder())
		_, err := decodeRepositoryAccessPut(c)
		return err
	}
	const valid = `{"expected_revision":1,"repositories":[]}`
	require.NoError(t, decode(valid), "positive control")
	require.NoError(t, decode(" \n"+valid+"\n\t "), "surrounding whitespace is not data")
	for name, body := range map[string]string{
		"second object":    valid + `{}`,
		"stray ]":          valid + `]`,
		"stray }":          valid + `}`,
		"garbage":          valid + `garbage`,
		"second value":     valid + ` 1`,
		"unknown field":    `{"expected_revision":1,"repositories":[],"x":1}`,
		"null revision":    `{"expected_revision":null,"repositories":[]}`,
		"null list":        `{"expected_revision":1,"repositories":null}`,
		"empty body":       ``,
		"array not object": `[]`,
	} {
		require.Error(t, decode(body), name)
	}
}

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

var cyrillic = regexp.MustCompile(`[\x{0400}-\x{04FF}]`)

// builtinListBody calls GET /extensions through the real handler with the
// shipped github and runner manifests and returns the response body.
func builtinListBody(t *testing.T, acceptLanguage string) string {
	t.Helper()
	var rows []domain.InstalledExtension
	for _, name := range []string{"github", "runner"} {
		raw, err := os.ReadFile("../extensions/" + name + "/mesh-extension.json")
		require.NoError(t, err)
		rows = append(rows, domain.InstalledExtension{
			ExtensionID: name, Version: "1", Source: domain.ExtensionSourceBuiltin, Manifest: json.RawMessage(raw),
		})
	}
	h := NewExtensionHandler(&fakeInstalledRepo{rows: rows}, nil)
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	if acceptLanguage != "" {
		req.Header.Set("Accept-Language", acceptLanguage)
	}
	rec := httptest.NewRecorder()
	require.NoError(t, h.List(echo.New().NewContext(req, rec)))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"github"`)
	require.Contains(t, rec.Body.String(), `"runner"`)
	return rec.Body.String()
}

func TestExtensionList_AcceptLanguageEN_NoCyrillic(t *testing.T) {
	for _, al := range []string{"en", "en-US,en;q=0.9", ""} {
		assert.False(t, cyrillic.MatchString(builtinListBody(t, al)), "Accept-Language %q leaked Cyrillic", al)
	}
}

func TestExtensionList_AcceptLanguageRU_HasRussian(t *testing.T) {
	assert.True(t, cyrillic.MatchString(builtinListBody(t, "ru")))
}

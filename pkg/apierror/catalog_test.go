package apierror

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var cyrillic = regexp.MustCompile(`[А-Яа-яЁё]`)

func TestCatalogEveryCodeHasEnAndRu(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Catalog {
		assert.NotEmpty(t, e.Code)
		assert.False(t, seen[e.Code], "duplicate code %q", e.Code)
		seen[e.Code] = true
		assert.NotEmpty(t, e.EN, "code %q has no en text", e.Code)
		assert.NotEmpty(t, e.RU, "code %q has no ru text", e.Code)
		assert.False(t, cyrillic.MatchString(e.EN), "code %q: en text contains Cyrillic", e.Code)
		assert.True(t, cyrillic.MatchString(e.RU), "code %q: ru text is not Russian", e.Code)
	}
}

func TestFactoriesUseCatalogedCodes(t *testing.T) {
	for _, err := range []*Error{
		BadRequest("x"), BadRequestWithDetails("x", "y"), ValidationError(nil), Unauthorized(""),
		Forbidden(""), ForbiddenWithDetails("x", "y"), NotFound("T"), NotFoundWithDetails("T", "y"),
		Conflict("x"), TooManyRequests(""), ServiceUnavailable(""), InternalError(""), Wrap(assert.AnError),
	} {
		_, ok := Lookup(err.ErrorCode, "en")
		assert.True(t, ok, "%d %q: error_code %q is not in the catalog", err.Code, err.Message, err.ErrorCode)
		assert.False(t, cyrillic.MatchString(err.Message), "message must be English: %q", err.Message)
	}
}

func TestErrorJSONKeepsNumericCode(t *testing.T) {
	b, err := json.Marshal(Forbidden(""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"code":403,"error_code":"forbidden","message":"Forbidden"}`, string(b))
}

// The web interface translates by the same codes; a code missing from either
// locale file would show the English fallback to a Russian-speaking user.
func TestWebLocalesCarryEveryCatalogCode(t *testing.T) {
	for _, lang := range []string{"en", "ru"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "locales", lang, "apiErrors.json"))
		require.NoError(t, err)
		var apiErrors map[string]string
		require.NoError(t, json.Unmarshal(raw, &apiErrors))
		for _, e := range Catalog {
			want := e.EN
			if lang == "ru" {
				want = e.RU
			}
			assert.Equal(t, want, apiErrors[e.Code], "web %s locale, code %q", lang, e.Code)
		}
		assert.Len(t, apiErrors, len(Catalog), "web %s locale has codes the catalog does not", lang)
	}
}

func TestLookup(t *testing.T) {
	en, ok := Lookup("forbidden", "en")
	require.True(t, ok)
	ru, ok := Lookup("forbidden", "ru")
	require.True(t, ok)
	assert.NotEqual(t, en, ru, "the two locales must carry different text")
	other, ok := Lookup("forbidden", "de")
	require.True(t, ok)
	assert.Equal(t, en, other, "any locale but ru gets English")
	_, ok = Lookup("no_such_code", "en")
	assert.False(t, ok, "unknown code is reported, not guessed")
}

func TestDirectErrorGetsStatusCode(t *testing.T) {
	b, err := json.Marshal(&Error{Code: http.StatusConflict, Message: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"error_code":"conflict"`) {
		t.Fatalf("direct error lacks error_code: %s", b)
	}
	b, _ = json.Marshal(&Error{Code: http.StatusConflict, ErrorCode: "cas_conflict", Message: "x"})
	if !strings.Contains(string(b), `"error_code":"cas_conflict"`) {
		t.Fatalf("explicit code overwritten: %s", b)
	}
}

func TestDirectErrorCodeByStatus(t *testing.T) {
	cases := map[int]string{
		http.StatusBadRequest:          CodeBadRequest,
		http.StatusUnprocessableEntity: CodeBadRequest,
		http.StatusUnauthorized:        CodeUnauthorized,
		http.StatusForbidden:           CodeForbidden,
		http.StatusNotFound:            CodeNotFound,
		http.StatusConflict:            CodeConflict,
		http.StatusTooManyRequests:     CodeRateLimited,
		http.StatusServiceUnavailable:  CodeServiceUnavailable,
		http.StatusInternalServerError: CodeInternalError,
		http.StatusBadGateway:          CodeInternalError,
	}
	for status, want := range cases {
		b, err := json.Marshal(&Error{Code: status, Message: "x"})
		require.NoError(t, err)
		assert.Contains(t, string(b), `"error_code":"`+want+`"`, "status %d", status)
	}
}

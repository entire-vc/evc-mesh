package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PATCH /initiatives/:id must tell an absent target_date (leave the deadline)
// from an explicit null or "" (clear it): the edit form sends null when the
// date is cleared, and a plain *string read it as "leave it".
func TestUpdateInitiativeRequest_TargetDate(t *testing.T) {
	bind := func(body string) updateInitiativeRequest {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		var out updateInitiativeRequest
		require.NoError(t, echo.New().NewContext(req, httptest.NewRecorder()).Bind(&out))
		return out
	}
	clears := func(r updateInitiativeRequest) bool {
		d, err := parseOptionalDate(r.TargetDate.value)
		require.NoError(t, err)
		return r.TargetDate.wasSet && d == nil
	}

	absent := bind(`{"name":"x"}`)
	assert.False(t, absent.TargetDate.wasSet)
	assert.False(t, clears(absent), "an absent date leaves the deadline alone")

	assert.True(t, clears(bind(`{"target_date":null}`)), "null clears the deadline")
	assert.True(t, clears(bind(`{"target_date":""}`)), `"" clears the deadline`)

	set := bind(`{"target_date":"2026-12-01"}`)
	assert.False(t, clears(set))
	d, err := parseOptionalDate(set.TargetDate.value)
	require.NoError(t, err)
	assert.Equal(t, "2026-12-01", d.Format("2006-01-02"))
}

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// The point of these is the distinction the old behaviour destroyed: a caller
// who asked for a direction the endpoint cannot honour must be told, because
// silently serving "asc" is indistinguishable from having served what they asked
// for. Both halves are pinned — refusal for a bad value AND acceptance for the
// good ones — since a guard that refuses everything looks identical to a working
// one until you show it can also say yes.
func TestRejectBadSortDir(t *testing.T) {
	for _, tc := range []struct {
		name       string
		query      string
		wantRefuse bool
		wantField  string
	}{
		{"empty is the caller declining to choose", "", false, ""},
		{"sort_dir=asc", "?sort_dir=asc", false, ""},
		{"sort_dir=desc", "?sort_dir=desc", false, ""},
		{"order=desc (the conventional spelling)", "?order=desc", false, ""},
		{"garbage sort_dir is refused and names sort_dir", "?sort_dir=sideways", true, "sort_dir"},
		// The field named in the error must be the one that actually carried the
		// bad value, or the caller fixes the wrong parameter.
		{"garbage order is refused and names order", "?order=sideways", true, "order"},
		// A valid sort_dir wins over a garbage order: sort_dir is the documented
		// name, so the request is honourable and must not be refused.
		{"valid sort_dir beside garbage order is honoured", "?sort_dir=desc&order=sideways", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/"+tc.query, http.NoBody)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			var pg pagination.Params
			if err := c.Bind(&pg); err != nil {
				t.Fatalf("bind: %v", err)
			}

			refused, err := rejectBadSortDir(c, pg)
			if tc.wantRefuse {
				if !refused {
					t.Fatal("guard must report that it refused; a caller keyed on the error alone would run on past the 400 it just wrote")
				}
				if err != nil {
					t.Fatalf("guard must write a 400 response, not return a transport error: %v", err)
				}
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("want 400, got %d (body %s)", rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), tc.wantField) {
					t.Errorf("error must name %q so the caller knows which parameter to fix; body was %s",
						tc.wantField, rec.Body.String())
				}
				return
			}
			if refused {
				t.Fatal("valid direction must not be refused")
			}
			if err != nil || rec.Body.Len() != 0 {
				t.Fatalf("valid direction must pass through untouched; err=%v body=%s", err, rec.Body.String())
			}
		})
	}
}

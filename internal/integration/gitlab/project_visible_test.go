package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// GitLab answers 404 for a missing object and for a project the token may not
// see; the project lookup is what tells them apart.
func TestProjectVisible(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		visible bool
		failed  bool
	}{
		{"readable", http.StatusOK, true, false},
		{"not found or hidden", http.StatusNotFound, false, false},
		{"forbidden", http.StatusForbidden, false, false},
		{"unauthorized", http.StatusUnauthorized, false, false},
		{"provider error is no answer", http.StatusBadGateway, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/api/v4/projects/example%2Fproduct", r.URL.EscapedPath())
				require.Equal(t, "fixture-token", r.Header.Get("PRIVATE-TOKEN"))
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			visible, err := NewClient(srv.URL, "fixture-token").ProjectVisible(context.Background(), "example/product")
			require.Equal(t, tc.failed, err != nil)
			require.Equal(t, tc.visible, visible)
		})
	}
}

func TestProjectVisibleReturnsRequestAndTransportErrors(t *testing.T) {
	_, err := NewClient("http://[::1]:namedport", "").ProjectVisible(context.Background(), "g/p")
	require.Error(t, err)
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	_, err = NewClient(srv.URL, "").ProjectVisible(context.Background(), "g/p")
	require.Error(t, err)
}

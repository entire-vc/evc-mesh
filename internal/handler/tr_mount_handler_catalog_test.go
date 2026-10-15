package handler

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Sync answers a non-2xx mount outcome with error_code = the mount status, and
// the web translates by that code. A status missing from the catalog would show
// the English server message to a Russian-speaking user.
func TestTrMountErrorStatusesAreCataloged(t *testing.T) {
	for status, code := range trMountStatusHTTP {
		if code < http.StatusBadRequest {
			continue
		}
		_, ok := apierror.Lookup(string(status), "ru")
		assert.True(t, ok, "mount status %q (HTTP %d) is not in the apierror catalog", status, code)
	}
}

func TestCheckoutConflictCodeIsCataloged(t *testing.T) {
	_, ok := apierror.Lookup("task_already_checked_out", "ru")
	assert.True(t, ok)
}

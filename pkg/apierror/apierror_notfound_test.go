package apierror

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// NotFoundWithDetails exists for the case where "not found" is a fork in the
// flow rather than a dead end — adding a member by an address that has no
// account yet needs to say "invite them, or provision one", not just "no".
func TestNotFoundWithDetails(t *testing.T) {
	err := NotFoundWithDetails("User", "send an invite link instead")

	assert.Equal(t, http.StatusNotFound, err.StatusCode())
	assert.Equal(t, "User not found", err.Message)
	assert.Equal(t, "send an invite link instead", err.Details)
	assert.Contains(t, err.Error(), "send an invite link instead",
		"the details must survive into the error string, or logs lose the actionable half")
}

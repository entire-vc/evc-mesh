package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestHumanGateDecision_IsDecision(t *testing.T) {
	decision := HumanGateDecision{ID: uuid.New()}
	assert.True(t, decision.IsDecision())

	revokedID := uuid.New()
	revocation := HumanGateDecision{ID: uuid.New(), RevokesID: &revokedID}
	assert.False(t, revocation.IsDecision())
}

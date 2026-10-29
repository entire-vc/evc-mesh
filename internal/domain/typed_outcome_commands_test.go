package domain

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMirrorResultKindLabel(t *testing.T) {
	assert.Equal(t, []string{"backend", "kind:change"},
		MirrorResultKindLabel([]string{"backend"}, ResultKindChange))
	assert.Equal(t, []string{"kind:defect", "backend"},
		MirrorResultKindLabel([]string{"kind:change", "backend", " Kind:ops"}, ResultKindDefect),
		"every earlier kind:* goes, the new one takes the first one's place")
	assert.Equal(t, []string{"kind:ops"}, MirrorResultKindLabel(nil, ResultKindOps))
}

func TestNewOutcomeError(t *testing.T) {
	var oe *OutcomeError
	require.True(t, errors.As(error(NewOutcomeError(CodeMissingEvidence, "evidence is required")), &oe))
	assert.Equal(t, CodeMissingEvidence, oe.Code)
	assert.Equal(t, "evidence is required", oe.Message)
}

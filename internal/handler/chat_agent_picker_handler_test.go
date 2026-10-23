package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func TestChatAgentPicker_RejectsBadProjectAndAgentKey(t *testing.T) {
	h := NewChatAgentPickerHandler(postgres.NewChatAgentPickerRepo(nil)) // both refusals happen before a query
	status, _ := callUserRuntime(t, h.List, http.MethodGet, "/", "", uuid.New(), uuid.Nil, map[string]string{"proj_id": "not-a-uuid"})
	require.Equal(t, http.StatusBadRequest, status)
	status, _ = callUserRuntime(t, h.List, http.MethodGet, "/", "", uuid.Nil, uuid.New(), map[string]string{"proj_id": uuid.NewString()})
	require.Equal(t, http.StatusForbidden, status)
}

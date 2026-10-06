package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestCheckoutM1_LegacyRetryStable(t *testing.T) {
	svc, repo, _, _ := setupCheckoutTaskService()
	id, owner := uuid.New(), uuid.New()
	repo.items[id] = &domain.Task{ID: id, ProjectID: uuid.New()}
	a, err := svc.CheckoutTask(agentContext(owner), id, 30, nil)
	require.NoError(t, err)
	b, err := svc.CheckoutTask(agentContext(owner), id, 30, nil)
	require.NoError(t, err)
	require.Equal(t, a.CheckoutToken, b.CheckoutToken, "retry must not rotate the live token")
}

func TestCheckoutM1_ForceAgentRejected(t *testing.T) {
	svc, repo, _, _ := setupCheckoutTaskService()
	id, owner := uuid.New(), uuid.New()
	repo.items[id] = &domain.Task{ID: id, ProjectID: uuid.New(), CheckedOutBy: &owner}
	require.Error(t, svc.ForceReleaseCheckout(agentContext(uuid.New()), id))
	require.Equal(t, &owner, repo.items[id].CheckedOutBy)
}

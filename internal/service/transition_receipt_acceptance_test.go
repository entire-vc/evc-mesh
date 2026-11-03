package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// These refusals happen before the store is touched, so no store is needed;
// a nil store would panic if any of them reached it.

func TestRecordAcceptance_RefusedBeforeStore(t *testing.T) {
	s := NewTransitionReceiptService(nil, nil)
	taskID := uuid.New()

	_, err := s.RecordAcceptance(context.Background(), taskID, domain.AcceptanceIntentRequest{})
	require.Error(t, err, "no authenticated actor")

	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
	_, err = s.RecordAcceptance(ctx, taskID, domain.AcceptanceIntentRequest{})
	require.Error(t, err, "an empty request fails validation")
}

func TestRevokeAcceptance_RefusedBeforeStore(t *testing.T) {
	s := NewTransitionReceiptService(nil, nil)

	require.Error(t, s.RevokeAcceptance(context.Background(), uuid.New(), uuid.New(), "reason"), "no authenticated actor")

	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
	require.Error(t, s.RevokeAcceptance(ctx, uuid.New(), uuid.New(), ""), "a reason is required")
}

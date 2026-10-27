package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// memExtActors keeps one actor row in memory with the same conditional
// semantics as the SQL (rotate only when not revoked, restore and mark only
// while the given hash is current). The SQL itself is covered by
// extension_token_delivery_db_test.go against Postgres.
type memExtActors struct {
	repository.ExtensionActorRepository
	row *domain.ExtensionActor
	err error
}

func (m *memExtActors) GetByConnection(context.Context, uuid.UUID) (*domain.ExtensionActor, error) {
	if m.err != nil || m.row == nil {
		return nil, m.err
	}
	c := *m.row
	return &c, nil
}

func (m *memExtActors) Rotate(_ context.Context, _ uuid.UUID, newHash string, exp time.Time) (bool, error) {
	before, err := m.RotateKeepingState(context.Background(), uuid.Nil, newHash, exp)
	return before != nil, err
}

func (m *memExtActors) RotateKeepingState(_ context.Context, _ uuid.UUID, newHash string, exp time.Time) (*domain.ExtensionActor, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.row == nil || m.row.RevokedAt != nil {
		return nil, nil
	}
	before := *m.row
	old := m.row.TokenHash
	m.row.PrevTokenHash, m.row.PrevTokenExpiresAt, m.row.TokenHash = &old, &exp, newHash
	return &before, nil
}

func (m *memExtActors) RestoreToken(_ context.Context, _ uuid.UUID, newHash string, before *domain.ExtensionActor) (bool, error) {
	if m.row.TokenHash != newHash {
		return false, nil
	}
	m.row.TokenHash, m.row.PrevTokenHash, m.row.PrevTokenExpiresAt = before.TokenHash, before.PrevTokenHash, before.PrevTokenExpiresAt
	return true, nil
}

func (m *memExtActors) MarkTokenDelivered(_ context.Context, _ uuid.UUID, hash string, at time.Time) (bool, error) {
	if m.err != nil {
		return false, m.err
	}
	if m.row == nil || m.row.TokenHash != hash {
		return false, nil
	}
	m.row.TokenDeliveredAt = &at
	return true, nil
}

func TestExtensionActorService_RotateForDeliveryAndRestore(t *testing.T) {
	ctx := context.Background()
	ws, conn := uuid.New(), uuid.New()
	repo := &memExtActors{row: &domain.ExtensionActor{ConnectionID: conn, WorkspaceID: ws, ExtensionID: "hello", TokenHash: extensionTokenHash("ext_old")}}
	svc := NewExtensionActorService(repo, nil, nil)

	at, err := svc.TokenDeliveredAt(ctx, conn)
	require.NoError(t, err)
	assert.Nil(t, at)

	tok, restore, err := svc.RotateForDelivery(ctx, ws, conn)
	require.NoError(t, err)
	require.NotNil(t, restore)
	assert.Equal(t, extensionTokenHash(tok), repo.row.TokenHash)
	assert.Equal(t, extensionTokenHash("ext_old"), *repo.row.PrevTokenHash)

	now := time.Now()
	require.NoError(t, svc.MarkTokenDelivered(ctx, conn, tok, now))
	at, err = svc.TokenDeliveredAt(ctx, conn)
	require.NoError(t, err)
	assert.True(t, now.Equal(*at))

	require.NoError(t, restore(ctx))
	assert.Equal(t, extensionTokenHash("ext_old"), repo.row.TokenHash, "restored")
	assert.Nil(t, repo.row.PrevTokenHash)

	var ae *apierror.Error
	require.ErrorAs(t, svc.MarkTokenDelivered(ctx, conn, tok, now), &ae, "no longer current")
	assert.Equal(t, http.StatusConflict, ae.Code)
}

func TestExtensionActorService_RotateForDeliveryRefusals(t *testing.T) {
	ctx := context.Background()
	ws, conn := uuid.New(), uuid.New()
	repo := &memExtActors{}
	svc := NewExtensionActorService(repo, nil, nil)
	status := func(err error) int {
		var ae *apierror.Error
		require.ErrorAs(t, err, &ae)
		return ae.Code
	}

	_, _, err := svc.RotateForDelivery(ctx, ws, conn)
	assert.Equal(t, http.StatusNotFound, status(err), "no actor and no consents")

	repo.row = &domain.ExtensionActor{ConnectionID: conn, WorkspaceID: uuid.New(), TokenHash: "h"}
	_, _, err = svc.RotateForDelivery(ctx, ws, conn)
	assert.Equal(t, http.StatusNotFound, status(err), "other workspace")

	revoked := time.Now()
	repo.row = &domain.ExtensionActor{ConnectionID: conn, WorkspaceID: ws, TokenHash: "h", RevokedAt: &revoked}
	_, _, err = svc.RotateForDelivery(ctx, ws, conn)
	assert.Equal(t, http.StatusConflict, status(err))
	_, err = svc.Rotate(ctx, ws, conn)
	assert.Equal(t, http.StatusConflict, status(err))

	repo.err = errors.New("db down")
	_, _, err = svc.RotateForDelivery(ctx, ws, conn)
	assert.EqualError(t, err, "db down")
	_, err = svc.TokenDeliveredAt(ctx, conn)
	assert.EqualError(t, err, "db down")
	assert.EqualError(t, svc.MarkTokenDelivered(ctx, conn, "ext_x", time.Now()), "db down")
}

// revokedBetween is an actor that gets revoked between the read and the
// rotation: the rotation reports nothing replaced.
type revokedBetween struct{ memExtActors }

func (r *revokedBetween) RotateKeepingState(context.Context, uuid.UUID, string, time.Time) (*domain.ExtensionActor, error) {
	return nil, nil
}

func (r *revokedBetween) Rotate(context.Context, uuid.UUID, string, time.Time) (bool, error) {
	return false, nil
}

func TestExtensionActorService_RotateRacingRevoke(t *testing.T) {
	ctx := context.Background()
	ws, conn := uuid.New(), uuid.New()
	repo := &revokedBetween{memExtActors{row: &domain.ExtensionActor{ConnectionID: conn, WorkspaceID: ws, TokenHash: "h"}}}
	svc := NewExtensionActorService(repo, nil, nil)
	_, _, err := svc.RotateForDelivery(ctx, ws, conn)
	assert.ErrorContains(t, err, "revoked")
	_, err = svc.Rotate(ctx, ws, conn)
	assert.ErrorContains(t, err, "revoked")
}

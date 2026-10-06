package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	pg "github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func mockLease(t *domain.Task, changed bool) *domain.CheckoutLease {
	return &domain.CheckoutLease{Holder: t.CheckedOutBy, Token: t.CheckoutToken, ExpiresAt: t.CheckoutExpires, AcquiredAt: t.CheckoutAcquiredAt, SessionID: t.CheckoutSessionID, RequestID: t.CheckoutRequestID, Generation: t.CheckoutGeneration, Changed: changed}
}

func sameUUID(a, b *uuid.UUID) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func (m *MockTaskRepository) AcquireCheckout(_ context.Context, id, holder, token uuid.UUID, expiry time.Time, scope domain.CheckoutScope) (*domain.CheckoutLease, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.items[id]
	if t == nil {
		return nil, pg.ErrCheckoutConflict
	}
	if t.CheckedOutBy != nil && (t.CheckoutExpires == nil || t.CheckoutExpires.After(timeNow())) {
		if *t.CheckedOutBy == holder && sameUUID(t.CheckoutSessionID, scope.SessionID) && sameUUID(t.CheckoutRequestID, scope.RequestID) {
			if scope.SessionID == nil {
				t.CheckoutExpires = &expiry
			}
			return mockLease(t, false), nil
		}
		return nil, pg.ErrCheckoutConflict
	}
	if scope.RequestID != nil && sameUUID(scope.RequestID, t.CheckoutRequestID) && sameUUID(scope.SessionID, t.CheckoutSessionID) {
		return nil, pg.ErrCheckoutConflict
	}
	now := timeNow()
	t.CheckedOutBy, t.CheckoutToken, t.CheckoutExpires, t.CheckoutAcquiredAt = &holder, &token, &expiry, &now
	t.CheckoutSessionID, t.CheckoutRequestID = scope.SessionID, scope.RequestID
	t.CheckoutGeneration++
	return mockLease(t, true), nil
}

func mockExpectationMatches(t *domain.Task, e domain.CheckoutExpectation) bool {
	if e.Token != nil {
		return sameUUID(t.CheckoutToken, e.Token)
	}
	return sameUUID(t.CheckedOutBy, e.Holder) && (e.Generation > 0 && t.CheckoutGeneration == e.Generation && sameUUID(t.CheckoutSessionID, e.SessionID) || e.Legacy && e.Generation == 0 && e.SessionID == nil && t.CheckoutSessionID == nil)
}

func (m *MockTaskRepository) CompareReleaseCheckout(_ context.Context, id uuid.UUID, e domain.CheckoutExpectation) (*domain.CheckoutLease, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.items[id]
	if t == nil || t.CheckedOutBy == nil {
		return nil, nil
	}
	if !mockExpectationMatches(t, e) {
		return nil, pg.ErrInvalidCheckoutToken
	}
	lease := mockLease(t, true)
	t.CheckedOutBy, t.CheckoutToken, t.CheckoutExpires, t.CheckoutAcquiredAt = nil, nil, nil, nil
	return lease, nil
}

func (m *MockTaskRepository) ExtendScopedCheckout(_ context.Context, id uuid.UUID, e domain.CheckoutExpectation, expiry time.Time) (*domain.CheckoutLease, error) {
	if m.errToReturn != nil {
		return nil, m.errToReturn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.items[id]
	if t == nil || t.CheckedOutBy == nil || t.CheckoutExpires == nil || !t.CheckoutExpires.After(timeNow()) || !mockExpectationMatches(t, e) {
		return nil, pg.ErrInvalidCheckoutToken
	}
	t.CheckoutExpires = &expiry
	return mockLease(t, true), nil
}

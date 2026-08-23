package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// AC (#c9055b2c) #2: "тест, фиксирующий свойство: обе ветки вызывают bcrypt.
// Проверять на факте вызова (счётчик/подменный хешер), а не на времени —
// тест на таймингах будет флакать в CI."
//
// This test replaces the package-level bcryptCompare var with a recording
// fake for its duration, drives Login down BOTH branches (user found /
// user not found), and asserts on the FACT of the call — how many times it
// ran and what hash each call compared against — never on elapsed time.
//
// MUTATION CONTROL (verified live, see task report): remove the
// `_ = bcryptCompare(dummyPasswordHash, []byte(password))` line (and its
// containing statement) from Login's `if user == nil` branch, leaving only
// `return nil, nil, ErrInvalidCredentials`. TestLogin_BothBranches_CallBcryptCompare
// then fails at the "nonexistent-account branch must call bcryptCompare
// exactly once" assertion (calls == 0, hash never recorded) — proving this
// test actually exercises the fix and does not just pass by construction.
// ---------------------------------------------------------------------------

// recordingBcryptCompare is a test double for bcryptCompare (see service.go)
// that records every (hashedPassword, password) pair it was called with and
// always returns the same canned error, without spending any real bcrypt
// CPU — the "fact of the call" is what this test checks, not its cost.
type recordingBcryptCompare struct {
	calls []recordedBcryptCall
}

type recordedBcryptCall struct {
	hash     string
	password string
}

func (r *recordingBcryptCompare) compare(hashedPassword, password []byte) error {
	r.calls = append(r.calls, recordedBcryptCall{hash: string(hashedPassword), password: string(password)})
	// Always "fail" — this double never needs to let a login through; the
	// tests below only assert on invocation, not on outcome.
	return errWrongPasswordStub
}

// errWrongPasswordStub is a lightweight stand-in for bcrypt's own
// ErrMismatchedHashAndPassword — any non-nil error takes the same
// ErrInvalidCredentials path in Login, so the exact error type doesn't matter here.
var errWrongPasswordStub = assertErr("stub: bcrypt mismatch")

type assertErr string

func (e assertErr) Error() string { return string(e) }

// withRecordingBcrypt swaps the package-level bcryptCompare for a fresh
// recordingBcryptCompare for the duration of the test and restores the real
// bcrypt.CompareHashAndPassword on cleanup — tests never leak a fake
// implementation into any test that runs after them.
func withRecordingBcrypt(t *testing.T) *recordingBcryptCompare {
	t.Helper()
	rec := &recordingBcryptCompare{}
	original := bcryptCompare
	bcryptCompare = rec.compare
	t.Cleanup(func() { bcryptCompare = original })
	return rec
}

func TestLogin_BothBranches_CallBcryptCompare(t *testing.T) {
	svc, userRepo, _, _, _ := newTestService()

	// Seed a real, found user directly in the repo (bypassing Register, which
	// would itself call bcrypt.GenerateFromPassword — a different function,
	// irrelevant to this test and not worth recording). PasswordHash is a
	// deliberately-fake literal, not a real bcrypt hash: bcryptCompare is
	// mocked for this whole test, so nothing ever parses it as one.
	const foundUserHash = "$2a$10$stub.hash.for.a.found.user.only"
	seedUser := &domain.User{
		ID:           uuid.New(),
		Email:        "found-branch@example.com",
		PasswordHash: foundUserHash,
		Name:         "Found Branch User",
		Username:     "found-branch-user",
		IsActive:     true,
	}
	require.NoError(t, userRepo.Create(context.Background(), seedUser))

	rec := withRecordingBcrypt(t)

	// Branch 1: user == nil (no account at this email).
	_, _, err := svc.Login(context.Background(), "nonexistent-branch@example.com", "SomePassword1")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidCredentials)

	require.Len(t, rec.calls, 1,
		"nonexistent-account branch must call bcryptCompare exactly once — that IS the fix; "+
			"zero calls means Login short-circuited before paying the same cost as a real account")
	assert.Equal(t, string(dummyPasswordHash), rec.calls[0].hash,
		"nonexistent-account branch must compare against the fixed dummyPasswordHash, not an empty/zero hash")
	assert.Equal(t, "SomePassword1", rec.calls[0].password)

	// Branch 2: user found, wrong password.
	_, _, err = svc.Login(context.Background(), seedUser.Email, "SomeOtherPassword1")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidCredentials)

	require.Len(t, rec.calls, 2, "found-user branch must also call bcryptCompare exactly once")
	assert.Equal(t, foundUserHash, rec.calls[1].hash,
		"found-user branch must compare against the REAL stored hash, not the dummy one")
	assert.Equal(t, "SomeOtherPassword1", rec.calls[1].password)
}

// TestLogin_DummyHash_SameCostAsProductionHashes pins the cost of
// dummyPasswordHash to bcryptCost — the same constant Register uses via
// bcrypt.GenerateFromPassword(..., bcryptCost) for every real user. A dummy
// hash generated at a cheaper cost would reopen the timing channel for the
// nonexistent-account branch (cheaper bcrypt compare = faster response),
// just with a smaller gap — this test is what would catch that regression.
func TestLogin_DummyHash_SameCostAsProductionHashes(t *testing.T) {
	cost, err := bcrypt.Cost(dummyPasswordHash)
	require.NoError(t, err)
	assert.Equal(t, bcryptCost, cost,
		"dummyPasswordHash must be generated at exactly bcryptCost — the same cost as every real user row — "+
			"or the nonexistent-account branch stays measurably cheaper than a real login")
}

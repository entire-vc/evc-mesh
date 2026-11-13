package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/catalog"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type stubMover struct {
	calls     int
	err       error
	planScope string
	last      catalog.MoveRequest
}

func (m *stubMover) PlanMoveToCatalog(_ context.Context, _, _, scope string) (*catalog.MovePlan, error) {
	m.calls++
	m.planScope = scope
	if m.err != nil {
		return nil, m.err
	}
	return &catalog.MovePlan{OneWay: true}, nil
}

func (m *stubMover) MoveToCatalog(_ context.Context, _ uuid.UUID, _ string, req catalog.MoveRequest) (*catalog.MoveResult, error) {
	m.calls++
	m.last = req
	if m.err != nil {
		return nil, m.err
	}
	return &catalog.MoveResult{Outcome: catalog.OutcomeMoved}, nil
}

// The admin path of the four routes: disabled mover, unreadable body, the body
// reaching the mover trimmed, and a refusal keeping its status.
func TestMoveAndRepinRoutes_AdminPaths(t *testing.T) {
	off := newRepoFixture(t, true)
	assert.Equal(t, http.StatusNotImplemented, repoCall(t, off.h.PlanMoveToCatalog, `{}`, "hello", false).Code, "no mover wired")
	assert.Equal(t, http.StatusNotImplemented, repoCall(t, off.h.MoveToCatalog, `{}`, "hello", false).Code)

	f := newRepoFixture(t, true)
	mover := &stubMover{}
	f.h.WithCatalogMove(mover)
	for name, h := range map[string]echo.HandlerFunc{
		"move-plan": f.h.PlanMoveToCatalog, "move": f.h.MoveToCatalog, "repin-plan": f.h.PlanRepin, "repin": f.h.Repin,
	} {
		assert.Equal(t, http.StatusBadRequest, repoCall(t, h, `{`, "hello", false).Code, name)
	}
	assert.Zero(t, mover.calls, "an unreadable body never reaches the mover")

	rec := repoCall(t, f.h.MoveToCatalog, `{"expected_version":" 0.1.0 ","expected_commit_sha":"a","target_version":"0.2.0 ","target_commit_sha":" b","consent_commit_sha":"b"}`, "hello", false)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"moved"`)
	assert.Equal(t, catalog.MoveRequest{ExpectedVersion: "0.1.0", ExpectedCommitSHA: "a", TargetVersion: "0.2.0", TargetCommitSHA: "b", ConsentCommitSHA: "b"}, mover.last)

	mover.err = apierror.Conflict("stale")
	assert.Equal(t, http.StatusConflict, repoCall(t, f.h.MoveToCatalog, `{}`, "hello", false).Code)
	assert.Equal(t, http.StatusConflict, repoCall(t, f.h.PlanMoveToCatalog, `{}`, "hello", false).Code)

	// Re-pin refusals come from the service with their own status.
	assert.Equal(t, http.StatusNotFound, repoCall(t, f.h.PlanRepin, `{"ref":"main"}`, "missing", false).Code)
	assert.Equal(t, http.StatusBadRequest, repoCall(t, f.h.Repin, `{"ref":"main"}`, "hello", false).Code, "expected state is required")
	assert.Empty(t, f.repo.repins)
}

// target_scope reaches the plan and the apply: the move accepts an exact
// scoped version as its target.
func TestMoveRoutesPassTargetScope(t *testing.T) {
	f := newRepoFixture(t, true)
	mover := &stubMover{}
	f.h.WithCatalogMove(mover)
	require.Equal(t, http.StatusOK, repoCall(t, f.h.PlanMoveToCatalog, `{"target_version":"0.4.0","target_scope":" scoped "}`, "hello", false).Code)
	assert.Equal(t, "scoped", mover.planScope)
	rec := repoCall(t, f.h.MoveToCatalog, `{"target_version":"0.4.0","target_commit_sha":"c","target_scope":"scoped","expected_version":"0.4.0","expected_commit_sha":"r"}`, "hello", false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "scoped", mover.last.TargetScope)
	assert.Equal(t, "0.4.0", mover.last.TargetVersion)
}

func TestMoveAndRepinRoutesAreInstanceAdminOnly(t *testing.T) {
	f := newRepoFixture(t, false)
	mover := &stubMover{}
	f.h.WithCatalogMove(mover)
	for name, h := range map[string]echo.HandlerFunc{
		"move-plan": f.h.PlanMoveToCatalog, "move": f.h.MoveToCatalog, "repin-plan": f.h.PlanRepin, "repin": f.h.Repin,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, http.StatusForbidden, repoCall(t, h, `{"ref":"main"}`, "hello", false).Code)
			assert.Equal(t, http.StatusForbidden, repoCall(t, h, `{"ref":"main"}`, "hello", true).Code, "agents are refused too")
		})
	}
	assert.Zero(t, mover.calls)
	assert.Empty(t, f.repo.repins)

	// Control: an admin reaches the mover.
	admin := newRepoFixture(t, true)
	admin.h.WithCatalogMove(mover)
	assert.Equal(t, http.StatusOK, repoCall(t, admin.h.PlanMoveToCatalog, `{}`, "hello", false).Code)
	assert.Equal(t, 1, mover.calls)
}

// idemMem is an in-memory Idempotency-Key store with the postgres contract.
type idemMem struct {
	mu   sync.Mutex
	recs map[string]*domain.IdempotencyRecord
}

func (s *idemMem) key(sc domain.IdempotencyScope) string {
	return sc.ActorID.String() + sc.Method + sc.Path + sc.Key
}

func (s *idemMem) Claim(_ context.Context, sc domain.IdempotencyScope, hash string) (*domain.IdempotencyRecord, uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.recs[s.key(sc)]; ok {
		return r, uuid.Nil, nil
	}
	s.recs[s.key(sc)] = &domain.IdempotencyRecord{RequestHash: hash, State: domain.IdempotencyInFlight}
	return nil, uuid.New(), nil
}

func (s *idemMem) Complete(_ context.Context, sc domain.IdempotencyScope, _ uuid.UUID, status int, ct string, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.recs[s.key(sc)]
	r.State, r.ResponseStatus, r.ResponseType, r.ResponseBody = domain.IdempotencyDone, &status, &ct, append([]byte(nil), body...)
	return nil
}

func (s *idemMem) Release(_ context.Context, sc domain.IdempotencyScope, _ uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.recs, s.key(sc))
	return nil
}

// A retried re-pin after a lost answer, with the same Idempotency-Key, acts
// once (one journal entry) and replays the first answer, which never carries
// the token.
func TestRepinRetryWithIdempotencyKeyActsOnce(t *testing.T) {
	f := newRepoFixture(t, true)
	sha1, sha2 := strings.Repeat("1", 40), strings.Repeat("2", 40)
	f.publish(t, "main", sha1, "0.3.0")
	f.publish(t, sha2, sha2, "0.3.1")
	f.repo.rows["hello"] = &domain.InstalledExtension{ExtensionID: "hello", Source: domain.ExtensionSourceRepo, Version: "0.3.0",
		RepoURL: f.url, RepoRef: "main", CommitSHA: sha1, Manifest: []byte(repoManifest(t, "0.3.0")), TrustLevel: domain.ExtensionTrustCustom}

	h := middleware.Idempotency(&idemMem{recs: map[string]*domain.IdempotencyRecord{}}, 1<<20)(f.h.Repin)
	user := uuid.New()
	body := `{"ref":"` + sha2 + `","auth_token":"glpat-very-secret","expected_version":"0.3.0","expected_commit_sha":"` + sha1 + `","expected_target_commit_sha":"` + sha2 + `"}`
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/extensions/hello/repin", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(middleware.HeaderIdempotencyKey, "repin-hello-1")
		req = req.WithContext(actorctx.WithActor(req.Context(), user, domain.ActorTypeUser))
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.SetParamNames("ext_id")
		c.SetParamValues("hello")
		c.Set(middleware.ContextKeyUserID, user)
		require.NoError(t, h(c))
		return rec
	}
	first := call()
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	assert.Contains(t, first.Body.String(), `"outcome":"repinned"`)
	assert.NotContains(t, first.Body.String(), "glpat-very-secret")

	second := call()
	assert.Equal(t, http.StatusOK, second.Code)
	assert.Equal(t, "true", second.Header().Get(middleware.HeaderIdempotentReplayed))
	assert.JSONEq(t, first.Body.String(), second.Body.String(), "the retry gets what happened the first time")
	assert.Len(t, f.repo.repins, 1, "one journal entry")
	assert.NotContains(t, string(f.repo.repins[0].Details), "glpat-very-secret")
}

// A retried move after a lost answer, with the same Idempotency-Key, reaches
// the mover once (one apply, so one journal entry) and replays the first
// answer; another key acts again. The route carries this middleware in
// cmd/api/main.go.
func TestMoveRetryWithIdempotencyKeyActsOnce(t *testing.T) {
	f := newRepoFixture(t, true)
	mover := &stubMover{}
	f.h.WithCatalogMove(mover)
	h := middleware.Idempotency(&idemMem{recs: map[string]*domain.IdempotencyRecord{}}, 1<<20)(f.h.MoveToCatalog)
	user := uuid.New()
	body := `{"expected_version":"0.1.0","expected_commit_sha":"a","target_version":"0.2.0","target_commit_sha":"b"}`
	call := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/extensions/hello/move-to-catalog", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(middleware.HeaderIdempotencyKey, key)
		req = req.WithContext(actorctx.WithActor(req.Context(), user, domain.ActorTypeUser))
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.SetParamNames("ext_id")
		c.SetParamValues("hello")
		c.Set(middleware.ContextKeyUserID, user)
		require.NoError(t, h(c))
		return rec
	}
	first := call("move-hello-1")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	assert.Contains(t, first.Body.String(), `"moved"`)

	second := call("move-hello-1")
	assert.Equal(t, http.StatusOK, second.Code)
	assert.Equal(t, "true", second.Header().Get(middleware.HeaderIdempotentReplayed))
	assert.JSONEq(t, first.Body.String(), second.Body.String(), "the retry gets what happened the first time")
	assert.Equal(t, 1, mover.calls, "the retry never reaches the mover again")

	// Control: another key is another request.
	require.Equal(t, http.StatusOK, call("move-hello-2").Code)
	assert.Equal(t, 2, mover.calls)
}

package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// ---------------------------------------------------------------------------
// Test doubles.
// ---------------------------------------------------------------------------

// stubVCSLinkService captures the GitHubWebhookEvent passed by the handler.
type stubVCSLinkService struct {
	mu              sync.Mutex
	handleCalls     []service.GitHubWebhookEvent
	handleResult    service.PRHandleResult
	handleErr       error
	createCalls     []domain.CreateVCSLinkInput
	createReturn    *domain.VCSLink
	createReturnErr error
	// createReturnCreated defaults to true (a fresh insert) so existing tests
	// that don't care about the upsert-vs-insert distinction keep observing
	// the historical 201. Set false to simulate the upsert-onto-existing-row
	// branch (#b73171fa).
	createReturnCreated *bool
	listReturn          []domain.VCSLink
	listReturnErr       error
	deleteReturnErr     error
	resolveCalls        [][]service.TaskRefSource
	resolveTaskID       uuid.UUID
	resolveRef          service.TaskRef
}

func (s *stubVCSLinkService) Create(_ context.Context, input domain.CreateVCSLinkInput) (*domain.VCSLink, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createCalls = append(s.createCalls, input)
	created := true
	if s.createReturnCreated != nil {
		created = *s.createReturnCreated
	}
	if s.createReturn != nil {
		return s.createReturn, created, s.createReturnErr
	}
	link := &domain.VCSLink{ID: uuid.New(), TaskID: input.TaskID}
	return link, created, s.createReturnErr
}

func (s *stubVCSLinkService) GetByID(_ context.Context, _ uuid.UUID) (*domain.VCSLink, error) {
	return nil, nil
}

func (s *stubVCSLinkService) Delete(_ context.Context, _ uuid.UUID) error {
	return s.deleteReturnErr
}

func (s *stubVCSLinkService) ListByTask(_ context.Context, _ uuid.UUID) ([]domain.VCSLink, error) {
	return s.listReturn, s.listReturnErr
}

// ResolveTaskRef records what the push path asked about and answers with a
// canned task, so a handler test can assert the branch and message actually
// reached the resolver.
func (s *stubVCSLinkService) ResolveTaskRef(_ context.Context, sources ...service.TaskRefSource) (uuid.UUID, service.TaskRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolveCalls = append(s.resolveCalls, sources)
	return s.resolveTaskID, s.resolveRef
}

func (s *stubVCSLinkService) lastResolveSources() ([]service.TaskRefSource, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.resolveCalls) == 0 {
		return nil, false
	}
	return s.resolveCalls[len(s.resolveCalls)-1], true
}

func (s *stubVCSLinkService) HandleGitHubPullRequestEvent(_ context.Context, ev service.GitHubWebhookEvent) (service.PRHandleResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handleCalls = append(s.handleCalls, ev)
	return s.handleResult, s.handleErr
}

func (s *stubVCSLinkService) lastHandleEvent() (service.GitHubWebhookEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.handleCalls) == 0 {
		return service.GitHubWebhookEvent{}, false
	}
	return s.handleCalls[len(s.handleCalls)-1], true
}

// memDedupStore is an in-memory WebhookDedupStore used in handler tests so
// no Redis is required.
type memDedupStore struct {
	mu      sync.Mutex
	seen    map[string]struct{}
	failErr error
}

func newMemDedupStore() *memDedupStore {
	return &memDedupStore{seen: map[string]struct{}{}}
}

func (m *memDedupStore) Claim(_ context.Context, id string) (bool, error) {
	if m.failErr != nil {
		return false, m.failErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.seen[id]; ok {
		return false, nil
	}
	m.seen[id] = struct{}{}
	return true, nil
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

func newPullRequestRequest(t *testing.T, body []byte, deliveryID, secret string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-GitHub-Event", "pull_request")
	if deliveryID != "" {
		req.Header.Set("X-GitHub-Delivery", deliveryID)
	}
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return req
}

func newPullRequestPayload(t *testing.T, action string, prNumber int, title, body string, merged bool, mergeSHA string) []byte {
	t.Helper()
	payload := map[string]any{
		"action": action,
		"pull_request": map[string]any{
			"number":           prNumber,
			"title":            title,
			"body":             body,
			"html_url":         "https://github.com/example/repo/pull/" + itoaTest(prNumber),
			"state":            "closed",
			"merged":           merged,
			"merge_commit_sha": mergeSHA,
		},
		"repository": map[string]any{
			"full_name": "example/repo",
			"html_url":  "https://github.com/example/repo",
		},
	}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return b
}

// itoaTest is a tiny int-to-string helper local to tests so we don't rely on
// strconv import noise.
func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// ---------------------------------------------------------------------------
// Tests.
// ---------------------------------------------------------------------------

// Test 10 (spec): missing X-GitHub-Delivery header → 400.
func TestGitHubWebhook_MissingDeliveryHeader_400(t *testing.T) {
	svc := &stubVCSLinkService{}
	dedup := newMemDedupStore()
	h := NewVCSLinkHandler(svc, WithWebhookDedupStore(dedup))

	body := newPullRequestPayload(t, "closed", 42, "MESH-"+uuid.New().String(), "", true, "abc1234567")
	req := newPullRequestRequest(t, body, "", "")

	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)

	require.NoError(t, h.GitHubWebhook(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, svc.handleCalls, "service must not be invoked when X-GitHub-Delivery is missing")
}

// Test 7 (spec): invalid HMAC signature → 401.
func TestGitHubWebhook_InvalidHMAC_401(t *testing.T) {
	svc := &stubVCSLinkService{}
	dedup := newMemDedupStore()
	const secret = "supersecret"
	h := NewVCSLinkHandler(svc, WithGitHubWebhookSecret(secret), WithWebhookDedupStore(dedup))

	body := newPullRequestPayload(t, "closed", 7, "MESH-"+uuid.New().String(), "", true, "deadbee")
	// Build request with WRONG secret so signature mismatches.
	req := newPullRequestRequest(t, body, "delivery-7", "wrong-secret")

	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)

	require.NoError(t, h.GitHubWebhook(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, svc.handleCalls, "service must not be invoked when HMAC fails")
}

// Test 6 (spec): duplicate X-GitHub-Delivery → 200 with status=duplicate,
// short-circuit BEFORE HMAC verification (so even a totally invalid signature
// is acceptable on the duplicate path).
func TestGitHubWebhook_DuplicateDelivery_ShortCircuits(t *testing.T) {
	svc := &stubVCSLinkService{}
	dedup := newMemDedupStore()
	const secret = "supersecret"
	h := NewVCSLinkHandler(svc, WithGitHubWebhookSecret(secret), WithWebhookDedupStore(dedup))

	// Pre-claim the delivery id so the second send below sees it as a duplicate.
	_, _ = dedup.Claim(context.Background(), "delivery-dup")

	body := newPullRequestPayload(t, "closed", 99, "MESH-"+uuid.New().String(), "", true, "1234567")
	// No signature header at all — still must respond 200 duplicate since
	// dedup short-circuits before HMAC.
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "delivery-dup")

	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)

	require.NoError(t, h.GitHubWebhook(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "duplicate", resp["status"])
	assert.Empty(t, svc.handleCalls, "service must not be invoked for duplicates")
}

// TestGitHubWebhook_PullRequest_DelegatesToService asserts the handler
// extracts every field the orchestrator needs (action, number, title, body,
// merged flag, merge_commit_sha) and passes them through.
func TestGitHubWebhook_PullRequest_DelegatesToService(t *testing.T) {
	svc := &stubVCSLinkService{
		handleResult: service.PRHandleResult{
			TaskID:       uuid.New(),
			OldStatus:    "in_progress",
			NewStatus:    "review",
			Transitioned: true,
			Reason:       "transitioned",
		},
	}
	dedup := newMemDedupStore()
	h := NewVCSLinkHandler(svc, WithWebhookDedupStore(dedup))

	taskID := uuid.New()
	body := newPullRequestPayload(t, "closed", 123, "MESH-"+taskID.String(), "body-text", true, "deadbeefcafef00d1234567")
	req := newPullRequestRequest(t, body, "delivery-pass-through", "")

	rec := httptest.NewRecorder()
	e := echo.New()
	c := e.NewContext(req, rec)

	require.NoError(t, h.GitHubWebhook(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	ev, ok := svc.lastHandleEvent()
	require.True(t, ok, "service.HandleGitHubPullRequestEvent should have been called")
	assert.Equal(t, "closed", ev.Action)
	assert.Equal(t, 123, ev.PRNumber)
	assert.Equal(t, "MESH-"+taskID.String(), ev.PRTitle)
	assert.Equal(t, "body-text", ev.PRBody)
	assert.True(t, ev.PRMerged)
	assert.Equal(t, "deadbeefcafef00d1234567", ev.MergeSHA)
	assert.Equal(t, "example/repo", ev.Repository)
}

// TestGitHubWebhook_PullRequest_ServiceErrorReturns200Logged guards the
// behaviour that webhook senders should not see a 5xx when the orchestrator
// hits a transient DB error — we log and ack instead, so GitHub does not
// retry-storm us.
func TestGitHubWebhook_PullRequest_ServiceErrorReturns200Logged(t *testing.T) {
	svc := &stubVCSLinkService{
		handleErr: errors.New("db down"),
	}
	dedup := newMemDedupStore()
	h := NewVCSLinkHandler(svc, WithWebhookDedupStore(dedup))

	body := newPullRequestPayload(t, "closed", 42, "MESH-"+uuid.New().String(), "", true, "abc1234")
	req := newPullRequestRequest(t, body, "delivery-svc-err", "")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	require.NoError(t, h.GitHubWebhook(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestGitHubWebhook_NoSignatureWhenSecretConfigured covers the existing 401
// behaviour for a request that has X-GitHub-Delivery but no signature at all.
func TestGitHubWebhook_NoSignatureWhenSecretConfigured(t *testing.T) {
	svc := &stubVCSLinkService{}
	dedup := newMemDedupStore()
	h := NewVCSLinkHandler(svc, WithGitHubWebhookSecret("s3cret"), WithWebhookDedupStore(dedup))

	body := newPullRequestPayload(t, "closed", 1, "MESH-"+uuid.New().String(), "", false, "")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "delivery-no-sig")

	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)

	require.NoError(t, h.GitHubWebhook(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, svc.handleCalls)
}

// ---------------------------------------------------------------------------
// Create — link_type spelling.
// ---------------------------------------------------------------------------

func postVCSLink(t *testing.T, svc *stubVCSLinkService, taskID uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()

	h := NewVCSLinkHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetPath("/tasks/:task_id/vcs-links")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())

	require.NoError(t, h.Create(c))
	return rec
}

// A pull request has an obvious spelling and a canonical one. Both are
// accepted, and both are stored as the canonical "pr" — consumers join on the
// stored value, so a second spelling in the column would silently split the
// data in two.
func TestVCSLinkHandler_Create_LinkTypeSpellings(t *testing.T) {
	accepted := []struct {
		name  string
		given string
		want  domain.VCSLinkType
	}{
		{"canonical pr", "pr", domain.VCSLinkTypePR},
		{"pull_request alias", "pull_request", domain.VCSLinkTypePR},
		{"uppercase canonical", "PR", domain.VCSLinkTypePR},
		{"mixed-case alias", "Pull_Request", domain.VCSLinkTypePR},
		{"commit", "commit", domain.VCSLinkTypeCommit},
		{"branch", "branch", domain.VCSLinkTypeBranch},
	}

	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			taskID := uuid.New()
			svc := &stubVCSLinkService{}
			body := `{"link_type":"` + tc.given + `","external_id":"42","url":"https://github.com/o/r/pull/42"}`

			rec := postVCSLink(t, svc, taskID, body)

			assert.Equal(t, http.StatusCreated, rec.Code)
			require.Len(t, svc.createCalls, 1)
			assert.Equal(t, tc.want, svc.createCalls[0].LinkType)
			assert.Equal(t, taskID, svc.createCalls[0].TaskID)
		})
	}
}

func TestVCSLinkHandler_Create_RejectsUnknownLinkType(t *testing.T) {
	taskID := uuid.New()
	svc := &stubVCSLinkService{}
	body := `{"link_type":"merge_request","external_id":"42","url":"https://example.com/x"}`

	rec := postVCSLink(t, svc, taskID, body)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, svc.createCalls)

	// The rejection names the accepted set; the previous message left the
	// caller to guess which spelling was wanted.
	respBody := rec.Body.String()
	assert.Contains(t, respBody, "merge_request")
	for _, name := range domain.VCSLinkTypeNames {
		assert.Contains(t, respBody, name)
	}
}

func TestVCSLinkHandler_Create_RequiresLinkType(t *testing.T) {
	taskID := uuid.New()
	svc := &stubVCSLinkService{}
	body := `{"external_id":"42","url":"https://github.com/o/r/pull/42"}`

	rec := postVCSLink(t, svc, taskID, body)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "link_type is required")
	assert.Empty(t, svc.createCalls)
}

// ---------------------------------------------------------------------------
// Create — status field (#df734dd9: add_vcs_link had no way to record a PR's
// status at link time, so a link to an already-merged PR sat blocked
// forever since no webhook fires for a merge that predates the link).
// ---------------------------------------------------------------------------

func TestVCSLinkHandler_Create_AcceptsExplicitStatus(t *testing.T) {
	accepted := []struct {
		name  string
		given string
		want  domain.VCSLinkStatus
	}{
		{"open", "open", domain.VCSLinkStatusOpen},
		{"merged", "merged", domain.VCSLinkStatusMerged},
		{"closed", "closed", domain.VCSLinkStatusClosed},
		{"uppercase", "MERGED", domain.VCSLinkStatusMerged},
	}

	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			taskID := uuid.New()
			svc := &stubVCSLinkService{}
			body := `{"link_type":"pr","external_id":"40","url":"https://github.com/entire-vc/evc-mesh-mcp/pull/40","status":"` + tc.given + `"}`

			rec := postVCSLink(t, svc, taskID, body)

			assert.Equal(t, http.StatusCreated, rec.Code)
			require.Len(t, svc.createCalls, 1)
			assert.Equal(t, tc.want, svc.createCalls[0].Status)
		})
	}
}

// Omitting status entirely must still work — the handler passes "" through,
// leaving the default decision to the service (which knows the link_type).
func TestVCSLinkHandler_Create_OmittedStatusPassesThrough(t *testing.T) {
	taskID := uuid.New()
	svc := &stubVCSLinkService{}
	body := `{"link_type":"pr","external_id":"42","url":"https://github.com/o/r/pull/42"}`

	rec := postVCSLink(t, svc, taskID, body)

	assert.Equal(t, http.StatusCreated, rec.Code)
	require.Len(t, svc.createCalls, 1)
	assert.Equal(t, domain.VCSLinkStatus(""), svc.createCalls[0].Status)
}

// The upsert path (explicit status onto an existing link) updates a row
// rather than inserting one. The handler must reflect that in the status
// code — 200, not 201 — otherwise the response reads as "a new link was
// created" when in fact nothing but status/title/metadata/url changed on the
// SAME row (#b73171fa: the previous handler always answered 201 regardless
// of which branch the service actually took).
func TestVCSLinkHandler_Create_UpsertOnExistingLinkReturns200(t *testing.T) {
	taskID := uuid.New()
	existingID := uuid.New()
	existingCreatedAt := "2026-07-29T21:06:46Z"
	notCreated := false
	svc := &stubVCSLinkService{
		createReturn: &domain.VCSLink{
			ID:        existingID,
			TaskID:    taskID,
			Status:    domain.VCSLinkStatusMerged,
			CreatedAt: mustParseRFC3339(t, existingCreatedAt),
		},
		createReturnCreated: &notCreated,
	}
	body := `{"link_type":"pr","external_id":"40","url":"https://github.com/entire-vc/evc-mesh-mcp/pull/40","status":"merged"}`

	rec := postVCSLink(t, svc, taskID, body)

	assert.Equal(t, http.StatusOK, rec.Code, "updating an existing link must answer 200, not 201")

	var resp domain.VCSLink
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, existingID, resp.ID, "response must carry the EXISTING row's id, not a freshly generated one")
	assert.Equal(t, mustParseRFC3339(t, existingCreatedAt), resp.CreatedAt)
}

// The plain-insert path (no explicit status, or a genuinely new link) still
// answers 201 — this pins the "insert" side of the same branch so the fix
// above can't be satisfied by just always returning 200.
func TestVCSLinkHandler_Create_FreshInsertReturns201(t *testing.T) {
	taskID := uuid.New()
	svc := &stubVCSLinkService{}
	body := `{"link_type":"pr","external_id":"41","url":"https://github.com/entire-vc/evc-mesh-mcp/pull/41","status":"merged"}`

	rec := postVCSLink(t, svc, taskID, body)

	assert.Equal(t, http.StatusCreated, rec.Code)
}

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return tm
}

func TestVCSLinkHandler_Create_RejectsUnknownStatus(t *testing.T) {
	taskID := uuid.New()
	svc := &stubVCSLinkService{}
	body := `{"link_type":"pr","external_id":"40","url":"https://github.com/o/r/pull/40","status":"pending"}`

	rec := postVCSLink(t, svc, taskID, body)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, svc.createCalls)

	respBody := rec.Body.String()
	assert.Contains(t, respBody, "pending")
	for _, name := range domain.VCSLinkStatusNames {
		assert.Contains(t, respBody, name)
	}
}

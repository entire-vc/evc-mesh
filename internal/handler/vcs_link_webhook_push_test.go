package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/service"
)

// The push (commit) path had no test at all, which is part of how it stayed on
// a MESH--only parser without anyone noticing. These tests pin the two things
// that matter: what the handler hands the resolver, and what it does with each
// possible answer.

func newPushRequest(t *testing.T, deliveryID, ref, commitMsg string) *http.Request {
	t.Helper()
	payload := map[string]any{
		"ref": ref,
		"head_commit": map[string]any{
			"id":      "82377a26b856c04d1f9e3a7b5c2d8e6f0a1b3c4d",
			"message": commitMsg,
			"url":     "https://github.com/entire-vc/evc-mesh/commit/82377a26",
		},
		"repository": map[string]any{
			"full_name": "entire-vc/evc-mesh",
			"html_url":  "https://github.com/entire-vc/evc-mesh",
		},
	}
	b, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(b))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	return req
}

func sendPush(t *testing.T, h *VCSLinkHandler, req *http.Request) map[string]string {
	t.Helper()
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	require.NoError(t, h.GitHubWebhook(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

// The commit message and the branch must BOTH reach the resolver — the branch
// is often the only place the task id appears, and it was not parsed at all
// before.
func TestGitHubWebhook_Push_PassesMessageAndBranchToResolver(t *testing.T) {
	taskID := uuid.New()
	svc := &stubVCSLinkService{
		resolveTaskID: taskID,
		resolveRef:    service.TaskRef{Kind: service.RefKindShortID, Short: taskID.String()[:8]},
	}
	h := NewVCSLinkHandler(svc, WithWebhookDedupStore(newMemDedupStore()))

	const msg = "fix(gate): stop the train\n\nRefs #82377a26"
	resp := sendPush(t, h, newPushRequest(t, "push-1", "refs/heads/linus/82377a26-gate", msg))
	assert.Equal(t, "ok", resp["status"])

	sources, ok := svc.lastResolveSources()
	require.True(t, ok, "resolver must be consulted on a push")
	require.Len(t, sources, 2)
	assert.Equal(t, "body", sources[0].Name)
	assert.Equal(t, msg, sources[0].Text, "the whole commit message, not just its first line")
	assert.Equal(t, "branch", sources[1].Name)
	assert.Equal(t, "linus/82377a26-gate", sources[1].Text,
		"refs/heads/ must be stripped so branch segments split cleanly")

	require.Len(t, svc.createCalls, 1, "a resolved reference must create the commit link")
	assert.Equal(t, taskID, svc.createCalls[0].TaskID)
}

func TestGitHubWebhook_Push_NoReference_CreatesNothing(t *testing.T) {
	svc := &stubVCSLinkService{} // resolveTaskID stays uuid.Nil
	h := NewVCSLinkHandler(svc, WithWebhookDedupStore(newMemDedupStore()))

	resp := sendPush(t, h, newPushRequest(t, "push-2", "refs/heads/garfield/gofmt", "chore: gofmt"))
	assert.Equal(t, "no_task_ref", resp["status"])
	assert.Empty(t, svc.createCalls, "no link may be created without a resolvable reference")
}

// A push with no head_commit (branch deletion, tag push) must not reach the
// resolver at all.
func TestGitHubWebhook_Push_NoHeadCommit_Ignored(t *testing.T) {
	svc := &stubVCSLinkService{}
	h := NewVCSLinkHandler(svc, WithWebhookDedupStore(newMemDedupStore()))

	payload, err := json.Marshal(map[string]any{
		"ref":        "refs/heads/main",
		"repository": map[string]any{"full_name": "entire-vc/evc-mesh"},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "push-3")

	resp := sendPush(t, h, req)
	assert.Equal(t, "ignored", resp["status"])
	_, consulted := svc.lastResolveSources()
	assert.False(t, consulted)
	assert.Empty(t, svc.createCalls)
}

// pull_request.head.ref is new in the payload projection; if it silently
// stopped being parsed the branch signal would go dead without any test noticing.
func TestGitHubWebhook_PullRequest_ForwardsHeadBranch(t *testing.T) {
	svc := &stubVCSLinkService{}
	h := NewVCSLinkHandler(svc, WithWebhookDedupStore(newMemDedupStore()))

	payload, err := json.Marshal(map[string]any{
		"action": "opened",
		"pull_request": map[string]any{
			"number":   777,
			"title":    "feat: cost tracking dashboard",
			"body":     "",
			"html_url": "https://github.com/entire-vc/evc-mesh/pull/777",
			"state":    "open",
			"merged":   false,
			"head":     map[string]any{"ref": "linus/82377a26-cost-tracking"},
		},
		"repository": map[string]any{"full_name": "entire-vc/evc-mesh"},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "pr-777")

	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	require.NoError(t, h.GitHubWebhook(c))
	require.Equal(t, http.StatusOK, rec.Code)

	ev, ok := svc.lastHandleEvent()
	require.True(t, ok)
	assert.Equal(t, "linus/82377a26-cost-tracking", ev.PRBranch)
}

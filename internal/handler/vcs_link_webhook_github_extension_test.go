package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	githubext "github.com/entire-vc/evc-mesh/internal/extensions/github"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// rowConns shows the extension the same workspace row the repository holds.
type rowConns struct{ repo *fakeResolverIntegrationRepo }

func (c rowConns) Connection(_ context.Context, ws uuid.UUID, id string) (*extensions.Connection, error) {
	row, _ := c.repo.GetByProvider(context.Background(), ws, domain.IntegrationProvider(id))
	if row == nil {
		return nil, nil
	}
	return &extensions.Connection{IsActive: row.IsActive, Settings: map[string]any{"webhook_secret": "ws-secret"}}, nil
}

type switchInst struct{ s *extensions.InstanceSetting }

func (i switchInst) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return i.s, nil
}

// C2 through the extension: the webhook is served on the canonical extension
// route and on both legacy aliases; a workspace that switched GitHub off, or an
// instance switched off, gets 401 with a reason, never 200.
func TestGitHubWebhook_ExtensionRoutes_DisableIsRefusedNot200(t *testing.T) {
	const instSecret = "instance-secret"
	repo := &fakeResolverIntegrationRepo{}
	inst := switchInst{s: &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"webhook_secret": instSecret}}}
	prov := githubext.New(extensions.Deps{Conns: rowConns{repo}, Inst: inst})
	resolver := service.NewVCSIntegrationResolver(repo, service.VCSEnvFallback{}).WithGitHubExtension(prov)

	svc := &stubVCSLinkService{handleResult: service.PRHandleResult{Transitioned: true}}
	h := NewVCSLinkHandler(svc, WithVCSIntegrationResolver(resolver), WithWebhookDedupStore(newMemDedupStore()))
	prov.WebhookHandler = h.GitHubWebhook

	e := echo.New()
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(prov))
	reg.RegisterRoutes(e)
	e.POST("/webhooks/github", h.GitHubWebhook)
	e.POST("/api/v1/integrations/github/webhook", h.GitHubWebhook)

	body := newPullRequestPayload(t, "closed", 1, "MESH-"+uuid.New().String(), "", true, "sha1")
	post := func(path, delivery, secret string) *httptest.ResponseRecorder {
		req := newPullRequestRequest(t, body, delivery, secret)
		req.URL.Path = path
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	// positive control: the instance level governs, all three paths deliver.
	for i, path := range []string{githubext.WebhookPath, "/webhooks/github", "/api/v1/integrations/github/webhook"} {
		assert.Equal(t, http.StatusOK, post(path, "ok-"+string(rune('a'+i)), instSecret).Code, path)
	}
	assert.Len(t, svc.handleCalls, 3)

	// the workspace switches GitHub off: refused on every path.
	repo.row = &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitHub, IsActive: false}
	for i, path := range []string{githubext.WebhookPath, "/webhooks/github"} {
		rec := post(path, "ws-off-"+string(rune('a'+i)), instSecret)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, path)
		assert.Contains(t, rec.Body.String(), "disabled")
	}

	// instance switched off, no workspace row: refused too.
	repo.row = nil
	inst.s.Enabled = false
	assert.Equal(t, http.StatusUnauthorized, post(githubext.WebhookPath, "inst-off", instSecret).Code)
	assert.Len(t, svc.handleCalls, 3, "no delivery may reach the service while disabled")
}

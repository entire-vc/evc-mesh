package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	gitlabext "github.com/entire-vc/evc-mesh/internal/extensions/gitlab"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// The GitLab webhook through the extension: delivered on the canonical
// extension route and on the /webhooks/gitlab alias (what every configured
// GitLab project points at); a workspace that switched GitLab off,
// or an instance switched off, gets 401 with a reason, never 200.
func TestGitLabWebhook_ExtensionRoutes_DisableIsRefusedNot200(t *testing.T) {
	const instSecret = "instance-secret"
	repo := &fakeResolverIntegrationRepo{}
	inst := switchInst{s: &extensions.InstanceSetting{Enabled: true, Settings: map[string]any{"base_url": "https://git.example", "webhook_secret": instSecret}}}
	prov := gitlabext.New(extensions.Deps{Conns: rowConns{repo}, Inst: inst})
	resolver := service.NewVCSIntegrationResolver(repo, service.VCSEnvFallback{}).WithGitLabExtension(prov)

	svc := &stubVCSLinkService{}
	h := NewVCSLinkHandler(svc, WithVCSIntegrationResolver(resolver), WithWebhookDedupStore(newMemDedupStore()))
	prov.WebhookHandler = h.GitLabWebhook

	e := echo.New()
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(prov))
	reg.RegisterRoutes(e)
	e.POST("/webhooks/gitlab", h.GitLabWebhook)

	body := newMergeRequestPayload(t, "merge", 7, "MESH-"+uuid.New().String(), "", "merged", "deadbeefcafef00d1234567")
	post := func(path, secret string) *httptest.ResponseRecorder {
		req := newMergeRequestRequest(t, body, "Merge Request Hook", secret)
		req.URL.Path = path
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	paths := []string{gitlabext.WebhookPath, "/webhooks/gitlab"}

	// positive control: the instance level governs, both paths deliver.
	for _, path := range paths {
		assert.Equal(t, http.StatusOK, post(path, instSecret).Code, path)
	}
	assert.Len(t, svc.gitlabHandleCalls, 2)

	// wrong token is refused on the alias that existing projects use.
	assert.Equal(t, http.StatusUnauthorized, post("/webhooks/gitlab", "not-the-secret").Code)

	// the workspace switches GitLab off: refused on every path.
	repo.row = &domain.IntegrationConfig{Provider: domain.IntegrationProviderGitLab, IsActive: false}
	for _, path := range paths {
		rec := post(path, instSecret)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, path)
		assert.Contains(t, rec.Body.String(), "disabled")
	}

	// instance switched off, no workspace row: refused too.
	repo.row = nil
	inst.s.Enabled = false
	assert.Equal(t, http.StatusUnauthorized, post(gitlabext.WebhookPath, instSecret).Code)
	assert.Len(t, svc.gitlabHandleCalls, 2, "no delivery may reach the service while disabled")
}

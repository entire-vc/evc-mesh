package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// relayAt resolves every workspace to one Team Relay connection.
type relayAt string

func (r relayAt) Resolve(context.Context, uuid.UUID) (extensions.Resolved, error) {
	return extensions.Resolved{Enabled: true, Settings: map[string]any{"relay_url": string(r)}}, nil
}

const keyShareID = "5ba2a6c4-3b9b-485b-b0e4-b8b8691dc49a"

var testAgentKey = "tr_agent_" + strings.Repeat("b", 48)

// keyRelay is a relay that knows testAgentKey; status overrides the answer.
func keyRelay(t *testing.T, status int, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/web/agent-key" {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if r.Header.Get("X-Agent-Key") != testAgentKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":"k1","share_id":"` + keyShareID + `","scopes":["read","write"],
			"share":{"id":"` + keyShareID + `","kind":"folder","path":"Vault/Mesh Docs","web_slug":"mesh-docs","web_published":true}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func keyShareService(relayURL string) (ProjectIntegrationService, *MockProjectIntegrationRepository) {
	repo := NewMockProjectIntegrationRepository()
	projects := NewMockProjectRepository().WithDefaultWorkspace(uuid.New())
	return NewProjectIntegrationService(repo, WithProjectIntegrationRelayResolver(relayAt(relayURL), projects)), repo
}

func storedShare(t *testing.T, pi *domain.ProjectIntegration) domain.TeamRelaySettings {
	t.Helper()
	var s domain.TeamRelaySettings
	require.NoError(t, json.Unmarshal(pi.Settings, &s))
	return s
}

// The key alone binds the project: the share's id, slug and name come from the relay.
func TestUpsertTeamRelay_ShareFollowsFromAgentKey(t *testing.T) {
	var calls atomic.Int32
	svc, _ := keyShareService(keyRelay(t, http.StatusOK, &calls).URL)

	pi, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{Enabled: true, AgentKey: testAgentKey})
	require.NoError(t, err)
	s := storedShare(t, pi)
	assert.Equal(t, keyShareID, s.ShareID)
	assert.Equal(t, "mesh-docs", s.ShareSlug)
	assert.Equal(t, "Vault/Mesh Docs", s.ShareName)
	assert.Equal(t, int32(1), calls.Load())
}

// A save without a new key (the form edits the subfolder) keeps the bound share.
func TestUpsertTeamRelay_SaveWithoutKeyKeepsBoundShare(t *testing.T) {
	var calls atomic.Int32
	svc, _ := keyShareService(keyRelay(t, http.StatusOK, &calls).URL)
	projectID := uuid.New()
	_, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{Enabled: true, AgentKey: testAgentKey})
	require.NoError(t, err)

	pi, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{Enabled: true, Subfolder: "notes"})
	require.NoError(t, err)
	s := storedShare(t, pi)
	assert.Equal(t, keyShareID, s.ShareID)
	assert.Equal(t, "Vault/Mesh Docs", s.ShareName)
	assert.Equal(t, "notes", s.Subfolder)
	assert.Equal(t, int32(1), calls.Load(), "no key, no relay call")
}

// Naming a share the key does not belong to is refused, not silently rebound.
func TestUpsertTeamRelay_ShareOtherThanTheKeysIsRefused(t *testing.T) {
	var calls atomic.Int32
	svc, _ := keyShareService(keyRelay(t, http.StatusOK, &calls).URL)

	_, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{
		Enabled: true, AgentKey: testAgentKey, ShareID: uuid.NewString(),
	})
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Validation["agent_key"], "Vault/Mesh Docs")
}

func TestUpsertTeamRelay_KeyTheRelayRejectsIsRefused(t *testing.T) {
	var calls atomic.Int32
	svc, _ := keyShareService(keyRelay(t, http.StatusUnauthorized, &calls).URL)

	_, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{Enabled: true, AgentKey: testAgentKey})
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Validation, "agent_key")
}

// A relay without the route (404) leaves the share the caller sent; with none,
// the save is refused as before rather than stored unbound.
func TestUpsertTeamRelay_RelayWithoutRouteFallsBackToSentShare(t *testing.T) {
	var calls atomic.Int32
	svc, _ := keyShareService(keyRelay(t, http.StatusNotFound, &calls).URL)

	pi, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{
		Enabled: true, AgentKey: testAgentKey, ShareID: keyShareID,
	})
	require.NoError(t, err)
	assert.Equal(t, keyShareID, storedShare(t, pi).ShareID)

	_, err = svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{Enabled: true, AgentKey: testAgentKey})
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Validation, "share_slug")
}

func TestUpsertTeamRelay_KeyExpiredInRelayIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Agent key has expired"}}`))
	}))
	t.Cleanup(srv.Close)
	svc, _ := keyShareService(srv.URL)

	_, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{Enabled: true, AgentKey: testAgentKey})
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Validation["agent_key"], "expired")
}

// No Team Relay connection for the workspace: the share is taken as sent.
func TestUpsertTeamRelay_NoConnectionKeepsSentShare(t *testing.T) {
	projects := NewMockProjectRepository().WithDefaultWorkspace(uuid.New())
	svc := NewProjectIntegrationService(NewMockProjectIntegrationRepository(), WithProjectIntegrationRelayResolver(noRelay{}, projects))

	pi, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{
		Enabled: true, AgentKey: testAgentKey, ShareID: keyShareID, ShareSlug: "mesh-docs",
	})
	require.NoError(t, err)
	assert.Equal(t, keyShareID, storedShare(t, pi).ShareID)
}

// noRelay resolves every workspace to a disabled Team Relay connection.
type noRelay struct{}

func (noRelay) Resolve(context.Context, uuid.UUID) (extensions.Resolved, error) {
	return extensions.Resolved{}, nil
}

// Without a new key the stored key decides the share: naming another share is
// refused and the binding stays where it was.
func TestUpsertTeamRelay_OtherShareWithoutItsKeyIsRefused(t *testing.T) {
	var calls atomic.Int32
	svc, repo := keyShareService(keyRelay(t, http.StatusOK, &calls).URL)
	projectID := uuid.New()
	_, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{Enabled: true, AgentKey: testAgentKey})
	require.NoError(t, err)

	for _, in := range []UpsertProjectIntegrationInput{
		{Enabled: true, ShareID: uuid.NewString()},
		{Enabled: true, ShareSlug: "other-share"},
		{Enabled: true, ShareID: keyShareID, ShareSlug: "other-share"},
		{Enabled: true, ShareID: uuid.NewString(), ShareSlug: "mesh-docs"},
	} {
		_, err = svc.UpsertTeamRelay(context.Background(), projectID, in)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Contains(t, apiErr.Validation, "agent_key")
	}
	pi, err := repo.Get(context.Background(), projectID, "team_relay")
	require.NoError(t, err)
	assert.Equal(t, keyShareID, storedShare(t, pi).ShareID)
	assert.Equal(t, "mesh-docs", storedShare(t, pi).ShareSlug)
}

// A key of an unpublished share has no web slug: a slug the caller sent names
// another share and is not stored next to this one.
func TestUpsertTeamRelay_UnpublishedShareDropsSentSlug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"share_id":"` + keyShareID + `","share":{"id":"` + keyShareID + `","path":"Vault/Drafts","web_slug":"","web_published":false}}`))
	}))
	t.Cleanup(srv.Close)
	svc, _ := keyShareService(srv.URL)

	pi, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{
		Enabled: true, AgentKey: testAgentKey, ShareSlug: "someone-elses-share",
	})
	require.NoError(t, err)
	s := storedShare(t, pi)
	assert.Equal(t, keyShareID, s.ShareID)
	assert.Equal(t, "", s.ShareSlug)
	assert.Equal(t, "Vault/Drafts", s.ShareName)
}

// Naming the bound share, by either field, is an ordinary save.
func TestUpsertTeamRelay_SameShareWithoutKeyIsKept(t *testing.T) {
	var calls atomic.Int32
	svc, _ := keyShareService(keyRelay(t, http.StatusOK, &calls).URL)
	projectID := uuid.New()
	_, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{Enabled: true, AgentKey: testAgentKey})
	require.NoError(t, err)

	pi, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{Enabled: true, ShareID: strings.ToUpper(keyShareID), Subfolder: "x"})
	require.NoError(t, err)
	s := storedShare(t, pi)
	assert.Equal(t, keyShareID, s.ShareID)
	assert.Equal(t, "mesh-docs", s.ShareSlug)
	assert.Equal(t, "Vault/Mesh Docs", s.ShareName)
}

// A binding stored without a key is held to the same rule: a keyless save
// cannot move it to another share.
func TestUpsertTeamRelay_KeylessBindingIsNotRebound(t *testing.T) {
	svc, repo := keyShareService("")
	projectID := uuid.New()
	settings, _ := json.Marshal(domain.TeamRelaySettings{ShareID: keyShareID, ShareSlug: "mesh-docs"})
	require.NoError(t, repo.Upsert(context.Background(), &domain.ProjectIntegration{ID: uuid.New(), ProjectID: projectID, Type: "team_relay", Settings: settings}))

	_, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{ShareSlug: "other-share"})
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Contains(t, apiErr.Validation, "agent_key")
	pi, err := repo.Get(context.Background(), projectID, "team_relay")
	require.NoError(t, err)
	assert.Equal(t, "mesh-docs", storedShare(t, pi).ShareSlug)
}

// A binding with a key but no share stored (a disabled integration) cannot be
// bound to a share by a keyless save; an empty save changes nothing.
func TestUpsertTeamRelay_BindingWithoutShareIsNotBoundWithoutKey(t *testing.T) {
	svc, repo := keyShareService("")
	projectID := uuid.New()
	require.NoError(t, repo.Upsert(context.Background(), &domain.ProjectIntegration{
		ID: uuid.New(), ProjectID: projectID, Type: "team_relay", AgentKey: testAgentKey, Settings: json.RawMessage(`{}`),
	}))

	for _, in := range []UpsertProjectIntegrationInput{{ShareID: keyShareID}, {ShareSlug: "other-share"}} {
		_, err := svc.UpsertTeamRelay(context.Background(), projectID, in)
		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Contains(t, apiErr.Validation, "agent_key")
	}
	pi, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{Subfolder: "x"})
	require.NoError(t, err)
	s := storedShare(t, pi)
	assert.Equal(t, "", s.ShareID)
	assert.Equal(t, "", s.ShareSlug)
}

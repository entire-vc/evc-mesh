package handler

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

type fakeUsage map[string]int

func (f fakeUsage) CountActiveConnections(context.Context) (map[string]int, error) { return f, nil }

// searchPolicyRepo records the filter the handler passes down.
type searchPolicyRepo struct {
	*fakePolicyRepo
	got domain.InstanceJournalFilter
}

func (s *searchPolicyRepo) SearchJournal(_ context.Context, f domain.InstanceJournalFilter) ([]domain.InstanceExtensionJournalEntry, error) {
	s.got = f
	var out []domain.InstanceExtensionJournalEntry
	for _, e := range s.journal {
		if (f.ExtensionID == "" || e.ExtensionID == f.ExtensionID) && (f.Action == "" || e.Action == f.Action) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *searchPolicyRepo) JournalFacets(context.Context) (*domain.InstanceJournalFacets, error) {
	return &domain.InstanceJournalFacets{Actions: []string{"allow_changed", "theme_selected"}, Actors: []domain.InstanceJournalActor{}, Extensions: []string{"community-x"}}, nil
}

func TestInstanceAdmin_OverviewHasSourceUsageAndHidesSystemParts(t *testing.T) {
	pol := newFakePolicyRepo()
	installed := &adminInstalledRepo{fakeInstalledRepo{rows: []domain.InstalledExtension{
		{ExtensionID: "team-relay", Manifest: []byte(testManifest), Source: domain.ExtensionSourceBuiltin, Version: "1.2.0", TrustLevel: domain.ExtensionTrustBuiltin},
		{ExtensionID: "community-x", Manifest: []byte(strings.Replace(testManifest, `"team-relay"`, `"community-x"`, 1)), Source: domain.ExtensionSourceRepo, TrustLevel: "custom", Blocked: true, BlockedReason: "withdrawn by the publisher"},
		{ExtensionID: "runner", Manifest: []byte(strings.Replace(testManifest, `"team-relay"`, `"runner"`, 1)), Source: domain.ExtensionSourceBuiltin, TrustLevel: domain.ExtensionTrustBuiltin},
	}}}
	h := NewExtensionInstanceAdminHandler(pol, &fakeInstRepo{}, installed, fakeAdmins{ok: true}).WithUsage(fakeUsage{"team-relay": 3})

	c, rec := adminCtx(http.MethodGet, "", "", false)
	require.NoError(t, h.Overview(c))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Regexp(t, `"id":"team-relay"[^}]*"source":"builtin","version":"1.2.0"[^}]*"enabled_workspaces":3`, body)
	require.Regexp(t, `"id":"community-x"[^}]*"source":"repo"[^}]*"enabled_workspaces":0`, body, "a counted extension with no workspaces says 0, not nothing")
	require.Regexp(t, `"id":"community-x"[^}]*"blocked":true,"blocked_reason":"withdrawn by the publisher"`, body, "a catalog block reaches the table")
	require.Regexp(t, `"id":"team-relay"[^}]*"blocked":false`, body)
	require.NotContains(t, body, `"id":"runner"`, "the runner server side is a system part, not a table row")

	// without a counter the field is absent rather than a misleading 0
	h = NewExtensionInstanceAdminHandler(pol, &fakeInstRepo{}, installed, fakeAdmins{ok: true})
	c, rec = adminCtx(http.MethodGet, "", "", false)
	require.NoError(t, h.Overview(c))
	require.NotContains(t, rec.Body.String(), "enabled_workspaces")
}

func TestInstanceAdmin_JournalFilters(t *testing.T) {
	pol := &searchPolicyRepo{fakePolicyRepo: newFakePolicyRepo()}
	pol.journal = []domain.InstanceExtensionJournalEntry{
		{ID: uuid.New(), ExtensionID: "mesh-dark", Action: "theme_selected"},
		{ID: uuid.New(), ExtensionID: "community-x", Action: "allow_changed"},
	}
	h := NewExtensionInstanceAdminHandler(pol, &fakeInstRepo{}, &adminInstalledRepo{}, fakeAdmins{ok: true})
	actor := uuid.New()

	q := url.Values{"extension": {"community-x"}, "action": {"allow_changed"}, "actor": {actor.String()}, "since": {"2026-10-01"}, "until": {"2026-10-27T12:00:00Z"}, "facets": {"1"}, "limit": {"500"}}
	c, rec := adminCtx(http.MethodGet, "", "", false)
	c.Request().URL.RawQuery = q.Encode()
	require.NoError(t, h.Journal(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"extension_id":"community-x"`)
	require.NotContains(t, rec.Body.String(), "mesh-dark", "the theme rows are filtered out")
	require.Contains(t, rec.Body.String(), `"facets":{"actions":["allow_changed","theme_selected"]`)
	require.Equal(t, 200, pol.got.Limit, "the limit stays capped")
	require.Equal(t, actor, *pol.got.ActorID)
	require.Equal(t, "2026-10-01T00:00:00Z", pol.got.Since.UTC().Format("2006-01-02T15:04:05Z07:00"))
	require.Equal(t, "2026-10-27T12:00:00Z", pol.got.Until.UTC().Format("2006-01-02T15:04:05Z07:00"))

	for _, bad := range []string{"actor=nope", "since=yesterday", "until=27.10.2026"} {
		c, rec = adminCtx(http.MethodGet, "", "", false)
		c.Request().URL.RawQuery = bad
		require.NoError(t, h.Journal(c))
		require.Equal(t, http.StatusBadRequest, rec.Code, bad)
	}
}

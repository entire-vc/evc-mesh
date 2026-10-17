package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/integration/teamrelay"
)

const teamRelayRelayURLEnvVar = "MESH_TEAMRELAY_RELAY_URL"

// urlByWorkspace answers ResolveRelayURL from a map; a workspace that is not
// in it is "Team Relay off".
type urlByWorkspace map[uuid.UUID]string

func (m urlByWorkspace) ResolveRelayURL(_ context.Context, ws uuid.UUID) (relayURL, source string, ok bool) {
	relayURL, ok = m[ws]
	return relayURL, "workspace", ok
}

// urlRecordingDescriber records the relay URL every key introspection went to.
type urlRecordingDescriber struct {
	mu   sync.Mutex
	urls []string
}

func (d *urlRecordingDescriber) DescribeAgentKey(_ context.Context, relayURL, _, _ string) (*teamrelay.AgentKeyDescription, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.urls = append(d.urls, relayURL)
	exp := time.Now().Add(24 * time.Hour)
	return &teamrelay.AgentKeyDescription{ExpiresAt: &exp}, nil
}

func TestRelayURLFor_ResolvesPerWorkspaceThroughExtension(t *testing.T) {
	t.Setenv(teamRelayRelayURLEnvVar, "https://env.must-not-be-used.example")
	f := setupMountFixture(t, "")
	projects := newFakeProjectRepo()
	projects.put(f.projectID, f.wsID)
	WithRelayURLResolver(urlByWorkspace{f.wsID: "https://ws.tr.example"}, projects)(f.svc)

	rs, err := f.svc.settingsFor(context.Background(), f.projectID)
	require.NoError(t, err)
	assert.Equal(t, "https://ws.tr.example", rs.relayURL, "the extension answer governs, not the process env")
}

func TestRelayURLFor_TeamRelayOffForWorkspaceIsRefused(t *testing.T) {
	t.Setenv(teamRelayRelayURLEnvVar, "https://env.must-not-be-used.example")
	f := setupMountFixture(t, "")
	projects := newFakeProjectRepo()
	projects.put(f.projectID, f.wsID)
	WithRelayURLResolver(urlByWorkspace{}, projects)(f.svc)

	_, err := f.svc.settingsFor(context.Background(), f.projectID)
	require.Error(t, err, "an off workspace must not fall through to env")
	assert.Contains(t, err.Error(), "not configured")
}

func TestRelayURLFor_UnwiredResolverRefusesEnvPath(t *testing.T) {
	f := setupMountFixture(t, "")
	f.svc.relayResolver = nil
	t.Setenv(teamRelayRelayURLEnvVar, "https://env.tr.example")
	_, err := f.svc.settingsFor(context.Background(), f.projectID)
	require.ErrorContains(t, err, "resolver not configured")
}

func TestRelayURLFor_ProjectLookupFailureIsAnError(t *testing.T) {
	f := setupMountFixture(t, "")
	projects := NewMockProjectRepository() // project not seeded: GetByID fails
	WithRelayURLResolver(urlByWorkspace{f.wsID: "https://ws.tr.example"}, projects)(f.svc)

	_, err := f.svc.relayURLFor(context.Background(), f.projectID)
	require.Error(t, err)
}

func TestRefreshSleepingKeyExpiries_UsesEachProjectsWorkspaceURL(t *testing.T) {
	f := setupMountFixture(t, "")
	projects := newFakeProjectRepo()
	projects.put(f.projectID, f.wsID)
	desc := &urlRecordingDescriber{}
	f.svc.keyDescriber = desc
	WithRelayURLResolver(urlByWorkspace{f.wsID: "https://ws.tr.example"}, projects)(f.svc)

	checked, updated, err := f.svc.RefreshSleepingKeyExpiries(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, checked)
	assert.Equal(t, 1, updated)
	assert.Equal(t, []string{"https://ws.tr.example"}, desc.urls)
}

func TestRefreshSleepingKeyExpiries_OffWorkspaceIsSkippedNotFatal(t *testing.T) {
	f := setupMountFixture(t, "")
	projects := newFakeProjectRepo()
	projects.put(f.projectID, f.wsID)
	desc := &urlRecordingDescriber{}
	f.svc.keyDescriber = desc
	WithRelayURLResolver(urlByWorkspace{}, projects)(f.svc)

	checked, updated, err := f.svc.RefreshSleepingKeyExpiries(context.Background())
	require.NoError(t, err, "one workspace with Team Relay off must not stop the sweep")
	assert.Equal(t, 1, checked)
	assert.Equal(t, 0, updated)
	assert.Empty(t, desc.urls, "no call may go out without a resolved URL")
}

func TestRelayURLFor_ResolverWithoutProjectRepoDoesNotFallBackToEnv(t *testing.T) {
	t.Setenv(teamRelayRelayURLEnvVar, "https://env.must-not-be-used.example")
	f := setupMountFixture(t, "")
	WithRelayURLResolver(urlByWorkspace{f.wsID: "https://ws.tr.example"}, nil)(f.svc)

	_, err := f.svc.settingsFor(context.Background(), f.projectID)
	require.Error(t, err, "a wired resolver must never degrade to the process env")
	assert.Contains(t, err.Error(), "without a project repository")
}

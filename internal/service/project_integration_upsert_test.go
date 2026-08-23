package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func TestUpsertTeamRelay_EnabledRequiresShareIdentifier(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	svc := NewProjectIntegrationService(repo)

	_, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{
		Enabled: true,
	})
	require.Error(t, err)
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok, "expected *apierror.Error, got %T", err)
	assert.Contains(t, apiErr.Validation, "share_slug")
}

func TestUpsertTeamRelay_InvalidShareID(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	svc := NewProjectIntegrationService(repo)

	_, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{
		Enabled: true,
		ShareID: "not-a-uuid",
	})
	require.Error(t, err)
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok, "expected *apierror.Error, got %T", err)
	assert.Contains(t, apiErr.Validation, "share_id")
}

func TestUpsertTeamRelay_AgentKeyTooShort(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	svc := NewProjectIntegrationService(repo)

	_, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{
		Enabled:   true,
		ShareSlug: "probe-share",
		AgentKey:  "too-short",
	})
	require.Error(t, err)
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok, "expected *apierror.Error, got %T", err)
	assert.Contains(t, apiErr.Validation, "agent_key")
}

func TestUpsertTeamRelay_AgentKeyRequiredOnFirstCreate(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	svc := NewProjectIntegrationService(repo)

	_, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{
		Enabled:   true,
		ShareSlug: "probe-share",
	})
	require.Error(t, err)
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok, "expected *apierror.Error, got %T", err)
	assert.Contains(t, apiErr.Validation, "agent_key")
}

// TestUpsertTeamRelay_UpdateKeepsExistingAgentKeyWhenOmitted pins that
// updating an already-configured integration (e.g. only to change the mount
// point) does not require re-supplying the agent key.
func TestUpsertTeamRelay_UpdateKeepsExistingAgentKeyWhenOmitted(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	svc := NewProjectIntegrationService(repo)
	projectID := uuid.New()

	_, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{
		Enabled:   true,
		ShareSlug: "probe-share",
		AgentKey:  "tr_agent_" + strings.Repeat("a", 48),
	})
	require.NoError(t, err)

	_, err = svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{
		Enabled:       true,
		ShareSlug:     "probe-share",
		DocsMountPath: "External/Notes",
	})
	require.NoError(t, err)
}

// TestUpsertTeamRelay_SettingsRoundTripDocsMountPath is the direct regression
// test for the fix: DocsMountPath must reach the persisted settings JSON, not
// be silently dropped the way it was before UpsertProjectIntegrationInput
// carried the field at all.
func TestUpsertTeamRelay_SettingsRoundTripDocsMountPath(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	svc := NewProjectIntegrationService(repo)
	projectID := uuid.New()

	pi, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{
		Enabled:       true,
		ShareSlug:     "probe-share",
		AgentKey:      "tr_agent_" + strings.Repeat("a", 48),
		DocsMountPath: "Team Relay/Archive",
	})
	require.NoError(t, err)

	var settings domain.TeamRelaySettings
	require.NoError(t, json.Unmarshal(pi.Settings, &settings))
	assert.Equal(t, "Team Relay/Archive", settings.DocsMountPath)
	assert.Equal(t, "probe-share", settings.ShareSlug)
}

// TestUpsertTeamRelay_SecondPatchChangesOnlyMountPath is the scenario this
// whole task exists for: a caller updates ONLY the mount point on an
// already-configured integration, and it must not require touching anything
// else — this is what "without a manual DB edit" means end to end through
// the service layer.
func TestUpsertTeamRelay_SecondPatchChangesOnlyMountPath(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	svc := NewProjectIntegrationService(repo)
	projectID := uuid.New()

	_, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{
		Enabled:            true,
		ShareSlug:          "probe-share",
		AgentKey:           "tr_agent_" + strings.Repeat("a", 48),
		Subfolder:          "Notes",
		IncludeProjectSlug: true,
	})
	require.NoError(t, err)

	updated, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{
		Enabled:            true,
		ShareSlug:          "probe-share",
		Subfolder:          "Notes",
		IncludeProjectSlug: true,
		DocsMountPath:      "External/Notes",
	})
	require.NoError(t, err)

	var settings domain.TeamRelaySettings
	require.NoError(t, json.Unmarshal(updated.Settings, &settings))
	assert.Equal(t, "External/Notes", settings.DocsMountPath)
	assert.Equal(t, "Notes", settings.Subfolder)
	assert.True(t, settings.IncludeProjectSlug)
}

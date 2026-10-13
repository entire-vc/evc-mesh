package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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

// ---------------------------------------------------------------------------
// Card #4e97f465: mount exclusion + scheduled-sync settings on the upsert.
// ---------------------------------------------------------------------------

// TestUpsertTeamRelay_ExclusionEntryMustBeSingleSegment: the exclusion is
// matched against a files-index entry's FIRST path segment, so a configured
// "artifacts/deep" can never match anything — refused at the write rather
// than silently excluding nothing.
func TestUpsertTeamRelay_ExclusionEntryMustBeSingleSegment(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	svc := NewProjectIntegrationService(repo)

	_, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{
		Enabled:       true,
		ShareSlug:     "probe-share",
		AgentKey:      "tr_agent_" + strings.Repeat("a", 48),
		ExcludedPaths: []string{"artifacts"},
	})
	require.NoError(t, err, "a bare segment is the valid case")

	_, err = svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{
		Enabled:       true,
		ShareSlug:     "probe-share",
		AgentKey:      "tr_agent_" + strings.Repeat("a", 48),
		ExcludedPaths: []string{"artifacts/nested"},
	})
	require.Error(t, err)
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok, "expected *apierror.Error, got %T", err)
	assert.Contains(t, apiErr.Validation, "excluded_paths")
}

// TestUpsertTeamRelay_ExclusionListRoundTrip: an explicitly empty list is the
// deliberate "mount everything" state and must SURVIVE the round trip as
// empty — not come back nil (never configured) and not come back as the
// default. Trailing-comma blanks are dropped, not fatal.
func TestUpsertTeamRelay_ExclusionListRoundTrip(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	svc := NewProjectIntegrationService(repo)
	projectID := uuid.New()

	pi, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{
		Enabled:       true,
		ShareSlug:     "probe-share",
		AgentKey:      "tr_agent_" + strings.Repeat("a", 48),
		ExcludedPaths: []string{"archive", " ", ""},
	})
	require.NoError(t, err)

	var settings domain.TeamRelaySettings
	require.NoError(t, json.Unmarshal(pi.Settings, &settings))
	require.NotNil(t, settings.ExcludedPaths, "an explicitly-sent list must stay explicit after the round trip")
	assert.Equal(t, []string{"archive"}, settings.ExcludedPaths, "blank entries dropped, real ones kept")
}

// TestUpsertTeamRelay_NotSendingExclusionsLeavesDefaultToReadSide: a caller
// that does not send the field stores ABSENT settings — the read-side default
// ["artifacts"] applies. This is the state every pre-existing integration is
// in the moment this code ships.
func TestUpsertTeamRelay_NotSendingExclusionsLeavesDefaultToReadSide(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	svc := NewProjectIntegrationService(repo)

	pi, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{
		Enabled:   true,
		ShareSlug: "probe-share",
		AgentKey:  "tr_agent_" + strings.Repeat("a", 48),
	})
	require.NoError(t, err)

	var settings domain.TeamRelaySettings
	require.NoError(t, json.Unmarshal(pi.Settings, &settings))
	assert.Nil(t, settings.ExcludedPaths, "absent field must stay absent — the default belongs to the read side")
}

// TestUpsertTeamRelay_SyncIntervalValidation: 0 is valid (scheduled sync
// explicitly off), 5+ is valid, anything between 1 and 4 is refused — the
// floor exists so nobody configures a share-walking loop that never exits
// the walk.
func TestUpsertTeamRelay_SyncIntervalValidation(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	svc := NewProjectIntegrationService(repo)
	base := UpsertProjectIntegrationInput{
		Enabled:   true,
		ShareSlug: "probe-share",
		AgentKey:  "tr_agent_" + strings.Repeat("a", 48),
	}

	zero := 0
	_, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), func() UpsertProjectIntegrationInput {
		in := base
		in.SyncIntervalMinutes = &zero
		return in
	}())
	require.NoError(t, err, "0 = scheduled sync off, a valid explicit choice")

	thirty := 30
	_, err = svc.UpsertTeamRelay(context.Background(), uuid.New(), func() UpsertProjectIntegrationInput {
		in := base
		in.SyncIntervalMinutes = &thirty
		return in
	}())
	require.NoError(t, err)

	three := 3
	_, err = svc.UpsertTeamRelay(context.Background(), uuid.New(), func() UpsertProjectIntegrationInput {
		in := base
		in.SyncIntervalMinutes = &three
		return in
	}())
	require.Error(t, err)
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok, "expected *apierror.Error, got %T", err)
	assert.Contains(t, apiErr.Validation, "sync_interval_minutes")
}

// TestSyncIntervalParsing pins the read-side interpretation: nil → 15m
// default, 0 → 0 (caller disables), 3 → floored to the 5m minimum (rows
// written by hand bypass upsert validation; the floor keeps them honest).
func TestSyncIntervalParsing(t *testing.T) {
	assert.Equal(t, DefaultTeamRelaySyncInterval, syncInterval(domain.TeamRelaySettings{}), "unset → default 15m")

	off := 0
	assert.Equal(t, time.Duration(0), syncInterval(domain.TeamRelaySettings{SyncIntervalMinutes: &off}), "explicit 0 → disabled")

	tooLow := 3
	assert.Equal(t, MinTeamRelaySyncInterval, syncInterval(domain.TeamRelaySettings{SyncIntervalMinutes: &tooLow}), "below the floor → clamped to it")

	forty := 40
	assert.Equal(t, 40*time.Minute, syncInterval(domain.TeamRelaySettings{SyncIntervalMinutes: &forty}))
}

// TestUpsertTeamRelay_DisableWithoutKeyKeepsStoredKey: switching the
// integration off (and back on) without re-entering the key must not touch the
// stored credential.
func TestUpsertTeamRelay_DisableWithoutKeyKeepsStoredKey(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	svc := NewProjectIntegrationService(repo)
	projectID := uuid.New()
	key := "tr_agent_" + strings.Repeat("a", 48)

	_, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{
		Enabled: true, ShareSlug: "probe-share", AgentKey: key,
	})
	require.NoError(t, err)

	for _, enabled := range []bool{false, true} {
		got, err := svc.UpsertTeamRelay(context.Background(), projectID, UpsertProjectIntegrationInput{
			Enabled: enabled, ShareSlug: "probe-share",
		})
		require.NoError(t, err)
		assert.Equal(t, key, got.AgentKey, "enabled=%v must keep the stored key", enabled)
	}
}

// TestUpsertTeamRelay_MalformedKeyRejectedEvenWhenDisabled is the regression
// for the form that echoed the "••••" mask back: it must be a readable
// validation error on agent_key regardless of `enabled`.
func TestUpsertTeamRelay_MalformedKeyRejectedEvenWhenDisabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, bad := range []string{"••••••••••••••", "hunter2-browser-password", "tr_agent_short"} {
			repo := NewMockProjectIntegrationRepository()
			svc := NewProjectIntegrationService(repo)
			_, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{
				Enabled: enabled, ShareSlug: "probe-share", AgentKey: bad,
			})
			require.Error(t, err, "enabled=%v key=%q", enabled, bad)
			apiErr, ok := err.(*apierror.Error)
			require.True(t, ok, "expected *apierror.Error, got %T", err)
			assert.Contains(t, apiErr.Validation, "agent_key")
		}
	}
}

func TestUpsertTeamRelay_MaskedKeyMessageIsReadable(t *testing.T) {
	svc := NewProjectIntegrationService(NewMockProjectIntegrationRepository())
	_, err := svc.UpsertTeamRelay(context.Background(), uuid.New(), UpsertProjectIntegrationInput{
		Enabled: false, AgentKey: "••••••••••••••",
	})
	apiErr, ok := err.(*apierror.Error)
	require.True(t, ok)
	assert.Contains(t, apiErr.Validation["agent_key"], "masked placeholder")
}

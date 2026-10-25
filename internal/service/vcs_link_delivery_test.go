package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func sha(c string) string { return strings.Repeat(c, 40) }

func TestPatchMetadata_DeliveryStatesAreSeparate(t *testing.T) {
	svc, _, link := patchMetaSetup(domain.VCSLinkTypePR)
	ctx := context.Background()

	got, err := svc.PatchMetadata(ctx, link.TaskID, link.ID, map[string]any{
		"implementation_sha": sha("1"),
		"merged_sha":         sha("2"),
		"ci_verdict":         map[string]any{"status": "success", "pipeline_id": 16187, "sha": sha("2")},
		"deployed":           map[string]any{"sha": sha("3"), "environment": "prod"},
		"acceptance":         map[string]any{"result": "passed", "by": "verifier", "evidence_url": "https://x/e"},
	})
	require.NoError(t, err)

	d := got.Delivery()
	assert.Equal(t, sha("1"), d.ImplementationSHA)
	assert.Equal(t, sha("2"), d.MergedSHA)
	require.NotNil(t, d.Deployed)
	assert.Equal(t, sha("3"), d.Deployed.SHA, "deployed sha differs from merged sha")
	require.NotNil(t, d.Acceptance)
	assert.Equal(t, "passed", d.Acceptance.Result)
	assert.Equal(t, "success", d.CIVerdict.Status)
	assert.False(t, got.IsShipped(), "deployed on another sha than merged is not shipped")

	var out map[string]any
	b, err := json.Marshal(got)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Contains(t, out, "delivery")
	assert.Equal(t, false, out["is_shipped"])
}

func TestVCSLink_IsShipped(t *testing.T) {
	mk := func(meta string) domain.VCSLink {
		return domain.VCSLink{LinkType: domain.VCSLinkTypePR, Status: domain.VCSLinkStatusMerged, Metadata: json.RawMessage(meta)}
	}
	m := sha("2")
	assert.True(t, mk(`{"merged_sha":"`+m+`","deployed":{"sha":"`+m+`","environment":"prod"}}`).IsShipped())
	assert.False(t, mk(`{"merged_sha":"`+m+`"}`).IsShipped(), "merged but not deployed")
	assert.False(t, mk(`{"merged_sha":"`+m+`","deployed":{"sha":"`+m+`","environment":"staging"}}`).IsShipped(), "staging is not prod")
	assert.False(t, mk(`{"merged_sha":"`+m+`","deployed":{"sha":"`+sha("3")+`","environment":"prod"}}`).IsShipped())
	assert.False(t, mk(`{"deployed":{"sha":"`+m+`","environment":"prod"}}`).IsShipped(), "no merged_sha: fail closed")
	assert.False(t, mk(`{}`).IsShipped())

	// Legacy link without the new keys keeps the old merged+ship+green rule.
	ok := "success"
	legacy := mk(`{"head_sha":"` + m + `","ship_sha":"` + m + `","pipeline_sha":"` + m + `"}`)
	legacy.PipelineStatus = &ok
	assert.True(t, legacy.IsShipped())
}

func TestPatchMetadata_DeployedIsIdempotent(t *testing.T) {
	svc, _, link := patchMetaSetup(domain.VCSLinkTypePR)
	ctx := context.Background()
	dep := map[string]any{"sha": sha("3"), "environment": "Prod"}

	first, err := svc.PatchMetadata(ctx, link.TaskID, link.ID, map[string]any{"deployed": dep})
	require.NoError(t, err)
	at := first.Deployed().At
	require.NotNil(t, at)
	assert.Equal(t, "prod", first.Deployed().Environment)

	time.Sleep(5 * time.Millisecond)
	second, err := svc.PatchMetadata(ctx, link.TaskID, link.ID, map[string]any{"deployed": dep})
	require.NoError(t, err)
	assert.True(t, at.Equal(*second.Deployed().At), "deployed.at must not move for the same sha+env")

	third, err := svc.PatchMetadata(ctx, link.TaskID, link.ID, map[string]any{"deployed": map[string]any{"sha": sha("4"), "environment": "prod"}})
	require.NoError(t, err)
	assert.Equal(t, sha("4"), third.Deployed().SHA)
	assert.True(t, third.Deployed().At.After(*at) || third.Deployed().At.Equal(*at))
}

func TestPatchMetadata_RejectsBadDeliveryValues(t *testing.T) {
	svc, _, link := patchMetaSetup(domain.VCSLinkTypePR)
	for name, patch := range map[string]map[string]any{
		"merged short":      {"merged_sha": "abc"},
		"ci bad status":     {"ci_verdict": map[string]any{"status": "weird"}},
		"ci unknown field":  {"ci_verdict": map[string]any{"status": "success", "x": 1}},
		"deployed no env":   {"deployed": map[string]any{"sha": sha("3")}},
		"deployed bad sha":  {"deployed": map[string]any{"sha": "zz", "environment": "prod"}},
		"acceptance result": {"acceptance": map[string]any{"result": "maybe"}},
		"acceptance string": {"acceptance": "passed"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.PatchMetadata(context.Background(), link.TaskID, link.ID, patch)
			require.Error(t, err)
		})
	}
	assert.NotContains(t, string(link.Metadata), "deployed")
}

func TestPatchMetadata_DeliveryEdgeCases(t *testing.T) {
	svc, repo, link := patchMetaSetup(domain.VCSLinkTypePR)
	ctx := context.Background()
	bad := map[string]map[string]any{
		"ci bad sha":        {"ci_verdict": map[string]any{"status": "success", "sha": "zz"}},
		"ci negative id":    {"ci_verdict": map[string]any{"status": "success", "pipeline_id": -1}},
		"ci not object":     {"ci_verdict": "success"},
		"deployed bad env":  {"deployed": map[string]any{"sha": sha("3"), "environment": "Pr od!"}},
		"deployed not obj":  {"deployed": 5},
		"acceptance bad sh": {"acceptance": map[string]any{"result": "passed", "sha": "zz"}},
		"unknown key":       {"nope": "x"},
	}
	for name, patch := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := svc.PatchMetadata(ctx, link.TaskID, link.ID, patch)
			require.Error(t, err)
		})
	}

	// A value equal to the stored one is not rewritten: the same object comes back.
	ok := map[string]any{"ci_verdict": map[string]any{"status": "SUCCESS", "pipeline_id": 5, "sha": sha("3")}}
	first, err := svc.PatchMetadata(ctx, link.TaskID, link.ID, ok)
	require.NoError(t, err)
	assert.Equal(t, "success", first.CIVerdict().Status, "status is normalised")
	again, err := svc.PatchMetadata(ctx, link.TaskID, link.ID, ok)
	require.NoError(t, err)
	assert.JSONEq(t, string(first.Metadata), string(again.Metadata))

	// Another task's id never reaches the link.
	_, err = svc.PatchMetadata(ctx, link.TaskID, uuid.New(), ok)
	require.Error(t, err)
	_, err = svc.PatchMetadata(ctx, uuid.New(), link.ID, ok)
	require.Error(t, err)
	_ = repo
}

type failingGetRepo struct {
	*fakeVCSLinkRepo
	err error
}

func (r failingGetRepo) GetByID(context.Context, uuid.UUID) (*domain.VCSLink, error) {
	return nil, r.err
}

func TestPatchMetadata_GetErrorIsWrapped(t *testing.T) {
	boom := errors.New("db down")
	svc := NewVCSLinkService(failingGetRepo{newFakeVCSLinkRepo(), boom})
	_, err := svc.PatchMetadata(context.Background(), uuid.New(), uuid.New(), map[string]any{"merged_sha": sha("1")})
	require.ErrorIs(t, err, boom)
}

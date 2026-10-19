package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func (r *fakeVCSLinkRepo) MergeMetadata(_ context.Context, taskID, id uuid.UUID, patch json.RawMessage) (*domain.VCSLink, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.links[id]
	if l == nil || l.TaskID != taskID || l.LinkType != domain.VCSLinkTypePR {
		return nil, nil
	}
	merged := map[string]json.RawMessage{}
	_ = json.Unmarshal(l.Metadata, &merged)
	var p map[string]json.RawMessage
	_ = json.Unmarshal(patch, &p)
	for k, v := range p {
		merged[k] = v
	}
	l.Metadata, _ = json.Marshal(merged)
	cp := *l
	return &cp, nil
}

func patchMetaSetup(linkType domain.VCSLinkType) (*vcsLinkService, *fakeVCSLinkRepo, *domain.VCSLink) {
	repo := newFakeVCSLinkRepo()
	link := &domain.VCSLink{
		ID: uuid.New(), TaskID: uuid.New(), LinkType: linkType,
		Metadata: json.RawMessage(`{"head_sha":"` + strings.Repeat("a", 40) + `","role":"review"}`),
	}
	repo.links[link.ID] = link
	return NewVCSLinkService(repo).(*vcsLinkService), repo, link
}

func TestPatchMetadata_MergesWithoutClobberingOtherKeys(t *testing.T) {
	svc, _, link := patchMetaSetup(domain.VCSLinkTypePR)
	ship := strings.Repeat("b", 40)

	got, err := svc.PatchMetadata(context.Background(), link.TaskID, link.ID, map[string]any{"ship_sha": ship})
	require.NoError(t, err)

	var meta map[string]string
	require.NoError(t, json.Unmarshal(got.Metadata, &meta))
	assert.Equal(t, ship, meta["ship_sha"])
	assert.Equal(t, strings.Repeat("a", 40), meta["head_sha"], "head_sha must survive")
	assert.Equal(t, "review", meta["role"], "role must survive")
}

func TestPatchMetadata_RejectsBadInput(t *testing.T) {
	svc, _, link := patchMetaSetup(domain.VCSLinkTypePR)
	ctx := context.Background()
	for name, patch := range map[string]map[string]any{
		"head_sha":      {"head_sha": strings.Repeat("c", 40)},
		"foreign key":   {"role": "x"},
		"mixed":         {"ship_sha": strings.Repeat("b", 40), "head_sha": strings.Repeat("c", 40)},
		"short sha":     {"ship_sha": "abc123"},
		"uppercase sha": {"ship_sha": strings.Repeat("B", 40)},
		"non-hex sha":   {"ship_sha": strings.Repeat("z", 40)},
		"non-string":    {"ship_sha": 123},
		"empty":         {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.PatchMetadata(ctx, link.TaskID, link.ID, patch)
			var apiErr *apierror.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, http.StatusBadRequest, apiErr.Code)
		})
	}
	// Nothing was written by any rejected call.
	assert.NotContains(t, string(link.Metadata), "ship_sha")
	assert.Contains(t, string(link.Metadata), "head_sha")
}

func TestPatchMetadata_NotFound(t *testing.T) {
	ship := map[string]any{"ship_sha": strings.Repeat("b", 40)}
	ctx := context.Background()

	svc, _, link := patchMetaSetup(domain.VCSLinkTypePR)
	_, err := svc.PatchMetadata(ctx, uuid.New(), link.ID, ship)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr, "link on another task")
	assert.Equal(t, http.StatusNotFound, apiErr.Code)

	svc, _, commit := patchMetaSetup(domain.VCSLinkTypeCommit)
	_, err = svc.PatchMetadata(ctx, commit.TaskID, commit.ID, ship)
	require.ErrorAs(t, err, &apiErr, "non-pr link")
	assert.Equal(t, http.StatusNotFound, apiErr.Code)
}

type failingMergeRepo struct {
	*fakeVCSLinkRepo
	err error
}

func (r failingMergeRepo) MergeMetadata(context.Context, uuid.UUID, uuid.UUID, json.RawMessage) (*domain.VCSLink, error) {
	return nil, r.err
}

func TestPatchMetadata_RepoErrorIsWrappedNotSwallowed(t *testing.T) {
	boom := errors.New("db down")
	svc := NewVCSLinkService(failingMergeRepo{newFakeVCSLinkRepo(), boom})

	_, err := svc.PatchMetadata(context.Background(), uuid.New(), uuid.New(), map[string]any{"ship_sha": strings.Repeat("b", 40)})

	require.ErrorIs(t, err, boom)
}

func TestIsFullCommitSHA(t *testing.T) {
	assert.True(t, domain.IsFullCommitSHA(strings.Repeat("0123456789abcdef", 2)+"01234567"))
	assert.False(t, domain.IsFullCommitSHA(strings.Repeat("a", 39)))
	assert.False(t, domain.IsFullCommitSHA(strings.Repeat("a", 41)))
	assert.False(t, domain.IsFullCommitSHA(strings.Repeat("A", 40)))
	assert.False(t, domain.IsFullCommitSHA(strings.Repeat("g", 40)))
}

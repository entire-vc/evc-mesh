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

// The MR text of public repos carries no card id, so ship_sha has to be
// publishable from the MR url alone.
func TestPatchShipSHAByURL_ResolvesLinkWithoutCardID(t *testing.T) {
	repo := newFakeVCSLinkRepo()
	mine := &domain.VCSLink{ID: uuid.New(), TaskID: uuid.New(), Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "1372",
		URL:      "https://gitlab.example.invalid/entire-vc/evc-mesh/-/merge_requests/1372",
		Metadata: json.RawMessage(`{"head_sha":"` + strings.Repeat("a", 40) + `"}`)}
	other := &domain.VCSLink{ID: uuid.New(), TaskID: uuid.New(), Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "1372",
		URL: "https://gitlab.example.invalid/entire-vc/other/-/merge_requests/1372"}
	repo.links[mine.ID], repo.links[other.ID] = mine, other
	svc := NewVCSLinkService(repo).(*vcsLinkService)
	ship := strings.Repeat("b", 40)

	got, err := svc.PatchShipSHAByURL(context.Background(), uuid.Nil, mine.URL+"/", ship)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, mine.ID, got[0].ID)
	assert.Contains(t, string(repo.links[mine.ID].Metadata), ship)
	assert.Contains(t, string(repo.links[mine.ID].Metadata), strings.Repeat("a", 40), "head_sha must survive")
	assert.NotContains(t, string(repo.links[other.ID].Metadata), ship, "same number in another project must not match")
}

func TestPatchShipSHAByURL_RejectsBadInput(t *testing.T) {
	svc := NewVCSLinkService(newFakeVCSLinkRepo()).(*vcsLinkService)
	ctx := context.Background()
	ok := strings.Repeat("b", 40)
	for name, c := range map[string]struct {
		url, sha string
		code     int
	}{
		"unknown mr":   {"https://gitlab.example.invalid/a/b/-/merge_requests/9", ok, http.StatusNotFound},
		"bad sha":      {"https://gitlab.example.invalid/a/b/-/merge_requests/9", "abc", http.StatusBadRequest},
		"no number":    {"https://gitlab.example.invalid/a/b", ok, http.StatusBadRequest},
		"relative url": {"/merge_requests/9", ok, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.PatchShipSHAByURL(ctx, uuid.Nil, c.url, c.sha)
			var apiErr *apierror.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, c.code, apiErr.Code)
		})
	}
}

type listErrVCSRepo struct{ *fakeVCSLinkRepo }

func (listErrVCSRepo) ListByExternalID(context.Context, domain.VCSProvider, domain.VCSLinkType, string) ([]domain.VCSLink, error) {
	return nil, errors.New("db down")
}

type mergeErrVCSRepo struct{ *fakeVCSLinkRepo }

func (mergeErrVCSRepo) MergeMetadata(context.Context, uuid.UUID, uuid.UUID, json.RawMessage) (*domain.VCSLink, error) {
	return nil, errors.New("db down")
}

func TestPatchShipSHAByURL_RepoErrorsSurface(t *testing.T) {
	mrURL := "https://gitlab.example.invalid/a/b/-/merge_requests/9"
	ship := strings.Repeat("b", 40)

	_, err := NewVCSLinkService(listErrVCSRepo{newFakeVCSLinkRepo()}).PatchShipSHAByURL(context.Background(), uuid.Nil, mrURL, ship)
	require.Error(t, err)

	base := newFakeVCSLinkRepo()
	l := &domain.VCSLink{ID: uuid.New(), TaskID: uuid.New(), Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "9", URL: mrURL}
	base.links[l.ID] = l
	_, err = NewVCSLinkService(mergeErrVCSRepo{base}).PatchShipSHAByURL(context.Background(), uuid.Nil, mrURL, ship)
	require.Error(t, err)
}

// A retried publish with the SHA already stored must not hit the repo; a new
// SHA (re-review after a push) must still overwrite.
func TestPatchShipSHAByURL_SameSHAIsNoWriteNewSHAOverwrites(t *testing.T) {
	mrURL := "https://gitlab.example.invalid/a/b/-/merge_requests/9"
	old, fresh := strings.Repeat("b", 40), strings.Repeat("c", 40)
	base := newFakeVCSLinkRepo()
	l := &domain.VCSLink{ID: uuid.New(), TaskID: uuid.New(), Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "9", URL: mrURL,
		Metadata: json.RawMessage(`{"ship_sha":"` + old + `"}`)}
	base.links[l.ID] = l

	got, err := NewVCSLinkService(mergeErrVCSRepo{base}).PatchShipSHAByURL(context.Background(), uuid.Nil, mrURL, old)
	require.NoError(t, err, "same sha must not reach MergeMetadata")
	require.Len(t, got, 1)

	got, err = NewVCSLinkService(base).PatchShipSHAByURL(context.Background(), uuid.Nil, mrURL, fresh)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Contains(t, string(base.links[l.ID].Metadata), fresh)
	assert.NotContains(t, string(base.links[l.ID].Metadata), old)
}

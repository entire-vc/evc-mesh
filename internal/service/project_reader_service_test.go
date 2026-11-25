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
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// recordingReaderRepo stands in for the issuing transaction
// (AgentRepo.CreateProjectReader, covered against Postgres in
// internal/middleware/project_reader_db_test.go).
type recordingReaderRepo struct {
	got []*domain.Agent
	err error
}

func (r *recordingReaderRepo) CreateProjectReader(_ context.Context, a *domain.Agent) error {
	if r.err != nil {
		return r.err
	}
	r.got = append(r.got, a)
	return nil
}

type readerServiceFixture struct {
	ws      *domain.Workspace
	wsRepo  *MockWorkspaceRepository
	users   *MockUserRepository
	agents  *MockAgentRepository
	readers *recordingReaderRepo
	svc     *ProjectReaderService
}

func newReaderServiceFixture() *readerServiceFixture {
	f := &readerServiceFixture{
		ws:      &domain.Workspace{ID: uuid.New(), Name: "Acme", Slug: "acme"},
		wsRepo:  NewMockWorkspaceRepository(),
		users:   NewMockUserRepository(),
		agents:  NewMockAgentRepository(),
		readers: &recordingReaderRepo{},
	}
	f.wsRepo.items[f.ws.ID] = f.ws
	f.svc = NewProjectReaderService(f.wsRepo, f.users, f.agents, f.readers)
	return f
}

func (f *readerServiceFixture) input(name string) IssueProjectReaderInput {
	return IssueProjectReaderInput{WorkspaceID: f.ws.ID, ProjectID: uuid.New(), Name: name}
}

func readerAPIStatus(t *testing.T, err error) int {
	t.Helper()
	var ae *apierror.Error
	require.True(t, errors.As(err, &ae), "not an api error: %v", err)
	return ae.Code
}

func TestProjectReaderService_IssueStoresTheReaderProfile(t *testing.T) {
	f := newReaderServiceFixture()
	issuer := uuid.New()
	in := f.input("  Consent Reader  ")
	in.IssuedBy = &issuer

	out, err := f.svc.Issue(context.Background(), in)
	require.NoError(t, err)
	require.Len(t, f.readers.got, 1, "the issuing transaction was not called exactly once")
	a := f.readers.got[0]
	assert.Same(t, out.Agent, a)

	require.NotNil(t, a.ReaderProjectID)
	assert.Equal(t, in.ProjectID, *a.ReaderProjectID)
	assert.Equal(t, f.ws.ID, a.WorkspaceID)
	assert.Equal(t, "Consent Reader", a.Name, "name is trimmed")
	assert.Equal(t, slugify("Consent Reader"), a.Slug)
	assert.Equal(t, &issuer, a.SupervisorUserID)
	assert.Equal(t, ProjectReaderRole, a.Role)
	assert.Equal(t, domain.AgentTypeCustom, a.AgentType)
	assert.JSONEq(t, `{"no_lane":true}`, string(a.Capabilities))
	assert.JSONEq(t, `[]`, string(a.AcceptsFrom))
	assert.False(t, a.MentionWakes)
	assert.Zero(t, a.MaxConcurrentTasks)
	require.NotNil(t, a.ExpiresAt)

	// The key is answered once; only its hash and digest are kept.
	require.NotEmpty(t, out.APIKey)
	assert.NotContains(t, a.APIKeyHash, out.APIKey)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(a.APIKeyHash), bcryptInput(out.APIKey)))
	assert.Equal(t, agentKeyDigest(out.APIKey), a.APIKeySHA256)
	assert.Equal(t, extractPrefix(out.APIKey, f.ws.Slug), a.APIKeyPrefix)
	assert.True(t, strings.HasPrefix(out.APIKey, "agk_"+f.ws.Slug+"_"), "the key is not a key of this workspace")

	raw, err := json.Marshal(out.Agent)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"reader_project_id":"`+in.ProjectID.String()+`"`)
	assert.NotContains(t, string(raw), out.APIKey, "the agent JSON carries the raw key")
}

func TestProjectReaderService_IssueRefusesAndWritesNothing(t *testing.T) {
	boom := errors.New("db down")
	cases := []struct {
		name   string
		mutate func(f *readerServiceFixture, in *IssueProjectReaderInput)
		status int
		err    error
	}{
		{"blank name", func(_ *readerServiceFixture, in *IssueProjectReaderInput) { in.Name = "   " }, http.StatusBadRequest, nil},
		{"no project", func(_ *readerServiceFixture, in *IssueProjectReaderInput) { in.ProjectID = uuid.Nil }, http.StatusBadRequest, nil},
		{"unknown workspace", func(_ *readerServiceFixture, in *IssueProjectReaderInput) { in.WorkspaceID = uuid.New() }, http.StatusNotFound, nil},
		{"workspace lookup fails", func(f *readerServiceFixture, _ *IssueProjectReaderInput) { f.wsRepo.errToReturn = boom }, 0, boom},
		{"handle taken by a user", func(f *readerServiceFixture, in *IssueProjectReaderInput) {
			f.users.AddUser(f.ws.ID, &domain.User{ID: uuid.New(), Username: slugify(in.Name)})
		}, http.StatusConflict, nil},
		{"user lookup fails", func(f *readerServiceFixture, _ *IssueProjectReaderInput) { f.users.errToReturn = boom }, http.StatusInternalServerError, nil},
		{"handle taken by an agent", func(f *readerServiceFixture, in *IssueProjectReaderInput) {
			id := uuid.New()
			f.agents.items[id] = &domain.Agent{ID: id, WorkspaceID: f.ws.ID, Slug: slugify(in.Name)}
		}, http.StatusConflict, nil},
		{"agent lookup fails", func(f *readerServiceFixture, _ *IssueProjectReaderInput) { f.agents.errToReturn = boom }, http.StatusInternalServerError, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReaderServiceFixture()
			in := f.input("Consent Reader")
			tc.mutate(f, &in)
			out, err := f.svc.Issue(context.Background(), in)
			require.Error(t, err)
			assert.Nil(t, out)
			if tc.err != nil {
				assert.ErrorIs(t, err, tc.err)
			} else {
				assert.Equal(t, tc.status, readerAPIStatus(t, err), "%v", err)
			}
			assert.Empty(t, f.readers.got, "a refused issue reached the issuing transaction")
		})
	}
}

func TestProjectReaderService_IssuePropagatesTheTransactionError(t *testing.T) {
	f := newReaderServiceFixture()
	f.readers.err = apierror.NotFound("Project")
	out, err := f.svc.Issue(context.Background(), f.input("Consent Reader"))
	require.Error(t, err)
	assert.Nil(t, out, "a key was answered for a reader that was not written")
	assert.Equal(t, http.StatusNotFound, readerAPIStatus(t, err))
}

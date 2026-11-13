package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func TestAuthorizeMemoryArchive_UserRoles(t *testing.T) {
	project := uuid.New()
	owner := uuid.New()
	writes := func(uuid.UUID) bool { return true }
	refuses := func(uuid.UUID) bool { return false }

	projectMem := &domain.Memory{Scope: domain.ScopeProject, ProjectID: &project}
	workspaceMem := &domain.Memory{Scope: domain.ScopeWorkspace}
	agentMem := &domain.Memory{Scope: domain.ScopeAgent, AgentID: &owner}

	cases := []struct {
		name  string
		mem   *domain.Memory
		actor MemoryArchiveActor
		ok    bool
	}{
		{"project: member user with project access", projectMem, MemoryArchiveActor{UserRole: domain.RoleMember, MayWriteProject: writes}, true},
		{"project: viewer user is refused", projectMem, MemoryArchiveActor{UserRole: domain.RoleViewer, MayWriteProject: writes}, false},
		{"project: member user without project access", projectMem, MemoryArchiveActor{UserRole: domain.RoleMember, MayWriteProject: refuses}, false},
		{"project: no access check refuses", projectMem, MemoryArchiveActor{UserRole: domain.RoleOwner}, false},
		{"workspace: admin user", workspaceMem, MemoryArchiveActor{UserRole: domain.RoleAdmin}, true},
		{"workspace: member user is refused", workspaceMem, MemoryArchiveActor{UserRole: domain.RoleMember}, false},
		{"workspace: coordinator agent", workspaceMem, MemoryArchiveActor{AgentID: &owner, AgentRole: "Coordinator"}, true},
		{"workspace: CPO in a role list", workspaceMem, MemoryArchiveActor{AgentID: &owner, AgentRole: "product, CPO"}, true},
		{"workspace: 'leader' is not 'lead'", workspaceMem, MemoryArchiveActor{AgentID: &owner, AgentRole: "team leader"}, false},
		{"agent: a user is refused even as owner", agentMem, MemoryArchiveActor{UserRole: domain.RoleOwner}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := authorizeMemoryArchive(tc.mem, tc.actor)
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestValidateArchiveReason(t *testing.T) {
	r, err := validateArchiveReason("  merged long ago \n")
	require.NoError(t, err)
	require.Equal(t, "merged long ago", r)

	_, err = validateArchiveReason(" \t")
	require.Error(t, err)

	_, err = validateArchiveReason(strings.Repeat("ж", domain.MemoryArchiveReasonMaxLen))
	require.NoError(t, err, "the limit counts characters, not bytes")
	_, err = validateArchiveReason(strings.Repeat("ж", domain.MemoryArchiveReasonMaxLen+1))
	require.Error(t, err)
}

func TestSetMemoryArchived_RefusesBeforeWriting(t *testing.T) {
	project := uuid.New()
	member := MemoryArchiveActor{AgentID: ptrUUID(uuid.New()), MayWriteProject: func(uuid.UUID) bool { return true }}
	writes := 0
	repo := &mockMemoryRepo{
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			if id == uuid.Nil {
				return nil, errors.New("db down")
			}
			return &domain.Memory{ID: id, Scope: domain.ScopeProject, ProjectID: &project}, nil
		},
		setArchivedWithRevisionFn: func(context.Context, uuid.UUID, bool, string, *uuid.UUID, *uuid.UUID) (*domain.MemoryArchiveResult, error) {
			writes++
			return nil, nil
		},
	}
	svc := NewMemoryService(repo, &mockMemoryEdgeRepo{}, nil).(MemoryArchiveService)
	ctx := context.Background()

	_, err := svc.SetMemoryArchived(ctx, uuid.New(), true, "  ", member)
	require.Error(t, err, "blank reason")

	_, err = svc.SetMemoryArchived(ctx, uuid.Nil, true, "stale", member)
	require.ErrorContains(t, err, "db down")

	refused := member
	refused.MayWriteProject = func(uuid.UUID) bool { return false }
	_, err = svc.SetMemoryArchived(ctx, uuid.New(), true, "stale", refused)
	require.Error(t, err, "no project write access")
	require.Zero(t, writes, "nothing is written before validation and authorization pass")

	// The row vanished between the read and the locked update: 404, not a nil result.
	_, err = svc.SetMemoryArchived(ctx, uuid.New(), true, "stale", member)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 404, apiErr.Code)
	require.Equal(t, 1, writes)
}

func TestSetMemoryArchived_PassesTrimmedReasonAndActor(t *testing.T) {
	project := uuid.New()
	agent := uuid.New()
	var gotReason string
	var gotActor, gotUser *uuid.UUID
	repo := &mockMemoryRepo{
		getByIDFn: func(_ context.Context, id uuid.UUID) (*domain.Memory, error) {
			return &domain.Memory{ID: id, Scope: domain.ScopeProject, ProjectID: &project}, nil
		},
		setArchivedWithRevisionFn: func(_ context.Context, id uuid.UUID, archived bool, reason string, actor, user *uuid.UUID) (*domain.MemoryArchiveResult, error) {
			gotReason, gotActor, gotUser = reason, actor, user
			return &domain.MemoryArchiveResult{Memory: &domain.Memory{ID: id, Archived: archived}, Changed: true}, nil
		},
	}
	svc := NewMemoryService(repo, &mockMemoryEdgeRepo{}, nil).(MemoryArchiveService)
	res, err := svc.SetMemoryArchived(context.Background(), uuid.New(), true, "  merged long ago ",
		MemoryArchiveActor{AgentID: &agent, MayWriteProject: func(p uuid.UUID) bool { return p == project }})
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.True(t, res.Memory.Archived)
	require.Equal(t, "merged long ago", gotReason)
	require.Equal(t, &agent, gotActor)
	require.Nil(t, gotUser)

	// A user caller is recorded as the user actor.
	userID := uuid.New()
	_, err = svc.SetMemoryArchived(context.Background(), uuid.New(), false, "back", MemoryArchiveActor{UserID: &userID, UserRole: domain.RoleMember,
		MayWriteProject: func(uuid.UUID) bool { return true }})
	require.NoError(t, err)
	require.Nil(t, gotActor)
	require.Equal(t, &userID, gotUser)

	repo.setArchivedWithRevisionFn = func(context.Context, uuid.UUID, bool, string, *uuid.UUID, *uuid.UUID) (*domain.MemoryArchiveResult, error) {
		return nil, errors.New("tx aborted")
	}
	_, err = svc.SetMemoryArchived(context.Background(), uuid.New(), false, "back", MemoryArchiveActor{AgentID: &agent, MayWriteProject: func(uuid.UUID) bool { return true }})
	require.ErrorContains(t, err, "tx aborted")
}

func TestListMemoriesByKey(t *testing.T) {
	var got domain.MemoryKeyLookup
	repo := &mockMemoryRepo{listByKeyFn: func(_ context.Context, l domain.MemoryKeyLookup) ([]domain.Memory, error) {
		got = l
		return nil, nil
	}}
	svc := NewMemoryService(repo, &mockMemoryEdgeRepo{}, nil).(MemoryArchiveService)
	ctx := context.Background()

	_, err := svc.ListMemoriesByKey(ctx, domain.MemoryKeyLookup{Key: " "})
	require.Error(t, err)

	rows, err := svc.ListMemoriesByKey(ctx, domain.MemoryKeyLookup{Key: "k", IncludeArchived: true, IncludeExpired: true})
	require.NoError(t, err)
	require.NotNil(t, rows, "an empty result is [], never null")
	require.Empty(t, rows)
	require.True(t, got.IncludeArchived && got.IncludeExpired, "flags reach the repo")

	repo.listByKeyFn = func(context.Context, domain.MemoryKeyLookup) ([]domain.Memory, error) { return nil, errors.New("boom") }
	_, err = svc.ListMemoriesByKey(ctx, domain.MemoryKeyLookup{Key: "k"})
	require.ErrorContains(t, err, "boom")
}

// A remember on a hand-archived key updates that row and keeps it archived,
// with a hint naming the way back; it never creates a live duplicate.
func TestRemember_HandArchivedKeyStaysArchived(t *testing.T) {
	wsID := uuid.New()
	archived := baseMemory(wsID)
	archived.Archived = true
	var upserted *domain.Memory
	repo := &mockMemoryRepo{
		getByKeyFn: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID, string, domain.MemoryScope) (*domain.Memory, error) {
			return nil, nil
		},
		getHandArchivedByKeyFn: func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID, string, domain.MemoryScope) (*domain.Memory, error) {
			return archived, nil
		},
		upsertFn: func(_ context.Context, m *domain.Memory) error { upserted = m; return nil },
	}
	mem := baseMemory(wsID)
	res, err := newMemoryService(repo).Remember(context.Background(), mem, domain.MemoryWriteIntent{})
	require.NoError(t, err)
	require.Equal(t, "updated", res.Outcome)
	require.True(t, res.Archived)
	require.Contains(t, res.Hint, "archive_memory memory_id="+archived.ID.String())
	require.Equal(t, archived.ID, upserted.ID, "the archived row is the one written")

	repo.getHandArchivedByKeyFn = func(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID, string, domain.MemoryScope) (*domain.Memory, error) {
		return nil, errors.New("lookup failed")
	}
	_, err = newMemoryService(repo).Remember(context.Background(), baseMemory(wsID), domain.MemoryWriteIntent{})
	require.ErrorContains(t, err, "lookup archived")
}

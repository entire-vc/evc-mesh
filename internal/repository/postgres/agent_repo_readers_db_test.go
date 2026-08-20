package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// No //go:build integration tag — same convention as the other *_db_test.go
// files here.
//
// These cover the four AgentRepo readers whose queries this change rewrote from
// `SELECT *` to an explicit column list: List, GetBySlug, SearchByPrefix and
// GetSubAgentTree. None of them had a test.
//
// The risk being covered is specific and invisible to the compiler. A column
// list is a string; nothing checks it against the table or against agentRow
// until a query runs. Name a column that does not exist and Postgres errors at
// runtime; omit one that agentRow expects and the field silently reads as its
// zero value; add one agentRow lacks and sqlx refuses the whole scan. All three
// are exactly the failure this PR is about — the previous `SELECT *` broke every
// one of these readers the moment migration 20260817092 landed, because sqlx
// will not scan a column with no matching struct field. So each test below
// asserts on FIELDS, not just on row counts: reading back a value is what proves
// the column was actually selected.

// seedAgentWorkspace creates a workspace and returns a builder for agents in it.
func seedAgentWorkspace(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	owner := &domain.User{
		ID: uuid.New(), Email: "readers-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Readers Owner", Username: "readers-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, owner))

	ws := &domain.Workspace{
		ID: uuid.New(), Name: "readers-ws", Slug: "readers-ws-" + suffix, OwnerID: owner.ID,
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))
	return ws.ID
}

// seedAgent creates an agent with every column this change touches populated to
// a DISTINCT, non-zero value, so a reader that silently drops a column shows up
// as a zero rather than passing.
func seedAgent(t *testing.T, db *sqlx.DB, wsID uuid.UUID, name, slug string, parent *uuid.UUID) *domain.Agent {
	t.Helper()
	suffix := uuid.New().String()[:8]
	agent := &domain.Agent{
		ID:            uuid.New(),
		WorkspaceID:   wsID,
		ParentAgentID: parent,
		Name:          name,
		Slug:          slug,
		AgentType:     domain.AgentTypeClaudeCode,
		APIKeyHash:    "$2a$12$hash-" + suffix,
		APIKeySHA256:  "digest-" + suffix,
		APIKeyPrefix:  suffix,
		Status:        domain.AgentStatusOffline,
		Role:          "developer",
		CallbackURL:   "https://example.com/cb/" + suffix,
	}
	require.NoError(t, NewAgentRepo(db).Create(context.Background(), agent))
	return agent
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestAgentRepoList_ReturnsFullyPopulatedRows(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	seeded := seedAgent(t, db, wsID, "List Agent", "list-agent-"+uuid.New().String()[:8], nil)

	page, err := NewAgentRepo(db).List(context.Background(), wsID,
		repository.AgentFilter{}, pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)

	got := page.Items[0]
	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, seeded.Name, got.Name)
	// The columns the explicit list has to carry, each read back by value: a
	// dropped column would arrive empty here and nowhere else.
	assert.Equal(t, seeded.APIKeyHash, got.APIKeyHash)
	assert.Equal(t, seeded.APIKeySHA256, got.APIKeySHA256,
		"the digest must survive a list read, or a read-modify-write blanks it")
	assert.Equal(t, seeded.APIKeyPrefix, got.APIKeyPrefix)
	assert.Equal(t, seeded.CallbackURL, got.CallbackURL)
	assert.Equal(t, "developer", got.Role)
	assert.Equal(t, domain.AgentTypeClaudeCode, got.AgentType)
}

func TestAgentRepoList_ScopesToTheWorkspace(t *testing.T) {
	db := agentDigestTestDB(t)
	mine := seedAgentWorkspace(t, db)
	theirs := seedAgentWorkspace(t, db)
	seedAgent(t, db, mine, "Mine", "mine-"+uuid.New().String()[:8], nil)
	seedAgent(t, db, theirs, "Theirs", "theirs-"+uuid.New().String()[:8], nil)

	page, err := NewAgentRepo(db).List(context.Background(), mine,
		repository.AgentFilter{}, pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "Mine", page.Items[0].Name)
}

func TestAgentRepoList_PaginatesAndReportsTheTotal(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	for i := 0; i < 5; i++ {
		seedAgent(t, db, wsID, fmt.Sprintf("Agent %d", i),
			fmt.Sprintf("paged-%d-%s", i, uuid.New().String()[:8]), nil)
	}

	page, err := NewAgentRepo(db).List(context.Background(), wsID,
		repository.AgentFilter{}, pagination.Params{Page: 1, PageSize: 2})
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
	assert.Equal(t, 5, page.TotalCount, "the count must not be limited by the page")
}

// ---------------------------------------------------------------------------
// GetBySlug
// ---------------------------------------------------------------------------

func TestAgentRepoGetBySlug_ReturnsFullyPopulatedRow(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	slug := "slug-agent-" + uuid.New().String()[:8]
	seeded := seedAgent(t, db, wsID, "Slug Agent", slug, nil)

	got, err := NewAgentRepo(db).GetBySlug(context.Background(), wsID, slug)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, seeded.APIKeyHash, got.APIKeyHash)
	assert.Equal(t, seeded.APIKeySHA256, got.APIKeySHA256)
	assert.Equal(t, seeded.APIKeyPrefix, got.APIKeyPrefix)
	assert.Equal(t, seeded.CallbackURL, got.CallbackURL)
}

// (nil, nil) rather than an error — the contract the callers branch on.
func TestAgentRepoGetBySlug_UnknownSlugIsNotAnError(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)

	got, err := NewAgentRepo(db).GetBySlug(context.Background(), wsID, "no-such-slug")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestAgentRepoGetBySlug_DoesNotCrossWorkspaces(t *testing.T) {
	db := agentDigestTestDB(t)
	mine := seedAgentWorkspace(t, db)
	theirs := seedAgentWorkspace(t, db)
	slug := "shared-slug-" + uuid.New().String()[:8]
	seedAgent(t, db, theirs, "Theirs", slug, nil)

	got, err := NewAgentRepo(db).GetBySlug(context.Background(), mine, slug)
	require.NoError(t, err)
	assert.Nil(t, got, "a slug in another workspace must not resolve")
}

// ---------------------------------------------------------------------------
// SearchByPrefix
// ---------------------------------------------------------------------------

func TestAgentRepoSearchByPrefix_MatchesAndPopulates(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	token := uuid.New().String()[:8]
	seeded := seedAgent(t, db, wsID, "Howard "+token, "howard-"+token, nil)
	seedAgent(t, db, wsID, "Unrelated", "unrelated-"+uuid.New().String()[:8], nil)

	got, err := NewAgentRepo(db).SearchByPrefix(context.Background(), wsID, "Howard "+token, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)

	assert.Equal(t, seeded.ID, got[0].ID)
	assert.Equal(t, seeded.APIKeySHA256, got[0].APIKeySHA256)
	assert.Equal(t, seeded.CallbackURL, got[0].CallbackURL)
}

// The ORDER BY puts an exact prefix match first; a plain substring match follows.
func TestAgentRepoSearchByPrefix_RanksPrefixMatchesFirst(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	token := uuid.New().String()[:8]

	seedAgent(t, db, wsID, "zzz-contains-"+token+"-inside",
		"contains-"+token+"-inside", nil)
	seedAgent(t, db, wsID, token+"-starts-here", token+"-starts-here", nil)

	got, err := NewAgentRepo(db).SearchByPrefix(context.Background(), wsID, token, 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, token+"-starts-here", got[0].Slug,
		"the agent whose name/slug STARTS with the query must come first")
}

func TestAgentRepoSearchByPrefix_HonoursTheLimit(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	token := uuid.New().String()[:8]
	for i := 0; i < 4; i++ {
		seedAgent(t, db, wsID, fmt.Sprintf("%s-%d", token, i), fmt.Sprintf("%s-%d", token, i), nil)
	}

	got, err := NewAgentRepo(db).SearchByPrefix(context.Background(), wsID, token, 2)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestAgentRepoSearchByPrefix_NoMatchIsEmptyNotAnError(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	seedAgent(t, db, wsID, "Somebody", "somebody-"+uuid.New().String()[:8], nil)

	got, err := NewAgentRepo(db).SearchByPrefix(context.Background(), wsID, "zzz-nothing-matches", 10)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// ---------------------------------------------------------------------------
// GetSubAgentTree
// ---------------------------------------------------------------------------

func TestAgentRepoGetSubAgentTree_ReturnsDescendantsWithTheDigest(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	suffix := uuid.New().String()[:8]

	root := seedAgent(t, db, wsID, "Root", "root-"+suffix, nil)
	child := seedAgent(t, db, wsID, "Child", "child-"+suffix, &root.ID)
	grandchild := seedAgent(t, db, wsID, "Grandchild", "grandchild-"+suffix, &child.ID)

	got, err := NewAgentRepo(db).GetSubAgentTree(context.Background(), root.ID)
	require.NoError(t, err)
	require.Len(t, got, 2, "both descendants, and not the root itself")

	byID := map[uuid.UUID]domain.Agent{}
	for _, a := range got {
		byID[a.ID] = a
	}
	require.Contains(t, byID, child.ID)
	require.Contains(t, byID, grandchild.ID)

	// The reason api_key_sha256 is in this query at all: a caller that reads an
	// agent from the tree, edits it and writes it back would otherwise blank the
	// digest and silently drop that agent to the bcrypt path.
	assert.Equal(t, child.APIKeySHA256, byID[child.ID].APIKeySHA256)
	assert.Equal(t, grandchild.APIKeySHA256, byID[grandchild.ID].APIKeySHA256)
	assert.Equal(t, child.APIKeyHash, byID[child.ID].APIKeyHash)
}

// Depth ordering is part of the contract: direct children before their own
// children.
func TestAgentRepoGetSubAgentTree_OrdersByDepth(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	suffix := uuid.New().String()[:8]

	root := seedAgent(t, db, wsID, "Root", "d-root-"+suffix, nil)
	child := seedAgent(t, db, wsID, "Child", "d-child-"+suffix, &root.ID)
	grandchild := seedAgent(t, db, wsID, "Grandchild", "d-grandchild-"+suffix, &child.ID)

	got, err := NewAgentRepo(db).GetSubAgentTree(context.Background(), root.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, child.ID, got[0].ID)
	assert.Equal(t, grandchild.ID, got[1].ID)
}

func TestAgentRepoGetSubAgentTree_LeafHasNoDescendants(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	leaf := seedAgent(t, db, wsID, "Leaf", "leaf-"+uuid.New().String()[:8], nil)

	got, err := NewAgentRepo(db).GetSubAgentTree(context.Background(), leaf.ID)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// A soft-deleted child drops out of the tree, and takes its own subtree with it.
func TestAgentRepoGetSubAgentTree_SkipsSoftDeleted(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	suffix := uuid.New().String()[:8]
	repo := NewAgentRepo(db)

	root := seedAgent(t, db, wsID, "Root", "s-root-"+suffix, nil)
	child := seedAgent(t, db, wsID, "Child", "s-child-"+suffix, &root.ID)
	seedAgent(t, db, wsID, "Grandchild", "s-grandchild-"+suffix, &child.ID)

	require.NoError(t, repo.Delete(context.Background(), child.ID))

	got, err := repo.GetSubAgentTree(context.Background(), root.ID)
	require.NoError(t, err)
	assert.Empty(t, got,
		"the recursive CTE walks down through the child, so deleting it removes its subtree too")
}

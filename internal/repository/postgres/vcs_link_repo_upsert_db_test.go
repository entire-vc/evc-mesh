package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// No //go:build integration tag, deliberately — same reasoning as
// memory_service_collapse_getbykey_db_test.go: CI's untagged `go test ./...`
// runs these against a migrated DATABASE_URL and skip()s rather than fails
// when no database is reachable.
//
// Regression test for #b73171fa: on the update branch (explicit status onto
// an existing (task_id, provider, link_type, external_id)), Upsert's SQL
// never touches id/created_at — real Postgres, not a fake, is what proves the
// RETURNING (xmax = 0) trick actually distinguishes insert from update and
// that the scanned-back id/created_at genuinely match what a fresh GetByID
// returns, not merely what the fakes were told to preserve.

func vcsLinkUpsertTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("Postgres at %s not accepting connections, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// vcsLinkTestTask creates the minimal workspace/project/status/task chain
// vcs_links.task_id's FK requires.
func vcsLinkTestTask(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	ws := &domain.Workspace{ID: uuid.New(), Name: "vcs-link-upsert-ws", Slug: "vcs-link-upsert-" + uuid.New().String()[:8], OwnerID: uuid.New()}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))

	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "vcs-link-upsert-proj",
		Slug: "vcs-link-upsert-p-" + uuid.New().String()[:8], DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, proj))

	status := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "Open", Slug: "open",
		Color: "#00FF00", Position: 0, Category: domain.StatusCategoryTodo, IsDefault: true,
	}
	require.NoError(t, NewTaskStatusRepo(db).Create(ctx, status))

	task := &domain.Task{
		ID: uuid.New(), ProjectID: proj.ID, StatusID: status.ID,
		Title: "vcs-link upsert regression task", AssigneeType: domain.AssigneeTypeUnassigned,
		Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	}
	require.NoError(t, NewTaskRepo(db).Create(ctx, task))

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM tasks WHERE id = $1", task.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM task_statuses WHERE id = $1", status.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", proj.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", ws.ID)
	})

	return task.ID
}

func TestVCSLinkRepo_Upsert_InsertReportsCreatedTrue(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	link := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "40",
		URL:    "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}

	created, err := repo.Upsert(ctx, link)
	require.NoError(t, err)
	assert.True(t, created, "a genuinely new (task,provider,link_type,external_id) must report created=true")
}

// A DB-level failure (here: task_id's FK to tasks(id), violated by a task
// that was never created) must surface as an error with created=false, not a
// false-positive success.
func TestVCSLinkRepo_Upsert_DBErrorReturnsFalseAndError(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)

	link := &domain.VCSLink{
		ID: uuid.New(), TaskID: uuid.New(), Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "40",
		URL:    "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}

	created, err := repo.Upsert(ctx, link)
	require.Error(t, err, "task_id must satisfy the FK to tasks(id)")
	assert.False(t, created)
}

// The core regression: the update branch must return the row that a
// subsequent GetByID actually finds — not the id/created_at the caller
// happened to generate before discovering the link already existed.
func TestVCSLinkRepo_Upsert_UpdateBranchReturnsActualPersistedRow(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	first := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "40",
		URL:    "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now().Add(-time.Hour),
	}
	created, err := repo.Upsert(ctx, first)
	require.NoError(t, err)
	require.True(t, created)
	originalID := first.ID
	originalCreatedAt := first.CreatedAt

	// Same (task_id, provider, link_type, external_id), a fresh id/created_at
	// the caller has no way to know will be discarded — exactly what
	// vcsLinkService.Create constructs on every call.
	second := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "40",
		URL:    "https://github.com/entire-vc/evc-mesh-mcp/pull/40",
		Status: domain.VCSLinkStatusMerged, CreatedAt: time.Now(),
	}
	require.NotEqual(t, originalID, second.ID, "test setup sanity: the caller-generated id must differ from the original")

	created, err = repo.Upsert(ctx, second)
	require.NoError(t, err)

	// RED before the fix: created was true and second.ID/CreatedAt were left
	// as the fresh, never-persisted values generated above.
	assert.False(t, created, "updating an existing row must report created=false")
	assert.Equal(t, originalID, second.ID, "Upsert must mutate the caller's link back to the row's REAL id")
	assert.Equal(t, originalCreatedAt.UTC().Truncate(time.Microsecond), second.CreatedAt.UTC().Truncate(time.Microsecond),
		"Upsert must mutate the caller's link back to the row's REAL created_at")

	// Equality against a fresh, independent GET — not just against what
	// Upsert echoed back — is the actual acceptance bar (#b73171fa AC1).
	stored, err := repo.GetByID(ctx, second.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, originalID, stored.ID)
	assert.Equal(t, domain.VCSLinkStatusMerged, stored.Status, "the update's new fields must still land")

	all, err := repo.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, all, 1, "no second row may exist under the same natural key")
}

// #0fbed572: re-linking with a CORRECTED provider (the fix path for a URL
// whose provider was originally mis-inferred, e.g. a self-hosted GitLab URL
// first recorded as github) must update the existing row in place, not
// insert a second one — the unique index is keyed on provider too, so the
// naive ON CONFLICT could never match across a provider change and used to
// leave two rows behind, one of which the done-evidence gate never reads.
func TestVCSLinkRepo_Upsert_ProviderCorrectionUpdatesInPlace(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	mrURL := "https://git.entire.host/entire-vc/team-relay-ops/-/merge_requests/14"
	wrong := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "14",
		URL:    mrURL,
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now().Add(-time.Hour),
	}
	created, err := repo.Upsert(ctx, wrong)
	require.NoError(t, err)
	require.True(t, created)
	originalID := wrong.ID

	corrected := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "14",
		URL:    mrURL,
		Status: domain.VCSLinkStatusMerged, CreatedAt: time.Now(),
	}
	created, err = repo.Upsert(ctx, corrected)
	require.NoError(t, err)
	assert.False(t, created, "correcting the provider must update the existing row, not insert a duplicate")
	assert.Equal(t, originalID, corrected.ID)

	all, err := repo.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, all, 1, "exactly one row must remain after the provider correction")
	assert.Equal(t, domain.VCSProviderGitLab, all[0].Provider, "the surviving row must carry the corrected provider")
	assert.Equal(t, domain.VCSLinkStatusMerged, all[0].Status)
}

// seedVCSLink inserts a row directly via SQL, bypassing repo Create/Upsert.
// Used to reproduce pre-existing corruption (#0fbed572 defect 1) that the
// FIXED repo methods can no longer produce on their own.
func seedVCSLink(t *testing.T, db *sqlx.DB, id, taskID uuid.UUID, provider domain.VCSProvider, linkType domain.VCSLinkType, externalID, url string, status domain.VCSLinkStatus, createdAt time.Time) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO vcs_links (id, task_id, provider, link_type, external_id, url, title, status, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, '', $7, '{}', $8)
	`, id, taskID, string(provider), string(linkType), externalID, url, string(status), createdAt)
	require.NoError(t, err)
}

// #0fbed572 defect 1 (found by Daedalus's independent review of the first
// attempt at this fix): a task already carrying TWO rows for the same MR —
// exactly the shape left behind by the OLD (task_id, provider, link_type,
// external_id)-only matching, which could never see across a provider
// correction — must be healed by the next Upsert, not rejected with a raw
// unique-constraint error. This is the live state #d742efd4 was actually in
// the day the bug was caught; seeded here via seedVCSLink because the fixed
// repo methods can no longer produce it on their own.
func TestVCSLinkRepo_Upsert_HealsPreexistingDuplicateSameURL(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	mrURL := "https://git.entire.host/entire-vc/team-relay-ops/-/merge_requests/14"
	staleID := uuid.New()
	freshID := uuid.New()
	seedVCSLink(t, db, staleID, taskID, domain.VCSProviderGitHub, domain.VCSLinkTypePR, "14", mrURL, domain.VCSLinkStatusOpen, time.Now().Add(-2*time.Hour))
	seedVCSLink(t, db, freshID, taskID, domain.VCSProviderGitLab, domain.VCSLinkTypePR, "14", mrURL, domain.VCSLinkStatusMerged, time.Now().Add(-time.Hour))

	seeded, err := repo.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, seeded, 2, "test setup sanity: both pre-existing dupe rows must be present")

	relink := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "14",
		URL:    mrURL,
		Status: domain.VCSLinkStatusMerged, CreatedAt: time.Now(),
	}
	created, err := repo.Upsert(ctx, relink)
	require.NoError(t, err, "healing a pre-existing duplicate must not surface a raw unique-constraint error")
	assert.False(t, created)
	assert.Equal(t, staleID, relink.ID, "the OLDEST row must survive")

	all, err := repo.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, all, 1, "the duplicate must be merged away, not left behind")
	assert.Equal(t, staleID, all[0].ID)
	assert.Equal(t, domain.VCSProviderGitLab, all[0].Provider)
	assert.Equal(t, domain.VCSLinkStatusMerged, all[0].Status)
}

// #0fbed572 defect 2: a GitHub PR and a GitLab MR that happen to share the
// same external_id NUMBER are different objects — matching by url must
// never collapse them into one row. Regression guard for the shape of fix
// that matched by (task_id, link_type, external_id) alone: the second
// Upsert there silently updated — and thereby erased — the first link.
func TestVCSLinkRepo_Upsert_DifferentProvidersSameExternalIDStayDistinct(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	githubLink := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "14",
		URL:    "https://github.com/entire-vc/team-relay-ops/pull/14",
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now().Add(-time.Hour),
	}
	created, err := repo.Upsert(ctx, githubLink)
	require.NoError(t, err)
	require.True(t, created)

	gitlabLink := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "14",
		URL:    "https://git.entire.host/entire-vc/team-relay-ops/-/merge_requests/14",
		Status: domain.VCSLinkStatusMerged, CreatedAt: time.Now(),
	}
	created, err = repo.Upsert(ctx, gitlabLink)
	require.NoError(t, err)
	assert.True(t, created, "a different-url object sharing only external_id must insert as a NEW row, not merge")

	all, err := repo.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, all, 2, "the github link must NOT have been silently erased")

	stillGithub, err := repo.GetByID(ctx, githubLink.ID)
	require.NoError(t, err)
	require.NotNil(t, stillGithub, "the original github row must still exist")
	assert.Equal(t, domain.VCSLinkStatusOpen, stillGithub.Status, "and must not have been silently overwritten")
}

// #0fbed572 defect 3: after a corrected provider inference, a plain
// add_vcs_link with no status (repo.Create, not Upsert) must refuse — not
// silently insert a second row — when a link for the same url already
// exists under a different provider. Before this fix the unique index
// (task_id, provider, link_type, external_id) simply didn't fire here: the
// provider differs, so nothing blocked the insert.
func TestVCSLinkRepo_Create_RefusesDuplicateAcrossProviderCorrection(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	mrURL := "https://git.entire.host/entire-vc/team-relay-ops/-/merge_requests/14"
	wrong := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "14",
		URL: mrURL, Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	}
	require.NoError(t, repo.Create(ctx, wrong))

	corrected := &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "14",
		URL: mrURL, Status: "", CreatedAt: time.Now(),
	}
	err := repo.Create(ctx, corrected)
	require.Error(t, err, "a plain Create on an already-linked url must refuse, not silently duplicate")
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 409, apiErr.StatusCode())

	all, err := repo.ListByTask(ctx, taskID)
	require.NoError(t, err)
	require.Len(t, all, 1, "the refused Create must not have inserted a second row")
	assert.Equal(t, wrong.ID, all[0].ID)
}

package postgres

import (
	"context"
	"os"
	"testing"

	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No //go:build integration tag, deliberately — same convention as the other
// *_db_test.go files here. CI's untagged `go test ./...` runs these against a
// migrated DATABASE_URL and skip()s when no database is reachable.
//
// This file and its *_coverage_db_test.go siblings exist because this change
// rewrote every unsafe `SELECT *` in this package into an explicit column list,
// and a column list is just a string: nothing checks it against the table or
// against the scan target until a query actually runs. Three distinct failures
// hide behind a clean compile —
//
//   - name a column that does not exist  → Postgres errors at runtime,
//   - omit one the row struct expects    → the field silently reads as zero,
//   - add one the row struct lacks       → sqlx refuses the entire scan.
//
// Only a real query against a real table can tell them apart, so every test in
// these files asserts on FIELD VALUES read back from Postgres, never merely on
// "no error" or on a row count. Reading a distinctive seeded value back is what
// proves the column was actually in the SELECT list.
//
// Seeding goes through each repository's own Create/Upsert method rather than
// raw SQL, so the write path is exercised alongside the read path and the rows
// are shaped exactly as production writes them.

// selectStarCoverageTestDB connects to the test database, skipping the test
// when none is reachable. Named distinctly from agentDigestTestDB and friends
// to avoid a redeclaration in this package.
func selectStarCoverageTestDB(t *testing.T) *sqlx.DB {
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

// coverageFixture is the FK chain almost every table in this package hangs
// off: user → workspace → project → task_status → task, plus an agent in the
// same workspace. Each field is a real row, so anything referencing them
// satisfies its foreign keys.
type coverageFixture struct {
	// suffix is a short random string, unique per fixture, for slugs and other
	// values under a UNIQUE constraint.
	suffix string

	userID      uuid.UUID
	workspaceID uuid.UUID
	projectID   uuid.UUID
	statusID    uuid.UUID
	taskID      uuid.UUID
	agentID     uuid.UUID

	// projectSlug and workspaceSlug are kept so slug-keyed readers
	// (GetBySlug) can be asserted without a second round trip.
	projectSlug   string
	workspaceSlug string
}

// newCoverageFixture builds a complete, independent FK chain. Every call
// produces a fresh workspace, so tests never see each other's rows and can run
// in any order against a shared database.
func newCoverageFixture(t *testing.T, db *sqlx.DB) coverageFixture {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	user := &domain.User{
		ID:           uuid.New(),
		Email:        "cover-" + suffix + "@example.com",
		PasswordHash: "x",
		Name:         "Coverage Owner",
		Username:     "cover-" + suffix,
		IsActive:     true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, user))

	// Both Create methods write created_at/updated_at from the struct verbatim
	// rather than letting the column default fire, so a zero-value time really
	// does land in the row. Set them, otherwise "was created_at selected?" is
	// indistinguishable from "was created_at inserted as year 1?".
	now := time.Now()

	ws := &domain.Workspace{
		ID:        uuid.New(),
		Name:      "coverage workspace " + suffix,
		Slug:      "cover-ws-" + suffix,
		OwnerID:   user.ID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))

	proj := &domain.Project{
		ID:                  uuid.New(),
		WorkspaceID:         ws.ID,
		Name:                "coverage project " + suffix,
		Description:         "seeded by newCoverageFixture",
		Slug:                "cover-proj-" + suffix,
		Icon:                "rocket",
		DefaultAssigneeType: domain.DefaultAssigneeNone,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, proj))

	status := &domain.TaskStatus{
		ID:        uuid.New(),
		ProjectID: proj.ID,
		Name:      "Coverage Todo",
		Slug:      "cover-todo",
		Color:     "#1A2B3C",
		Position:  0,
		Category:  domain.StatusCategoryTodo,
		IsDefault: true,
	}
	require.NoError(t, NewTaskStatusRepo(db).Create(ctx, status))

	agent := &domain.Agent{
		ID:           uuid.New(),
		WorkspaceID:  ws.ID,
		Name:         "Coverage Agent",
		Slug:         "cover-agent-" + suffix,
		AgentType:    domain.AgentTypeClaudeCode,
		Status:       domain.AgentStatusOffline,
		APIKeyHash:   "$2a$12$cover-" + suffix,
		APIKeyPrefix: suffix,
		Role:         "developer",
	}
	require.NoError(t, NewAgentRepo(db).Create(ctx, agent))

	fx := coverageFixture{
		suffix:        suffix,
		userID:        user.ID,
		workspaceID:   ws.ID,
		projectID:     proj.ID,
		statusID:      status.ID,
		agentID:       agent.ID,
		projectSlug:   proj.Slug,
		workspaceSlug: ws.Slug,
	}
	fx.taskID = newCoverageTask(t, db, fx, "coverage task")
	return fx
}

// newCoverageTask adds another task to the fixture's project. Returns its ID.
func newCoverageTask(t *testing.T, db *sqlx.DB, fx coverageFixture, title string) uuid.UUID {
	t.Helper()
	task := &domain.Task{
		ID:            uuid.New(),
		ProjectID:     fx.projectID,
		StatusID:      fx.statusID,
		Title:         title,
		Description:   "seeded for select-list coverage",
		AssigneeType:  domain.AssigneeTypeUnassigned,
		Priority:      domain.PriorityMedium,
		CreatedBy:     fx.userID,
		CreatedByType: domain.ActorTypeUser,
	}
	require.NoError(t, NewTaskRepo(db).Create(context.Background(), task))
	return task.ID
}

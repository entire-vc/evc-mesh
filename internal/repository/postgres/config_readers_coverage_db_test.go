package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Readers on the per-project and per-workspace configuration tables —
// custom_field_definitions, saved_views, integration_configs, webhook_configs
// and webhook_deliveries. Every query below was rewritten from `SELECT *` by
// this change and had no coverage at this package's level.
//
// See select_star_coverage_fixtures_db_test.go for why these assert on field
// values rather than on row counts.

// ---------------------------------------------------------------------------
// custom_field_repo.go: GetByID, ListByProject, ListVisibleToAgents
// ---------------------------------------------------------------------------

// seedCustomField writes one custom_field_definitions row. The slug is
// constrained to ^[a-z0-9_]{1,100}$, so the fixture's hyphenated suffix is
// folded to underscores.
func seedCustomField(t *testing.T, db *sqlx.DB, fx coverageFixture, slug string, visibleToAgents bool, position int) *domain.CustomFieldDefinition {
	t.Helper()
	f := &domain.CustomFieldDefinition{
		ID:                uuid.New(),
		ProjectID:         fx.projectID,
		Name:              "Field " + slug,
		Slug:              strings.ReplaceAll(slug, "-", "_"),
		FieldType:         domain.FieldTypeSelect,
		Description:       "description for " + slug,
		Options:           json.RawMessage(`{"choices": ["a", "b"]}`),
		DefaultValue:      json.RawMessage(`"a"`),
		IsRequired:        true,
		IsVisibleToAgents: visibleToAgents,
		Position:          position,
		CreatedAt:         time.Now(),
	}
	require.NoError(t, (&CustomFieldDefinitionRepo{db: db}).Create(context.Background(), f))
	return f
}

func TestCustomFieldRepoGetByID_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedCustomField(t, db, fx, "sprint", true, 0)

	got, err := (&CustomFieldDefinitionRepo{db: db}).GetByID(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, fx.projectID, got.ProjectID)
	assert.Equal(t, "Field sprint", got.Name)
	assert.Equal(t, "sprint", got.Slug)
	assert.Equal(t, domain.FieldTypeSelect, got.FieldType)
	assert.Equal(t, "description for sprint", got.Description)
	assert.JSONEq(t, `{"choices": ["a", "b"]}`, string(got.Options))
	assert.JSONEq(t, `"a"`, string(got.DefaultValue),
		"options and default_value are adjacent JSONB columns; transposing them is silent")
	assert.True(t, got.IsRequired)
	assert.True(t, got.IsVisibleToAgents)
	assert.Equal(t, 0, got.Position)
}

func TestCustomFieldRepoListByProject_OrdersByPositionAndPopulates(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	second := seedCustomField(t, db, fx, "team", false, 5)
	first := seedCustomField(t, db, fx, "sprint", true, 0)

	fields, err := (&CustomFieldDefinitionRepo{db: db}).ListByProject(context.Background(), fx.projectID)
	require.NoError(t, err)
	require.Len(t, fields, 2)

	assert.Equal(t, first.ID, fields[0].ID, "ORDER BY position ASC")
	assert.Equal(t, "sprint", fields[0].Slug)
	assert.True(t, fields[0].IsVisibleToAgents)

	assert.Equal(t, second.ID, fields[1].ID)
	assert.Equal(t, "team", fields[1].Slug)
	assert.Equal(t, 5, fields[1].Position)
	assert.False(t, fields[1].IsVisibleToAgents)
	assert.Equal(t, domain.FieldTypeSelect, fields[1].FieldType)
}

func TestCustomFieldRepoListVisibleToAgents_FiltersOnTheFlag(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	visible := seedCustomField(t, db, fx, "sprint", true, 0)
	seedCustomField(t, db, fx, "team", false, 1)

	fields, err := (&CustomFieldDefinitionRepo{db: db}).ListVisibleToAgents(context.Background(), fx.projectID)
	require.NoError(t, err)
	require.Len(t, fields, 1, "the agent-invisible field must be filtered out in SQL")

	assert.Equal(t, visible.ID, fields[0].ID)
	assert.Equal(t, "sprint", fields[0].Slug)
	assert.True(t, fields[0].IsVisibleToAgents)
	assert.JSONEq(t, `{"choices": ["a", "b"]}`, string(fields[0].Options))
}

// ---------------------------------------------------------------------------
// saved_view_repo.go: GetByID, ListByProject
// ---------------------------------------------------------------------------

// seedSavedView writes one saved_views row. created_by is a real users row
// because saved_views.created_by carries a foreign key.
func seedSavedView(t *testing.T, db *sqlx.DB, fx coverageFixture, name string, isShared bool, createdBy uuid.UUID) *domain.SavedView {
	t.Helper()
	sortBy := "priority"
	sortOrder := "desc"
	v := &domain.SavedView{
		ID:        uuid.New(),
		ProjectID: fx.projectID,
		Name:      name,
		ViewType:  "table",
		Filters:   map[string]interface{}{"status": "todo"},
		SortBy:    &sortBy,
		SortOrder: &sortOrder,
		Columns:   []string{"title", "assignee"},
		IsShared:  isShared,
		CreatedBy: createdBy,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, NewSavedViewRepo(db).Create(context.Background(), v))
	return v
}

func TestSavedViewRepoGetByID_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedSavedView(t, db, fx, "my board", false, fx.userID)

	got, err := NewSavedViewRepo(db).GetByID(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, fx.projectID, got.ProjectID)
	assert.Equal(t, "my board", got.Name)
	assert.Equal(t, "table", got.ViewType)
	assert.Equal(t, map[string]interface{}{"status": "todo"}, got.Filters)
	require.NotNil(t, got.SortBy)
	assert.Equal(t, "priority", *got.SortBy)
	require.NotNil(t, got.SortOrder)
	assert.Equal(t, "desc", *got.SortOrder,
		"sort_by and sort_order are adjacent nullable columns; dropping either reads as nil")
	assert.Equal(t, []string{"title", "assignee"}, got.Columns)
	assert.False(t, got.IsShared)
	assert.Equal(t, fx.userID, got.CreatedBy)
}

func TestSavedViewRepoListByProject_ReturnsOwnViewsAndSharedOnes(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)

	// A second user in the same workspace, to separate "mine" from "shared".
	other := &domain.User{
		ID: uuid.New(), Email: "other-" + fx.suffix + "@example.com", PasswordHash: "x",
		Name: "Other User", Username: "other-" + fx.suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(context.Background(), other))

	mine := seedSavedView(t, db, fx, "my private board", false, fx.userID)
	shared := seedSavedView(t, db, fx, "their shared board", true, other.ID)
	seedSavedView(t, db, fx, "their private board", false, other.ID)

	views, err := NewSavedViewRepo(db).ListByProject(context.Background(), fx.projectID, fx.userID)
	require.NoError(t, err)
	require.Len(t, views, 2, "own views plus shared ones, never someone else's private view")

	byID := map[uuid.UUID]domain.SavedView{}
	for _, v := range views {
		byID[v.ID] = v
	}

	got, ok := byID[mine.ID]
	require.True(t, ok, "the caller's own private view must be listed")
	assert.Equal(t, "my private board", got.Name)
	assert.Equal(t, "table", got.ViewType)
	assert.Equal(t, []string{"title", "assignee"}, got.Columns)
	require.NotNil(t, got.SortBy)
	assert.Equal(t, "priority", *got.SortBy)

	sharedGot, ok := byID[shared.ID]
	require.True(t, ok, "another user's SHARED view must be listed")
	assert.True(t, sharedGot.IsShared)
	assert.Equal(t, other.ID, sharedGot.CreatedBy)
	assert.Equal(t, map[string]interface{}{"status": "todo"}, sharedGot.Filters)
}

// ---------------------------------------------------------------------------
// integration_repo.go: GetByID, GetByProvider, ListByWorkspace
// ---------------------------------------------------------------------------

// seedIntegration upserts one integration_configs row.
func seedIntegration(t *testing.T, db *sqlx.DB, fx coverageFixture, provider domain.IntegrationProvider, isActive bool) *domain.IntegrationConfig {
	t.Helper()
	cfg := &domain.IntegrationConfig{
		ID:          uuid.New(),
		WorkspaceID: fx.workspaceID,
		Provider:    provider,
		Config:      json.RawMessage(`{"webhook": "https://example.com/hook"}`),
		IsActive:    isActive,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	require.NoError(t, NewIntegrationRepo(db).Upsert(context.Background(), cfg))
	return cfg
}

func TestIntegrationRepoGetByID_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedIntegration(t, db, fx, domain.IntegrationProviderSlack, true)

	got, err := NewIntegrationRepo(db).GetByID(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, fx.workspaceID, got.WorkspaceID)
	assert.Equal(t, domain.IntegrationProviderSlack, got.Provider)
	assert.JSONEq(t, `{"webhook": "https://example.com/hook"}`, string(got.Config),
		"config is the entire payload of this row; an omitted column reads as {}")
	assert.True(t, got.IsActive)
	assert.False(t, got.CreatedAt.IsZero())
	assert.False(t, got.UpdatedAt.IsZero())
}

func TestIntegrationRepoGetByProvider_KeysOnWorkspaceAndProvider(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedIntegration(t, db, fx, domain.IntegrationProviderGitHub, true)
	seedIntegration(t, db, fx, domain.IntegrationProviderSlack, false)

	repo := NewIntegrationRepo(db)

	got, err := repo.GetByProvider(context.Background(), fx.workspaceID, domain.IntegrationProviderGitHub)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, domain.IntegrationProviderGitHub, got.Provider)
	assert.True(t, got.IsActive)

	// Same provider, a workspace that never configured it.
	other := newCoverageFixture(t, db)
	missing, err := repo.GetByProvider(context.Background(), other.workspaceID, domain.IntegrationProviderGitHub)
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestIntegrationRepoListByWorkspace_ReturnsFullyPopulatedRows(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	github := seedIntegration(t, db, fx, domain.IntegrationProviderGitHub, true)
	slack := seedIntegration(t, db, fx, domain.IntegrationProviderSlack, false)

	configs, err := NewIntegrationRepo(db).ListByWorkspace(context.Background(), fx.workspaceID)
	require.NoError(t, err)
	require.Len(t, configs, 2)

	// ORDER BY provider ASC: "github" before "slack".
	assert.Equal(t, github.ID, configs[0].ID)
	assert.Equal(t, domain.IntegrationProviderGitHub, configs[0].Provider)
	assert.True(t, configs[0].IsActive)
	assert.JSONEq(t, `{"webhook": "https://example.com/hook"}`, string(configs[0].Config))

	assert.Equal(t, slack.ID, configs[1].ID)
	assert.Equal(t, domain.IntegrationProviderSlack, configs[1].Provider)
	assert.False(t, configs[1].IsActive,
		"is_active must be read per row, not defaulted")
}

// ---------------------------------------------------------------------------
// webhook_repo.go: GetByID, ListByWorkspace, ListActiveByEvent, ListDeliveries
// ---------------------------------------------------------------------------

// seedWebhook writes one webhook_configs row subscribed to the given events.
func seedWebhook(t *testing.T, db *sqlx.DB, fx coverageFixture, name string, isActive bool, events []string) *domain.WebhookConfig {
	t.Helper()
	failedAt := time.Now().Add(-time.Hour)
	succeededAt := time.Now().Add(-time.Minute)
	w := &domain.WebhookConfig{
		ID:            uuid.New(),
		WorkspaceID:   fx.workspaceID,
		Name:          name,
		URL:           "https://example.com/hooks/" + name,
		Secret:        "shhh-" + name,
		Events:        events,
		IsActive:      isActive,
		FailureCount:  3,
		LastFailureAt: &failedAt,
		LastSuccessAt: &succeededAt,
		CreatedBy:     fx.userID,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	require.NoError(t, NewWebhookRepo(db).Create(context.Background(), w))
	return w
}

func TestWebhookRepoGetByID_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedWebhook(t, db, fx, "primary", true, []string{"task.created", "task.moved"})

	got, err := NewWebhookRepo(db).GetByID(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, fx.workspaceID, got.WorkspaceID)
	assert.Equal(t, "primary", got.Name)
	assert.Equal(t, "https://example.com/hooks/primary", got.URL)
	assert.Equal(t, "shhh-primary", got.Secret,
		"secret is json:\"-\" so nothing downstream would reveal a dropped column")
	assert.Equal(t, []string{"task.created", "task.moved"}, got.Events)
	assert.True(t, got.IsActive)
	assert.Equal(t, 3, got.FailureCount)
	require.NotNil(t, got.LastFailureAt)
	assert.WithinDuration(t, *seeded.LastFailureAt, *got.LastFailureAt, time.Second)
	require.NotNil(t, got.LastSuccessAt)
	assert.WithinDuration(t, *seeded.LastSuccessAt, *got.LastSuccessAt, time.Second,
		"last_failure_at and last_success_at are adjacent nullable timestamps; transposing them is silent")
	assert.Equal(t, fx.userID, got.CreatedBy)
}

func TestWebhookRepoListByWorkspace_ScopesAndPopulates(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedWebhook(t, db, fx, "primary", true, []string{"task.created"})

	other := newCoverageFixture(t, db)
	seedWebhook(t, db, other, "someone-elses", true, []string{"task.created"})

	hooks, err := NewWebhookRepo(db).ListByWorkspace(context.Background(), fx.workspaceID)
	require.NoError(t, err)
	require.Len(t, hooks, 1)

	assert.Equal(t, seeded.ID, hooks[0].ID)
	assert.Equal(t, "primary", hooks[0].Name)
	assert.Equal(t, "shhh-primary", hooks[0].Secret)
	assert.Equal(t, []string{"task.created"}, hooks[0].Events)
	assert.Equal(t, 3, hooks[0].FailureCount)
}

func TestWebhookRepoListActiveByEvent_FiltersOnActiveAndSubscription(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)

	wanted := seedWebhook(t, db, fx, "subscribed-active", true, []string{"task.created", "task.moved"})
	seedWebhook(t, db, fx, "subscribed-inactive", false, []string{"task.created"})
	seedWebhook(t, db, fx, "active-but-unsubscribed", true, []string{"comment.created"})

	hooks, err := NewWebhookRepo(db).ListActiveByEvent(context.Background(), fx.workspaceID, "task.created")
	require.NoError(t, err)
	require.Len(t, hooks, 1, "both is_active and the events membership test must be applied in SQL")

	assert.Equal(t, wanted.ID, hooks[0].ID)
	assert.Equal(t, "subscribed-active", hooks[0].Name)
	assert.True(t, hooks[0].IsActive)
	assert.Equal(t, []string{"task.created", "task.moved"}, hooks[0].Events)
	assert.Equal(t, "shhh-subscribed-active", hooks[0].Secret,
		"the dispatcher signs with this; an unread secret would sign with the empty string")
}

func TestWebhookRepoListDeliveries_ReturnsEveryListedColumnNewestFirst(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	hook := seedWebhook(t, db, fx, "primary", true, []string{"task.created"})
	repo := NewWebhookRepo(db)
	ctx := context.Background()

	status := 502
	body := "upstream is down"
	duration := 1234
	older := &domain.WebhookDelivery{
		ID: uuid.New(), WebhookID: hook.ID, EventType: "task.created",
		Payload: []byte(`{"attempt": "first"}`), Success: false, Attempt: 1,
		ResponseStatus: &status, ResponseBody: &body, DurationMs: &duration,
		CreatedAt: time.Now().Add(-time.Hour),
	}
	require.NoError(t, repo.CreateDelivery(ctx, older))

	okStatus := 200
	newest := &domain.WebhookDelivery{
		ID: uuid.New(), WebhookID: hook.ID, EventType: "task.moved",
		Payload: []byte(`{"attempt": "second"}`), Success: true, Attempt: 2,
		ResponseStatus: &okStatus, CreatedAt: time.Now(),
	}
	require.NoError(t, repo.CreateDelivery(ctx, newest))

	deliveries, err := repo.ListDeliveries(ctx, hook.ID, 10)
	require.NoError(t, err)
	require.Len(t, deliveries, 2)

	// ORDER BY created_at DESC.
	first := deliveries[0]
	assert.Equal(t, newest.ID, first.ID)
	assert.Equal(t, "task.moved", first.EventType)
	assert.JSONEq(t, `{"attempt": "second"}`, string(first.Payload))
	assert.True(t, first.Success)
	assert.Equal(t, 2, first.Attempt)
	require.NotNil(t, first.ResponseStatus)
	assert.Equal(t, 200, *first.ResponseStatus)
	assert.Nil(t, first.ResponseBody, "an unset nullable column must read as nil, not as \"\"")

	second := deliveries[1]
	assert.Equal(t, older.ID, second.ID)
	assert.False(t, second.Success)
	assert.Equal(t, 1, second.Attempt)
	require.NotNil(t, second.ResponseStatus)
	assert.Equal(t, 502, *second.ResponseStatus)
	require.NotNil(t, second.ResponseBody)
	assert.Equal(t, "upstream is down", *second.ResponseBody)
	require.NotNil(t, second.DurationMs)
	assert.Equal(t, 1234, *second.DurationMs)
}

func TestWebhookRepoListDeliveries_HonoursTheLimit(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	hook := seedWebhook(t, db, fx, "primary", true, []string{"task.created"})
	repo := NewWebhookRepo(db)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		require.NoError(t, repo.CreateDelivery(ctx, &domain.WebhookDelivery{
			ID: uuid.New(), WebhookID: hook.ID, EventType: "task.created",
			Payload: []byte(`{}`), Success: true, Attempt: 1,
			CreatedAt: time.Now().Add(time.Duration(i) * time.Minute),
		}))
	}

	deliveries, err := repo.ListDeliveries(ctx, hook.ID, 2)
	require.NoError(t, err)
	assert.Len(t, deliveries, 2, "the LIMIT must reach Postgres, not be applied afterwards")
}

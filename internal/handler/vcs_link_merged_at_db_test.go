package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// merged_at of VCS links over real Postgres: the provider's merge
// time goes from a webhook or a provider read into vcs_links.merged_at and out
// of GET /tasks/:id/vcs-links in UTC; a status re-write never moves it; an
// unknown time stays null. Only the provider (GitLab REST) is faked. Skips
// without Postgres, like the other *_db_test.go here.

type mergedAtFixture struct {
	db                   *sqlx.DB
	ws                   uuid.UUID
	openStatus, doneStat uuid.UUID
	proj                 uuid.UUID
	links                *postgres.VCSLinkRepo
}

func newMergedAtFixture(t *testing.T) *mergedAtFixture {
	t.Helper()
	db := oauthE2ETestDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]
	ws := &domain.Workspace{ID: uuid.New(), Name: "merged-at-ws", Slug: "merged-at-ws-" + suffix, OwnerID: uuid.New()}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	proj := &domain.Project{ID: uuid.New(), WorkspaceID: ws.ID, Name: "merged-at", Slug: "merged-at-" + suffix,
		DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, proj))
	statuses := postgres.NewTaskStatusRepo(db)
	todo := &domain.TaskStatus{ID: uuid.New(), ProjectID: proj.ID, Name: "Todo", Slug: "todo", Color: "#00FF00",
		Category: domain.StatusCategoryTodo, IsDefault: true, Position: 1}
	done := &domain.TaskStatus{ID: uuid.New(), ProjectID: proj.ID, Name: "Done", Slug: "done", Color: "#999999",
		Category: domain.StatusCategoryDone, Position: 2}
	require.NoError(t, statuses.Create(ctx, todo))
	require.NoError(t, statuses.Create(ctx, done))
	return &mergedAtFixture{db: db, ws: ws.ID, openStatus: todo.ID, doneStat: done.ID, proj: proj.ID, links: postgres.NewVCSLinkRepo(db)}
}

func (f *mergedAtFixture) task(t *testing.T, status uuid.UUID, completedAt *time.Time) uuid.UUID {
	t.Helper()
	task := &domain.Task{ID: uuid.New(), ProjectID: f.proj, StatusID: status, Title: "merged-at card",
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser, CompletedAt: completedAt}
	require.NoError(t, postgres.NewTaskRepo(f.db).Create(context.Background(), task, nil))
	return task.ID
}

// legacyMergedLink stores a merged GitLab MR link the way rows that predate
// merged_at look: status merged, merged_at NULL.
func (f *mergedAtFixture) legacyMergedLink(t *testing.T, task uuid.UUID, iid int) uuid.UUID {
	t.Helper()
	l := &domain.VCSLink{ID: uuid.New(), TaskID: task, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: fmt.Sprint(iid), URL: fmt.Sprintf("https://git.example/g/p/-/merge_requests/%d", iid),
		Title: "mr", Status: domain.VCSLinkStatusMerged, Metadata: json.RawMessage(`{}`), CreatedAt: time.Now()}
	require.NoError(t, f.links.Create(context.Background(), l))
	return l.ID
}

// storedMergedAt re-reads the column itself, not the API.
func (f *mergedAtFixture) storedMergedAt(t *testing.T, linkID uuid.UUID) (mergedAt, syncedAt *time.Time) {
	t.Helper()
	row := struct {
		MergedAt *time.Time `db:"merged_at"`
		SyncedAt *time.Time `db:"status_synced_at"`
	}{}
	require.NoError(t, f.db.Get(&row, `SELECT merged_at, status_synced_at FROM vcs_links WHERE id = $1`, linkID))
	return row.MergedAt, row.SyncedAt
}

// listLinksJSON calls GET /tasks/:task_id/vcs-links through the handler and
// returns the raw link objects, so the wire spelling of merged_at is asserted.
func listLinksJSON(t *testing.T, h *VCSLinkHandler, taskID uuid.UUID) []map[string]json.RawMessage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	require.NoError(t, h.List(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Links []map[string]json.RawMessage `json:"vcs_links"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body.Links
}

func linkJSONByID(t *testing.T, links []map[string]json.RawMessage, id uuid.UUID) map[string]json.RawMessage {
	t.Helper()
	for _, l := range links {
		if string(l["id"]) == `"`+id.String()+`"` {
			return l
		}
	}
	t.Fatalf("link %s not in GET vcs-links", id)
	return nil
}

func linkJSONByURL(t *testing.T, links []map[string]json.RawMessage, url string) map[string]json.RawMessage {
	t.Helper()
	for _, l := range links {
		if string(l["url"]) == `"`+url+`"` {
			return l
		}
	}
	t.Fatalf("link %s not in GET vcs-links", url)
	return nil
}

func githubMergedPayload(t *testing.T, action string, number int, taskID uuid.UUID, mergedAt any) []byte {
	t.Helper()
	pr := map[string]any{
		"number": number, "title": "feat: x MESH-" + taskID.String(), "body": "",
		"html_url": fmt.Sprintf("https://github.com/example/repo/pull/%d", number),
		"state":    "closed", "merged": true, "merge_commit_sha": "abc",
	}
	if mergedAt != nil {
		pr["merged_at"] = mergedAt
	}
	b, err := json.Marshal(map[string]any{
		"action": action, "pull_request": pr,
		"repository": map[string]any{"full_name": "example/repo", "html_url": "https://github.com/example/repo"},
	})
	require.NoError(t, err)
	return b
}

// AC1: the GitHub pull_request delivery's merged_at (sent with a +03:00
// offset) is stored and served as the same instant in UTC; a repeated status
// upsert (a second delivery, a manual re-link with status=merged) moves
// status_synced_at but never merged_at; a merged delivery without a time
// leaves merged_at null instead of the delivery time.
func TestVCSLinkMergedAt_GitHubWebhookStoresProviderTime_DB(t *testing.T) {
	f := newMergedAtFixture(t)
	ctx := context.Background()
	const secret = "merged-at-secret"
	resolver := service.NewVCSIntegrationResolver(&fakeResolverIntegrationRepo{row: &domain.IntegrationConfig{
		WorkspaceID: f.ws, Provider: domain.IntegrationProviderGitHub, IsActive: true,
		Config: githubIntegrationCfgJSON(t, "", secret),
	}}, service.VCSEnvFallback{})
	// The card sits in todo, so the merge posts a system comment through the
	// real comment service and moves nothing (the task service is not reached).
	taskRepo := postgres.NewTaskRepo(f.db)
	svc := service.NewVCSLinkService(f.links,
		service.WithVCSTaskRepo(taskRepo),
		service.WithVCSStatusRepo(postgres.NewTaskStatusRepo(f.db)),
		service.WithVCSProjectRepo(postgres.NewProjectRepo(f.db)),
		service.WithVCSTaskService(&MockTaskService{}),
		service.WithVCSCommentService(service.NewCommentService(postgres.NewCommentRepo(f.db), taskRepo, postgres.NewActivityLogRepo(f.db))))
	h := NewVCSLinkHandler(svc, WithVCSIntegrationResolver(resolver), WithWebhookDedupStore(newMemDedupStore()))
	deliver := func(body []byte) {
		t.Helper()
		rec := httptest.NewRecorder()
		require.NoError(t, h.GitHubWebhook(echo.New().NewContext(newPullRequestRequest(t, body, uuid.NewString(), secret), rec)))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	task := f.task(t, f.openStatus, nil)
	deliver(githubMergedPayload(t, "closed", 41, task, "2026-11-20T21:03:04+03:00"))

	const wantUTC = `"2026-11-20T18:03:04Z"`
	url41 := "https://github.com/example/repo/pull/41"
	got := linkJSONByURL(t, listLinksJSON(t, h, task), url41)
	assert.Equal(t, wantUTC, string(got["merged_at"]), "GET vcs-links serves the provider instant in UTC")
	var linkID uuid.UUID
	require.NoError(t, json.Unmarshal(got["id"], &linkID))
	stored, synced1 := f.storedMergedAt(t, linkID)
	require.NotNil(t, stored)
	assert.True(t, stored.Equal(time.Date(2026, 11, 20, 18, 3, 4, 0, time.UTC)), "stored row: %v", stored)

	// A repeated status upsert: a later delivery carrying a different time
	// (a re-delivery or an edit) and a manual re-link with an explicit status.
	time.Sleep(5 * time.Millisecond)
	deliver(githubMergedPayload(t, "edited", 41, task, "2026-11-21T09:00:00Z"))
	_, _, err := svc.Create(ctx, domain.CreateVCSLinkInput{TaskID: task, Provider: domain.VCSProviderGitHub,
		LinkType: domain.VCSLinkTypePR, ExternalID: "41", URL: url41, Title: "x", Status: domain.VCSLinkStatusMerged})
	require.NoError(t, err)
	stored2, synced2 := f.storedMergedAt(t, linkID)
	require.NotNil(t, stored2)
	assert.True(t, stored2.Equal(*stored), "a status upsert must not move a known merged_at: %v -> %v", stored, stored2)
	require.NotNil(t, synced1)
	require.NotNil(t, synced2)
	assert.True(t, synced2.After(*synced1), "the re-writes did happen (status_synced_at moved)")
	assert.Equal(t, wantUTC, string(linkJSONByURL(t, listLinksJSON(t, h, task), url41)["merged_at"]))

	// Unknown provider time: merged without merged_at, and an unparseable one.
	other := f.task(t, f.openStatus, nil)
	deliver(githubMergedPayload(t, "closed", 42, other, nil))
	deliver(githubMergedPayload(t, "closed", 43, other, "yesterday"))
	links := listLinksJSON(t, h, other)
	for _, n := range []int{42, 43} {
		l := linkJSONByURL(t, links, fmt.Sprintf("https://github.com/example/repo/pull/%d", n))
		assert.Equal(t, `"merged"`, string(l["status"]))
		assert.Equal(t, "null", string(l["merged_at"]), "PR %d: unknown time stays null, not now() or zero", n)
	}
}

// fakeGitLabEvidence answers GET merge_requests/:iid; only the network is
// faked. calls counts reads, to prove a known time is not re-read.
type fakeGitLabEvidence struct {
	mergedAt map[int]string // iid -> GitLab merged_at ("" = GitLab reports none)
	calls    int
}

func (g *fakeGitLabEvidence) GetMergeRequestEvidence(_ context.Context, path string, iid int) (gitlabapi.MergeRequestEvidence, error) {
	g.calls++
	raw, ok := g.mergedAt[iid]
	if !ok {
		return gitlabapi.MergeRequestEvidence{}, errors.New("unexpected status 404")
	}
	return gitlabapi.MergeRequestEvidence{
		State: "merged", SHA: strings.Repeat("a", 40),
		WebURL:   fmt.Sprintf("https://git.example/%s/-/merge_requests/%d", path, iid),
		MergedAt: domain.ParseVCSProviderTime(raw),
	}, nil
}

func syncLink(t *testing.T, h *VCSLinkHandler, taskID, linkID uuid.UUID) map[string]json.RawMessage {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames("task_id", "link_id")
	c.SetParamValues(taskID.String(), linkID.String())
	require.NoError(t, h.Sync(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	return res
}

// AC2: an existing merged link with merged_at NULL gets GitLab's own merged_at
// through the ordinary provider read (POST .../sync and the backfill sweeper);
// a repeat changes nothing; a link GitLab reports no time for, or cannot
// resolve, stays null — never now() or zero.
func TestVCSLinkMergedAt_ExistingMergedLinkGetsProviderTime_DB(t *testing.T) {
	f := newMergedAtFixture(t)
	ctx := context.Background()
	gl := &fakeGitLabEvidence{mergedAt: map[int]string{
		71: "2026-11-19T10:11:12.345Z", // the sync endpoint
		72: "",                         // GitLab reports no time
		73: "2026-11-18T23:59:59.999999Z",
	}}
	svc := service.NewVCSLinkService(f.links,
		service.WithVCSTaskRepo(postgres.NewTaskRepo(f.db)),
		service.WithVCSProjectRepo(postgres.NewProjectRepo(f.db)),
		service.WithVCSMREvidenceSource(func(context.Context, uuid.UUID) (gitlabapi.MergeRequestEvidenceSource, bool) {
			return gl, true
		}))
	h := NewVCSLinkHandler(svc)

	task := f.task(t, f.openStatus, nil)
	l71 := f.legacyMergedLink(t, task, 71)
	l72 := f.legacyMergedLink(t, task, 72)
	l73 := f.legacyMergedLink(t, task, 73)
	l74 := f.legacyMergedLink(t, task, 74) // GitLab answers 404
	for _, id := range []uuid.UUID{l71, l72, l73, l74} {
		at, _ := f.storedMergedAt(t, id)
		require.Nil(t, at, "precondition: legacy row has no merged_at")
	}

	res := syncLink(t, h, task, l71)
	assert.Equal(t, "true", string(res["changed"]))
	var link map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(res["link"], &link))
	assert.Equal(t, `"2026-11-19T10:11:12.345Z"`, string(link["merged_at"]))
	at71, _ := f.storedMergedAt(t, l71)
	require.NotNil(t, at71)
	assert.True(t, at71.Equal(time.Date(2026, 11, 19, 10, 11, 12, 345e6, time.UTC)), "stored: %v", at71)

	// Repeat: idempotent, the known time stays.
	res = syncLink(t, h, task, l71)
	require.NoError(t, json.Unmarshal(res["link"], &link))
	assert.Equal(t, `"2026-11-19T10:11:12.345Z"`, string(link["merged_at"]))
	at71b, _ := f.storedMergedAt(t, l71)
	assert.True(t, at71b.Equal(*at71))

	// GitLab reports no time: still null after the sync.
	res = syncLink(t, h, task, l72)
	require.NoError(t, json.Unmarshal(res["link"], &link))
	assert.Equal(t, "null", string(link["merged_at"]))

	// The backfill sweeper fills the rest it can resolve, in one pass.
	backfiller, ok := svc.(service.MergedAtBackfiller)
	require.True(t, ok, "the VCS link service is the sweeper's backfiller")
	sweeper := service.NewMergedAtSweeper(backfiller, 500, time.Hour)
	page, err := sweeper.Tick(ctx)
	require.NoError(t, err)
	assert.True(t, page.Done)
	assert.GreaterOrEqual(t, page.Filled, 1)
	at73, _ := f.storedMergedAt(t, l73)
	require.NotNil(t, at73)
	assert.True(t, at73.Equal(time.Date(2026, 11, 18, 23, 59, 59, 999999e3, time.UTC)), "stored: %v", at73)
	for _, id := range []uuid.UUID{l72, l74} {
		at, _ := f.storedMergedAt(t, id)
		assert.Nil(t, at, "unresolvable link stays null after backfill")
	}

	// A second run is idempotent and, resting after the full pass, reads
	// nothing; a fresh pass over the same rows writes nothing new.
	callsBefore := gl.calls
	page, err = sweeper.Tick(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, page.Seen, "resting after a full pass")
	assert.Equal(t, callsBefore, gl.calls)
	page2, err := backfiller.BackfillMergedAt(ctx, uuid.Nil, 500)
	require.NoError(t, err)
	at73b, _ := f.storedMergedAt(t, l73)
	assert.True(t, at73b.Equal(*at73))
	at71c, _ := f.storedMergedAt(t, l71)
	assert.True(t, at71c.Equal(*at71))
	for _, id := range []uuid.UUID{l72, l74} {
		at, _ := f.storedMergedAt(t, id)
		assert.Nil(t, at)
	}
	_ = page2

	links := listLinksJSON(t, h, task)
	assert.Equal(t, `"2026-11-18T23:59:59.999999Z"`, string(linkJSONByID(t, links, l73)["merged_at"]))
	assert.Equal(t, "null", string(linkJSONByID(t, links, l74)["merged_at"]))
}

// AC3 (Mesh half): merged_at is the provider instant whatever the card's
// state — an open card and a card completed days later serve exactly the
// stored instant, so a consumer windowing by merged_at puts each MR in the
// window of its merge, not of the card's completed_at. Instants on a window
// boundary (t0, and t0 - 1µs) round-trip without truncation, so half-open
// [from, to) windows keep their exact edges. The window arithmetic itself is
// the consumer's (TokenLedger readapi feed tests).
func TestVCSLinkMergedAt_IndependentOfCardCompletion_DB(t *testing.T) {
	f := newMergedAtFixture(t)
	t0 := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC) // a window edge
	gl := &fakeGitLabEvidence{mergedAt: map[int]string{
		81: t0.Format(time.RFC3339Nano),
		82: t0.Add(-time.Microsecond).Format(time.RFC3339Nano),
	}}
	svc := service.NewVCSLinkService(f.links,
		service.WithVCSTaskRepo(postgres.NewTaskRepo(f.db)),
		service.WithVCSProjectRepo(postgres.NewProjectRepo(f.db)),
		service.WithVCSMREvidenceSource(func(context.Context, uuid.UUID) (gitlabapi.MergeRequestEvidenceSource, bool) {
			return gl, true
		}))
	h := NewVCSLinkHandler(svc)

	openCard := f.task(t, f.openStatus, nil)
	completed := t0.Add(2 * 24 * time.Hour) // the card closed inside [t0, t0+7d)
	doneCard := f.task(t, f.doneStat, &completed)
	lOpen := f.legacyMergedLink(t, openCard, 81)
	lDone := f.legacyMergedLink(t, doneCard, 82)
	syncLink(t, h, openCard, lOpen)
	syncLink(t, h, doneCard, lDone)

	parse := func(raw json.RawMessage) time.Time {
		var s string
		require.NoError(t, json.Unmarshal(raw, &s))
		require.True(t, strings.HasSuffix(s, "Z"), "UTC: %s", s)
		at, err := time.Parse(time.RFC3339Nano, s)
		require.NoError(t, err)
		return at
	}
	gotOpen := parse(linkJSONByID(t, listLinksJSON(t, h, openCard), lOpen)["merged_at"])
	gotDone := parse(linkJSONByID(t, listLinksJSON(t, h, doneCard), lDone)["merged_at"])
	assert.True(t, gotOpen.Equal(t0), "open card: merged_at %v, want %v", gotOpen, t0)
	assert.True(t, gotDone.Equal(t0.Add(-time.Microsecond)), "done card: merged_at %v", gotDone)
	assert.False(t, gotDone.Equal(completed), "merged_at is not the card's completed_at")

	// Half-open window [t0, t0+7d): the open card's MR is in, the done card's
	// MR (1µs before t0) is out, although that card completed inside it.
	in := func(at time.Time) bool { return !at.Before(t0) && at.Before(t0.Add(7*24*time.Hour)) }
	assert.True(t, in(gotOpen))
	assert.False(t, in(gotDone))
}

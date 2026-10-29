package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// The turn commands over real repositories and a fake GitLab (the network is
// the only boundary mocked). Every refusal must leave the card exactly as it
// was: no new wait, outcome, comment or dependency, and the checkout still
// held. Skips without Postgres, like the other *_db_test.go here.

type fakeGitLab struct {
	mrs       map[string]string // "path!iid" → opened|merged|closed
	pipelines map[string]string // "path#id" → status
}

func (f fakeGitLab) GetMergeRequestState(_ context.Context, path string, iid int) (gitlabapi.MergeRequestState, error) {
	st, ok := f.mrs[fmt.Sprintf("%s!%d", path, iid)]
	if !ok {
		return gitlabapi.MergeRequestState{}, errors.New("unexpected status 404")
	}
	return gitlabapi.MergeRequestState{Merged: st == "merged", State: st}, nil
}

func (f fakeGitLab) GetPipelineStatus(_ context.Context, path string, id int) (string, error) {
	st, ok := f.pipelines[fmt.Sprintf("%s#%d", path, id)]
	if !ok {
		return "", errors.New("unexpected status 404")
	}
	return st, nil
}

type turnFixture struct {
	db                     *sqlx.DB
	e                      *echo.Echo
	task, other, done      uuid.UUID
	agent, agent2          uuid.UUID
	otherLink              uuid.UUID
	agent2Name, todoStatus string
}

func newTurnFixture(t *testing.T) *turnFixture {
	t.Helper()
	db := oauthE2ETestDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]
	f := &turnFixture{db: db}

	ws := &domain.Workspace{ID: uuid.New(), Name: "turn-ws", Slug: "turn-ws-" + suffix, OwnerID: uuid.New()}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	proj := &domain.Project{ID: uuid.New(), WorkspaceID: ws.ID, Name: "turn", Slug: "turn-" + suffix,
		DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, proj))
	statuses := postgres.NewTaskStatusRepo(db)
	todo := &domain.TaskStatus{ID: uuid.New(), ProjectID: proj.ID, Name: "Todo", Slug: "todo", Color: "#00FF00",
		Category: domain.StatusCategoryTodo, IsDefault: true, Position: 1}
	inProgress := &domain.TaskStatus{ID: uuid.New(), ProjectID: proj.ID, Name: "In progress", Slug: "in_progress",
		Color: "#0000FF", Category: domain.StatusCategoryInProgress, Position: 2}
	doneStatus := &domain.TaskStatus{ID: uuid.New(), ProjectID: proj.ID, Name: "Done", Slug: "done", Color: "#999999",
		Category: domain.StatusCategoryDone, Position: 3}
	for _, s := range []*domain.TaskStatus{todo, inProgress, doneStatus} {
		require.NoError(t, statuses.Create(ctx, s))
	}
	f.todoStatus = todo.Slug

	newTask := func(title string, status uuid.UUID) uuid.UUID {
		task := &domain.Task{ID: uuid.New(), ProjectID: proj.ID, StatusID: status, Title: title,
			AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
			CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser}
		require.NoError(t, postgres.NewTaskRepo(db).Create(ctx, task, nil))
		return task.ID
	}
	newAgent := func(name string) uuid.UUID {
		a := &domain.Agent{ID: uuid.New(), WorkspaceID: ws.ID, Name: name, Slug: name,
			AgentType: domain.AgentTypeClaudeCode, Status: domain.AgentStatusOffline,
			APIKeyHash: "$2a$12$" + name, APIKeyPrefix: uuid.New().String()[:8], APIKeySHA256: "turn-" + uuid.New().String()}
		require.NoError(t, postgres.NewAgentRepo(db).Create(ctx, a))
		return a.ID
	}
	f.task = newTask("waiting card", inProgress.ID)
	f.other = newTask("blocker", inProgress.ID)
	f.done = newTask("closed blocker", doneStatus.ID)
	f.agent = newAgent("turn-a-" + suffix)
	f.agent2Name = "turn-b-" + suffix
	f.agent2 = newAgent(f.agent2Name)

	links := postgres.NewVCSLinkRepo(db)
	link := func(task uuid.UUID, iid int, status domain.VCSLinkStatus) uuid.UUID {
		l := &domain.VCSLink{ID: uuid.New(), TaskID: task, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
			ExternalID: fmt.Sprint(iid), URL: fmt.Sprintf("https://git.example/g/p/-/merge_requests/%d", iid),
			Title: "mr", Status: status, Metadata: json.RawMessage(`{}`), CreatedAt: time.Now()}
		require.NoError(t, links.Create(ctx, l))
		return l.ID
	}
	f.otherLink = link(f.other, 30, domain.VCSLinkStatusOpen)
	f.checkout(t, f.task, f.agent)

	gl := fakeGitLab{
		mrs:       map[string]string{"g/p!10": "opened", "g/p!11": "merged"},
		pipelines: map[string]string{"g/p#500": "running", "g/p#501": "success"},
	}
	svc := service.NewTurnCommandService(postgres.NewTurnCommandRepo(db), links, nil,
		func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool) { return gl, true })
	h := NewTurnCommandHandler(svc, nil)
	f.e = echo.New()
	f.e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id, err := uuid.Parse(c.Request().Header.Get("X-Test-Agent"))
			if err == nil {
				c.SetRequest(c.Request().WithContext(actorctx.WithActor(c.Request().Context(), id, domain.ActorTypeAgent)))
			}
			return next(c)
		}
	})
	f.e.POST("/api/v1/tasks/:task_id/wait", h.WaitFor)
	f.e.POST("/api/v1/tasks/:task_id/next", h.ContinueNext)
	f.e.POST("/api/v1/tasks/:task_id/handoff", h.HandOff)
	f.e.POST("/api/v1/tasks/:task_id/notes", h.AddNote)
	return f
}

func (f *turnFixture) checkout(t *testing.T, task, agent uuid.UUID) {
	t.Helper()
	_, err := f.db.Exec(`UPDATE tasks SET checked_out_by = $2, checkout_token = gen_random_uuid(),
		checkout_expires = now() + interval '1 hour', checkout_acquired_at = now(),
		checkout_generation = checkout_generation + 1 WHERE id = $1`, task, agent)
	require.NoError(t, err)
}

// timeEvidence fills a time wait's evidence; keys already in m win.
func timeEvidence(m map[string]any) map[string]any {
	for k, v := range map[string]any{"event_ref": "nightly-export", "source": "https://status.example.com/jobs/42",
		"why": "the export job publishes the file at this time"} {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	return m
}

// comment writes a comment by an agent and returns its id.
func (f *turnFixture) comment(t *testing.T, task, author uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, f.db.Get(&id, `INSERT INTO comments (id, task_id, author_id, author_type, body)
		VALUES (gen_random_uuid(), $1, $2, 'agent', 'the export job starts at 03:00 MSK') RETURNING id`, task, author))
	return id
}

func (f *turnFixture) post(as uuid.UUID, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+f.task.String()+path, bytes.NewReader(raw))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("X-Test-Agent", as.String())
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	return rec
}

type turnCounts struct{ Waits, Outcomes, Comments, Deps int }

func (f *turnFixture) counts(t *testing.T) turnCounts {
	t.Helper()
	var c turnCounts
	require.NoError(t, f.db.QueryRow(`SELECT
		(SELECT count(*) FROM task_waits WHERE task_id = $1),
		(SELECT count(*) FROM turn_outcomes WHERE task_id = $1),
		(SELECT count(*) FROM comments WHERE task_id = $1),
		(SELECT count(*) FROM task_dependencies WHERE task_id = $1)`, f.task).
		Scan(&c.Waits, &c.Outcomes, &c.Comments, &c.Deps))
	return c
}

func (f *turnFixture) holder(t *testing.T) *uuid.UUID {
	t.Helper()
	var h *uuid.UUID
	require.NoError(t, f.db.Get(&h, `SELECT checked_out_by FROM tasks WHERE id = $1`, f.task))
	return h
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		ErrorCode string `json:"error_code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e), rec.Body.String())
	return e.ErrorCode
}

func TestTurnCommands_InvalidTargetsWriteNothing_DB(t *testing.T) {
	f := newTurnFixture(t)
	now := time.Now().UTC()
	merged, finished := domain.WaitUntilMerged, domain.WaitUntilFinished
	cases := []struct {
		name, path string
		body       map[string]any
		code       string
	}{
		{"card does not exist", "/wait", map[string]any{"on": "card", "target_task_id": uuid.New()}, domain.CodeInvalidTarget},
		{"own card", "/wait", map[string]any{"on": "card", "target_task_id": f.task}, domain.CodeSelfWait},
		{"card already closed", "/wait", map[string]any{"on": "card", "target_task_id": f.done}, domain.CodeAlreadyMet},
		{"unknown condition", "/wait", map[string]any{"on": "card", "target_task_id": f.other, "target_condition": "whenever"}, domain.CodeInvalidTarget},
		{"MR does not exist", "/wait", map[string]any{"on": "mr", "provider": "gitlab", "project_path": "g/p", "mr_iid": 99, "until": merged}, domain.CodeInvalidTarget},
		{"MR already merged", "/wait", map[string]any{"on": "mr", "provider": "gitlab", "project_path": "g/p", "mr_iid": 11, "until": merged}, domain.CodeAlreadyMet},
		{"link of another card", "/wait", map[string]any{"on": "mr", "vcs_link_id": f.otherLink, "until": merged}, domain.CodeInvalidTarget},
		{"pipeline does not exist", "/wait", map[string]any{"on": "pipeline", "project_path": "g/p", "pipeline_id": 999, "until": finished}, domain.CodeInvalidTarget},
		{"pipeline already finished", "/wait", map[string]any{"on": "pipeline", "project_path": "g/p", "pipeline_id": 501, "until": finished}, domain.CodeAlreadyMet},
		{"time in 1 minute", "/wait", map[string]any{"on": "time", "until_at": now.Add(time.Minute)}, domain.CodeTimeOutOfBounds},
		{"time in 31 days", "/wait", map[string]any{"on": "time", "until_at": now.Add(31 * 24 * time.Hour)}, domain.CodeTimeOutOfBounds},
		{"unknown kind", "/wait", map[string]any{"on": "decision"}, domain.CodeInvalidTarget},
		{"hand_off to an unknown agent", "/handoff", map[string]any{"to": "nobody-" + uuid.NewString(), "why": "reason"}, domain.CodeNotAnAgent},
		{"hand_off to oneself", "/handoff", map[string]any{"to": f.agent.String(), "why": "reason"}, domain.CodeNotAnAgent},
	}
	// Receipts for time waits live on the other card so they do not move the counts.
	ownReceipt := f.comment(t, f.other, f.agent)
	cases = append(cases, []struct {
		name, path string
		body       map[string]any
		code       string
	}{
		{"time without evidence", "/wait", map[string]any{"on": "time", "until_at": now.Add(time.Hour)}, domain.CodeMissingEvidence},
		{"time relative until", "/wait", timeEvidence(map[string]any{"on": "time", "until_at": now.Add(time.Hour), "until": "6h"}), domain.CodeNaiveTime},
		{"time naive until_at", "/wait", timeEvidence(map[string]any{"on": "time", "until_at": "2026-10-30T03:00:00"}), domain.CodeNaiveTime},
		{"time relative until_at", "/wait", timeEvidence(map[string]any{"on": "time", "until_at": "6h"}), domain.CodeNaiveTime},
		{"time source is a card ref", "/wait", timeEvidence(map[string]any{"on": "time", "until_at": now.Add(time.Hour), "source": "card:" + f.other.String()}), domain.CodeMissingEvidence},
		{"time receipt does not exist", "/wait", timeEvidence(map[string]any{"on": "time", "until_at": now.Add(time.Hour), "source": "comment:" + uuid.NewString()}), domain.CodeMissingEvidence},
		{"time receipt written by the waiter", "/wait", timeEvidence(map[string]any{"on": "time", "until_at": now.Add(time.Hour), "source": "comment:" + ownReceipt.String()}), domain.CodeMissingEvidence},
	}...)
	before := f.counts(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.post(f.agent, tc.path, tc.body)
			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			assert.Equal(t, tc.code, errorCode(t, rec))
			assert.Equal(t, before, f.counts(t), "a refused command wrote rows")
			h := f.holder(t)
			require.NotNil(t, h, "a refused command released the checkout")
			assert.Equal(t, f.agent, *h)
		})
	}

	t.Run("time without an offset", func(t *testing.T) {
		naive := now.Add(time.Hour).UTC().Format("2006-01-02T15:04:05")
		rec := f.post(f.agent, "/wait", timeEvidence(map[string]any{"on": "time", "until_at": naive}))
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), domain.CodeNaiveTime)
		assert.Equal(t, before, f.counts(t))
	})

	t.Run("caller without the checkout", func(t *testing.T) {
		rec := f.post(f.agent2, "/next", map[string]any{"note": "x"})
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Equal(t, before, f.counts(t))
	})
}

func TestTurnCommands_WaitSupersedesAndReleases_DB(t *testing.T) {
	f := newTurnFixture(t)
	receipt := f.comment(t, f.other, f.agent2)
	msk := time.FixedZone("MSK", 3*3600)
	rec := f.post(f.agent, "/wait", timeEvidence(map[string]any{"on": "time", "until_at": time.Now().Add(time.Hour).In(msk),
		"note": "window", "session_id": "s1", "source": "comment:" + receipt.String()}))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var first domain.TurnCommandResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &first))
	assert.True(t, first.Released)
	require.NotNil(t, first.Wait)
	require.NotNil(t, first.Outcome)
	assert.Equal(t, domain.TurnOutcomeWait, first.Outcome.Kind)
	assert.Equal(t, first.Wait.ID, *first.Outcome.WaitID)
	assert.Nil(t, f.holder(t), "wait_for must release the checkout")
	var ev struct {
		EventRef string `db:"event_ref"`
		Source   string `db:"evidence_source"`
		Offset   string `db:"until_at_offset"`
		Status   string `db:"evidence_status"`
	}
	require.NoError(t, f.db.Get(&ev, `SELECT event_ref, evidence_source, until_at_offset, evidence_status FROM task_waits WHERE id = $1`, first.Wait.ID))
	assert.Equal(t, "nightly-export", ev.EventRef)
	assert.Equal(t, "comment:"+receipt.String(), ev.Source)
	assert.Equal(t, "+03:00", ev.Offset, "the offset until_at was given in is kept")
	assert.Equal(t, domain.TimeEvidenceUnverified, ev.Status)

	var state struct {
		Kind string `json:"kind"`
		Ref  string `json:"ref"`
	}
	var raw []byte
	require.NoError(t, f.db.Get(&raw, `SELECT runtime_state FROM tasks WHERE id = $1`, f.task))
	require.NoError(t, json.Unmarshal(raw, &state))
	assert.Equal(t, "waiting", state.Kind)
	assert.Equal(t, domain.WaitRuntimeRefPrefix+first.Wait.ID.String(), state.Ref)

	// Released: the same agent cannot declare again until it holds the card.
	assert.Equal(t, http.StatusConflict, f.post(f.agent, "/wait", map[string]any{"on": "card", "target_task_id": f.other}).Code)

	f.checkout(t, f.task, f.agent)
	rec = f.post(f.agent, "/wait", map[string]any{"on": "card", "target_task_id": f.other, "target_condition": "shipped"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var second domain.TurnCommandResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &second))

	var resolution *string
	require.NoError(t, f.db.Get(&resolution, `SELECT resolution FROM task_waits WHERE id = $1`, first.Wait.ID))
	require.NotNil(t, resolution)
	assert.Equal(t, string(domain.WaitResolutionSuperseded), *resolution)
	var active int
	require.NoError(t, f.db.Get(&active, `SELECT count(*) FROM task_waits WHERE task_id = $1 AND resolved_at IS NULL`, f.task))
	assert.Equal(t, 1, active)
	var cond string
	require.NoError(t, f.db.Get(&cond, `SELECT condition FROM task_dependencies
		WHERE task_id = $1 AND depends_on_task_id = $2 AND dependency_type = 'blocks'`, f.task, f.other))
	assert.Equal(t, "shipped", cond)

	// The same card again reuses the edge.
	f.checkout(t, f.task, f.agent)
	require.Equal(t, http.StatusCreated, f.post(f.agent, "/wait", map[string]any{"on": "card", "target_task_id": f.other}).Code)
	assert.Equal(t, 1, f.counts(t).Deps)

	// An open MR is a valid target.
	f.checkout(t, f.task, f.agent)
	rec = f.post(f.agent, "/wait", map[string]any{"on": "mr", "provider": "gitlab", "project_path": "g/p", "mr_iid": 10, "until": "merged"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

func TestTurnCommands_NextHandoffNote_DB(t *testing.T) {
	f := newTurnFixture(t)
	var before time.Time
	require.NoError(t, f.db.Get(&before, `SELECT checkout_expires FROM tasks WHERE id = $1`, f.task))

	rec := f.post(f.agent, "/next", map[string]any{"note": "tests written"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var next domain.TurnCommandResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &next))
	assert.False(t, next.Released)
	assert.Equal(t, domain.TurnOutcomeNext, next.Outcome.Kind)
	h := f.holder(t)
	require.NotNil(t, h)
	assert.Equal(t, f.agent, *h)
	var after time.Time
	require.NoError(t, f.db.Get(&after, `SELECT checkout_expires FROM tasks WHERE id = $1`, f.task))
	assert.False(t, after.Before(before), "continue_next must not shorten the checkout")

	rec = f.post(f.agent, "/notes", map[string]any{"text": "a quiet note"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var note struct {
		CommentID uuid.UUID `json:"comment_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &note))
	var meta []byte
	require.NoError(t, f.db.Get(&meta, `SELECT metadata FROM comments WHERE id = $1`, note.CommentID))
	assert.False(t, domain.CommentWakes(meta))
	assert.Equal(t, 1, f.counts(t).Outcomes, "add_note writes no outcome")

	// A wait to be cancelled by the hand-off.
	require.Equal(t, http.StatusCreated, f.post(f.agent, "/wait", timeEvidence(map[string]any{"on": "time", "until_at": time.Now().Add(time.Hour)})).Code)
	f.checkout(t, f.task, f.agent)
	rec = f.post(f.agent, "/handoff", map[string]any{"to": f.agent2Name, "why": "needs the frontend lane"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var ho domain.TurnCommandResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ho))
	assert.True(t, ho.Released)
	assert.Equal(t, f.todoStatus, ho.Task.Status)
	require.NotNil(t, ho.Task.AssigneeID)
	assert.Equal(t, f.agent2, *ho.Task.AssigneeID)
	assert.Nil(t, f.holder(t))
	var active int
	require.NoError(t, f.db.Get(&active, `SELECT count(*) FROM task_waits WHERE task_id = $1 AND resolved_at IS NULL`, f.task))
	assert.Equal(t, 0, active, "hand_off cancels the caller's wait")
}

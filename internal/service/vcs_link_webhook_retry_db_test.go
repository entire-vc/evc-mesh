package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// #b593c566: a GitLab "closed" MR delivery whose link upsert failed was
// answered 200 and never retried, so the link stayed open forever. The
// handler now answers 5xx so the provider redelivers; this proves the
// redelivery is SAFE end to end on real Postgres: the first attempt fails at
// the upsert (a real CHECK violation, not a stub), the second succeeds, and
// the task ends with exactly one link row, status closed, freshly synced,
// and exactly one "closed without merge" comment — no duplicate from the
// failed attempt.
//
// Fault injection reuses task_durable_db_test.go's shadow-schema technique:
// a per-test schema holding a vcs_links copy that refuses status='closed',
// placed first on search_path of a single-connection pool, so no other test
// package sharing this database ever sees it.
func TestHandleMR_ClosedRedeliveryAfterUpsertFailure_OneLinkClosedOneComment(t *testing.T) {
	f := newRecurringActorTypeFixture(t)
	ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	f.db.SetMaxOpenConns(1)
	f.db.SetMaxIdleConns(1)

	taskRepo := postgres.NewTaskRepo(f.db)
	statusRepo := postgres.NewTaskStatusRepo(f.db)
	task := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: f.statusID, Title: "mr retry fixture", AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedByType: domain.ActorTypeSystem}
	require.NoError(t, f.taskSvc.Create(ctx, task))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`SET search_path=public`)
		_, _ = f.db.Exec(`DELETE FROM comments WHERE task_id=$1`, task.ID)
		_, _ = f.db.Exec(`DELETE FROM vcs_links WHERE task_id=$1`, task.ID)
		_, _ = f.db.Exec(`DELETE FROM activity_log WHERE workspace_id=$1`, f.workspaceID)
	})

	linkRepo := postgres.NewVCSLinkRepo(f.db)
	svc := NewVCSLinkService(linkRepo,
		WithVCSTaskRepo(taskRepo),
		WithVCSStatusRepo(statusRepo),
		WithVCSTaskService(f.taskSvc),
		WithVCSCommentService(NewCommentService(postgres.NewCommentRepo(f.db), taskRepo, postgres.NewActivityLogRepo(f.db))),
	)

	ev := GitLabWebhookEvent{
		Action: "open", MRIID: 314, MRTitle: "MESH-" + task.ID.String(),
		MRURL:   "https://git.entire.host/entire-vc/evc-mesh/-/merge_requests/314",
		MRState: "opened", ProjectPath: "entire-vc/evc-mesh",
	}
	_, err := svc.HandleGitLabMergeRequestEvent(ctx, ev)
	require.NoError(t, err, "opened delivery records the open link")

	// Break the next closed write for real.
	schema := pq.QuoteIdentifier("vcs_retry_" + strings.ReplaceAll(uuid.NewString(), "-", ""))
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifier is pq.QuoteIdentifier of a generated UUID; no external input
	_, err = f.db.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifier is pq.QuoteIdentifier of a generated UUID; no external input
		_, _ = f.db.Exec(`SET search_path=public; DROP SCHEMA ` + schema + ` CASCADE`)
	})
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifier is pq.QuoteIdentifier of a generated UUID; no external input
	_, err = f.db.Exec(`CREATE TABLE ` + schema + `.vcs_links (LIKE public.vcs_links INCLUDING ALL)`)
	require.NoError(t, err)
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifier is pq.QuoteIdentifier of a generated UUID; no external input
	_, err = f.db.Exec(`ALTER TABLE ` + schema + `.vcs_links ADD CHECK (status <> 'closed')`)
	require.NoError(t, err)
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifier is pq.QuoteIdentifier of a generated UUID; no external input
	_, err = f.db.Exec(`SET search_path=` + schema + `,public`)
	require.NoError(t, err)

	closed := ev
	closed.Action, closed.MRState = "close", "closed"
	_, err = svc.HandleGitLabMergeRequestEvent(ctx, closed)
	require.Error(t, err, "first closed delivery must fail at the upsert (the handler turns this into a 5xx)")
	assert.Contains(t, err.Error(), "upsert vcs link")

	// Outage over; the provider redelivers the same payload.
	_, err = f.db.Exec(`SET search_path=public`)
	require.NoError(t, err)
	res, err := svc.HandleGitLabMergeRequestEvent(ctx, closed)
	require.NoError(t, err)
	assert.Equal(t, "closed_without_merge", res.Reason)

	links, err := linkRepo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, links, 1, "redelivery must update the existing row, not add one")
	assert.Equal(t, domain.VCSLinkStatusClosed, links[0].Status)
	require.NotNil(t, links[0].StatusSyncedAt)

	var comments int
	require.NoError(t, f.db.Get(&comments, `SELECT count(*) FROM comments WHERE task_id=$1 AND body LIKE '%MR !314 closed without merge%'`, task.ID))
	assert.Equal(t, 1, comments, "the failed attempt must not have posted a comment the retry then duplicates")
}

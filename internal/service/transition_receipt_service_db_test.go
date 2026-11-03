package service

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// The receipt service against real Postgres and the real repos; no mocks. No
// build tag: it skips when no Postgres is reachable (CI provides one).
func TestTransitionReceiptService_PolicyAndProductDone(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the transition receipt service against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres: %v", err)
	}
	defer func() { _ = db.Close() }()

	ws, proj, todo, done, task, link := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	producer, acceptor := uuid.New(), uuid.New()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'receipt svc',$2,$3)`, []any{ws, ws.String(), uuid.New()}},
		{`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1,$2,'p',$3)`, []any{proj, ws, proj.String()[:8]}},
		{`INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1,$2,'Todo','todo','todo'::task_status_category,0)`, []any{todo, proj}},
		{`INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1,$2,'Done','done','done'::task_status_category,1)`, []any{done, proj}},
		{`INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by) VALUES ($1,$2,$3,'svc task',1,$4)`, []any{task, proj, todo, uuid.New()}},
		{`INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,status,metadata) VALUES($1,$2,'gitlab','pr','3','https://git.example/mr/3','merged','{"head_sha":"abc","ship_sha":"abc"}'::jsonb)`, []any{link, task}},
	} {
		_, err = db.Exec(q.sql, q.args...)
		require.NoError(t, err)
	}

	svc := NewTransitionReceiptService(postgres.NewTransitionReceiptRepo(db), postgres.NewTaskRepo(db))
	actor := actorctx.WithActor(context.Background(), producer, domain.ActorTypeAgent)

	// An unauthenticated caller can neither set the policy nor commit.
	_, err = svc.SetPolicy(context.Background(), &domain.TransitionReceiptPolicy{ProjectID: proj})
	require.Error(t, err)
	_, _, err = svc.ProductDone(context.Background(), task, domain.ProductDoneRequest{})
	require.Error(t, err)

	// Default policy is off.
	p, err := svc.GetPolicy(actor, proj)
	require.NoError(t, err)
	require.False(t, p.Enabled)

	p, err = svc.SetPolicy(actor, &domain.TransitionReceiptPolicy{
		ProjectID: proj, Enabled: true,
		AllowedProducers: []domain.AllowedProducer{{ProducerID: "board", ActorID: producer}},
		AllowedAcceptors: []uuid.UUID{acceptor},
	})
	require.NoError(t, err)
	require.Equal(t, 1, p.PolicyVersion)

	// A request that fails shape validation never reaches the store.
	_, _, err = svc.ProductDone(actor, task, domain.ProductDoneRequest{})
	require.Error(t, err)

	var version int64
	require.NoError(t, db.Get(&version, `SELECT version FROM tasks WHERE id=$1`, task))
	req := domain.ProductDoneRequest{
		Domain: domain.TransitionDomainProduct, Class: domain.TransitionClassCode, Operation: domain.TransitionOpProductDone,
		ToStatusID: done, ExpectedStatusID: todo, ExpectedVersion: &version, IdempotencyKey: "svc",
		ProducerID: "board", AcceptingActorID: acceptor, Reason: "accepted",
		Evidence: domain.ProductDoneEvidence{VCSLinkID: &link, HeadSHA: "abc"},
	}
	rc, replayed, err := svc.ProductDone(actor, task, req)
	require.NoError(t, err)
	require.False(t, replayed)
	again, replayed, err := svc.ProductDone(actor, task, req)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, rc.ID, again.ID)

	// An unknown task is not found.
	_, _, err = svc.ProductDone(actor, uuid.New(), req)
	require.Error(t, err)

	list, err := svc.ListReceipts(actor, task)
	require.NoError(t, err)
	require.Len(t, list, 1)

	// Re-saving the policy advances its version.
	p, err = svc.SetPolicy(actor, &domain.TransitionReceiptPolicy{ProjectID: proj, Enabled: false})
	require.NoError(t, err)
	require.Equal(t, 2, p.PolicyVersion)
}

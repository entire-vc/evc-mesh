package postgres

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

const (
	headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type receiptFixture struct {
	t         *testing.T
	db        *sqlx.DB
	repo      *TransitionReceiptRepo
	taskID    uuid.UUID
	projectID uuid.UUID
	todoID    uuid.UUID
	doneID    uuid.UUID
	linkID    uuid.UUID
	producer  uuid.UUID // credential actor
	acceptor  uuid.UUID // independent accepting identity
}

// newReceiptFixture builds a project with the policy ON and one task carrying a
// merged pr link whose head and ship sha agree: the one fully valid case. Every
// negative test below changes exactly one thing from it.
func newReceiptFixture(t *testing.T) *receiptFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the transition receipt repo against PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	ws, projID, todoID := uuid.New(), uuid.New(), uuid.New()
	f := &receiptFixture{t: t, db: db, repo: NewTransitionReceiptRepo(db), projectID: projID, todoID: todoID,
		doneID: uuid.New(), linkID: uuid.New(), producer: uuid.New(), acceptor: uuid.New(), taskID: uuid.New()}
	f.exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'receipt repo',$2,$3)`, ws, ws.String(), uuid.New())
	f.exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1, $2, 'p', $3)`, projID, ws, projID.String()[:8])
	f.exec(`INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1,$2,'Todo','todo','todo'::task_status_category,0)`, todoID, projID)
	f.exec(`INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1,$2,'Done','done','done'::task_status_category,1)`, f.doneID, projID)
	f.exec(`INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by) VALUES ($1,$2,$3,'receipt fixture',1,$4)`, f.taskID, projID, todoID, uuid.New())
	f.exec(`INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,status,metadata) VALUES($1,$2,'gitlab','pr','7','https://git.example/mr/7','merged',$3::jsonb)`,
		f.linkID, f.taskID, `{"head_sha":"`+headA+`","ship_sha":"`+headA+`"}`)
	_, err = f.repo.UpsertPolicy(ctx, &domain.TransitionReceiptPolicy{
		ProjectID: projID, Enabled: true,
		AllowedProducers:  []domain.AllowedProducer{{ProducerID: "board", ActorID: f.producer}},
		AllowedAcceptors:  []uuid.UUID{f.acceptor},
		AllowedExemptions: []string{domain.TransitionClassResearch},
	}, f.producer)
	require.NoError(t, err)
	return f
}

func (f *receiptFixture) exec(q string, args ...any) {
	f.t.Helper()
	_, err := f.db.Exec(q, args...)
	require.NoError(f.t, err)
}

func (f *receiptFixture) count(q string, args ...any) (n int) {
	f.t.Helper()
	require.NoError(f.t, f.db.Get(&n, q, args...))
	return n
}

func (f *receiptFixture) state() (status uuid.UUID, version int64) {
	f.t.Helper()
	require.NoError(f.t, f.db.QueryRow(`SELECT status_id, version FROM tasks WHERE id=$1`, f.taskID).Scan(&status, &version))
	return
}

// request is the valid code-class request against the current task state.
func (f *receiptFixture) request(key string) domain.TransitionCommitInput {
	_, v := f.state()
	return domain.TransitionCommitInput{
		TaskID: f.taskID, ActorID: f.producer, ActorType: domain.ActorTypeAgent,
		Request: domain.ProductDoneRequest{
			Domain: domain.TransitionDomainProduct, Class: domain.TransitionClassCode, Operation: domain.TransitionOpProductDone,
			ToStatusID: f.doneID, ExpectedStatusID: f.todoID, ExpectedVersion: &v, IdempotencyKey: key,
			ProducerID: "board", ProducerSession: "sess-1", SourceRef: "board@abc", SourceHash: "deadbeef",
			AcceptingActorID: f.acceptor, AcceptanceRef: "https://mesh.example/c/1", Reason: "accepted on exact head",
			Evidence: domain.ProductDoneEvidence{VCSLinkID: &f.linkID, HeadSHA: headA},
		},
	}
}

func TestCommitProductDone_ValidReceipt(t *testing.T) {
	f := newReceiptFixture(t)
	_, v0 := f.state()
	in := f.request("k1")
	rc, replayed, err := f.repo.CommitProductDone(context.Background(), in)
	require.NoError(t, err)
	require.False(t, replayed)
	status, v1 := f.state()
	require.Equal(t, f.doneID, status)
	require.Equal(t, v0, rc.FromVersion)
	require.Equal(t, v1, rc.ToVersion)
	require.Equal(t, f.todoID, rc.FromStatusID)
	require.Equal(t, domain.SourceTrustClaimed, rc.SourceTrust)
	require.Equal(t, f.producer, rc.CredentialActorID)
	require.Equal(t, f.acceptor, rc.AcceptingActorID)
	require.Equal(t, 1, rc.PolicyVersion)
	require.NotEmpty(t, rc.EvidenceHash)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved' AND new_version=$2`, f.taskID, v1))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM task_event_outbox WHERE task_id=$1 AND task_version=$2`, f.taskID, v1))
}

func TestCommitProductDone_Refusals(t *testing.T) {
	cases := []struct {
		name string
		code string // TransitionRefusal code; "cas" = TaskConflict
		mut  func(f *receiptFixture, in *domain.TransitionCommitInput)
	}{
		{"policy off", "transition_policy_disabled", func(f *receiptFixture, in *domain.TransitionCommitInput) {
			f.exec(`UPDATE transition_receipt_policies SET enabled=false WHERE project_id=$1`, f.projectID)
		}},
		{"stale version", "cas", func(f *receiptFixture, in *domain.TransitionCommitInput) {
			v := *in.Request.ExpectedVersion - 1
			in.Request.ExpectedVersion = &v
		}},
		{"stale status", "cas", func(f *receiptFixture, in *domain.TransitionCommitInput) { in.Request.ExpectedStatusID = f.doneID }},
		{"unknown producer", "producer_not_authorized", func(f *receiptFixture, in *domain.TransitionCommitInput) { in.Request.ProducerID = "forged" }},
		{"producer bound to other actor", "producer_not_authorized", func(f *receiptFixture, in *domain.TransitionCommitInput) { in.ActorID = uuid.New() }},
		{"unallowed acceptor", "acceptor_not_authorized", func(f *receiptFixture, in *domain.TransitionCommitInput) { in.Request.AcceptingActorID = uuid.New() }},
		{"acceptor is the credential actor", "acceptor_not_independent", func(f *receiptFixture, in *domain.TransitionCommitInput) {
			f.exec(`UPDATE transition_receipt_policies SET allowed_acceptors=allowed_acceptors||$1::uuid WHERE project_id=$2`, f.producer, f.projectID)
			in.Request.AcceptingActorID = f.producer
		}},
		{"head changed", "evidence_head_changed", func(f *receiptFixture, in *domain.TransitionCommitInput) { in.Request.Evidence.HeadSHA = headB }},
		{"link has no head", "evidence_missing_head", func(f *receiptFixture, in *domain.TransitionCommitInput) {
			f.exec(`UPDATE vcs_links SET metadata='{}'::jsonb WHERE id=$1`, f.linkID)
			_, v := f.state()
			in.Request.ExpectedVersion = &v
		}},
		{"no ship on head", "evidence_not_shipped", func(f *receiptFixture, in *domain.TransitionCommitInput) {
			f.exec(`UPDATE vcs_links SET metadata=jsonb_set(metadata,'{ship_sha}',to_jsonb($2::text)) WHERE id=$1`, f.linkID, headB)
			_, v := f.state()
			in.Request.ExpectedVersion = &v
		}},
		{"link not merged", "evidence_not_merged", func(f *receiptFixture, in *domain.TransitionCommitInput) {
			f.exec(`UPDATE vcs_links SET status='open' WHERE id=$1`, f.linkID)
			_, v := f.state()
			in.Request.ExpectedVersion = &v
		}},
		{"link of another task", "evidence_missing", func(f *receiptFixture, in *domain.TransitionCommitInput) {
			other := uuid.New()
			in.Request.Evidence.VCSLinkID = &other
		}},
		{"exemption type not allowed by policy", "exemption_not_allowed", func(f *receiptFixture, in *domain.TransitionCommitInput) {
			in.Request.Class = domain.TransitionClassTrivial
			in.Request.Evidence = domain.ProductDoneEvidence{ExemptionType: domain.TransitionClassTrivial, ArtifactRef: "doc:1"}
		}},
		{"active human gate", "human_gate_active", func(f *receiptFixture, in *domain.TransitionCommitInput) {
			f.exec(`UPDATE tasks SET human_gate=true WHERE id=$1`, f.taskID)
			_, v := f.state()
			in.Request.ExpectedVersion = &v
		}},
		{"registered parked wait", "parked_wait_active", func(f *receiptFixture, in *domain.TransitionCommitInput) {
			f.exec(`INSERT INTO parked_waits (id, task_id, wait_comment_id, plan, registered_by, registered_by_type, result)
 VALUES ($1,$2,$3,'{}'::jsonb,$4,'agent','{}'::jsonb)`, uuid.New(), f.taskID, uuid.New(), uuid.New())
			_, v := f.state()
			in.Request.ExpectedVersion = &v
		}},
		{"target is not done", "target_not_done_status", func(f *receiptFixture, in *domain.TransitionCommitInput) { in.Request.ToStatusID = f.todoID }},
		{"dod gate", "dod_gate_blocked", func(f *receiptFixture, in *domain.TransitionCommitInput) {
			in.DoDBlocking = func(domain.ProjectSettings, domain.DodChecks) []string { return []string{"qa"} }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReceiptFixture(t)
			in := f.request("k-" + tc.name)
			tc.mut(f, &in)
			_, before := f.state()
			_, _, err := f.repo.CommitProductDone(context.Background(), in)
			require.Error(t, err)
			if tc.code == "cas" {
				var c *domain.TaskConflict
				require.True(t, errors.As(err, &c), "want TaskConflict, got %v", err)
			} else {
				var r *domain.TransitionRefusal
				require.True(t, errors.As(err, &r), "want refusal %s, got %v", tc.code, err)
				require.Equal(t, tc.code, r.Code)
			}
			status, v := f.state()
			require.Equal(t, before, v, "version must not advance on refusal")
			require.NotEqual(t, f.doneID, status, "state must not change on refusal")
			require.Zero(t, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
		})
	}
}

func TestCommitProductDone_ReplayAndKeyConflict(t *testing.T) {
	f := newReceiptFixture(t)
	ctx := context.Background()
	in := f.request("same")
	first, replayed, err := f.repo.CommitProductDone(ctx, in)
	require.NoError(t, err)
	require.False(t, replayed)

	// Same request, the version has moved on: the same receipt comes back.
	again, replayed, err := f.repo.CommitProductDone(ctx, in)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, first.ID, again.ID)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM task_event_outbox WHERE task_id=$1 AND action='task.moved' AND task_version=$2`, f.taskID, first.ToVersion))

	// Same key, different payload: conflict, nothing written.
	other := in
	other.Request.Reason = "a different reason"
	_, _, err = f.repo.CommitProductDone(ctx, other)
	require.ErrorIs(t, err, domain.ErrReceiptKeyConflict)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
}

func TestCommitProductDone_ConcurrentOneReceipt(t *testing.T) {
	f := newReceiptFixture(t)
	in := f.request("race")
	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	fresh, replays := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, replayed, err := f.repo.CommitProductDone(context.Background(), in)
			require.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			if replayed {
				replays++
			} else {
				fresh++
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, fresh)
	require.Equal(t, n-1, replays)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved' AND source='transition_receipt'`, f.taskID))
}

func TestCommitProductDone_AuditFailureRollsBack(t *testing.T) {
	f := newReceiptFixture(t)
	in := f.request("audit")
	_, v := f.state()
	// Occupy the outbox id the commit will derive (task, new version, action):
	// the audit insert fails after the state update and the receipt insert.
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("task:"+f.taskID.String()+":"+itoa(v+1)+":task.moved"))
	f.exec(`INSERT INTO task_event_outbox(id,task_id,task_version,action,message) VALUES($1,$2,$3,'task.moved','{}'::jsonb)`, id, f.taskID, v+1)
	_, _, err := f.repo.CommitProductDone(context.Background(), in)
	require.Error(t, err)
	status, v2 := f.state()
	require.Equal(t, f.todoID, status)
	require.Equal(t, v, v2)
	require.Zero(t, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
}

func TestReceipt_ImmutableAndSurvivesVCSEdit(t *testing.T) {
	f := newReceiptFixture(t)
	ctx := context.Background()
	rc, _, err := f.repo.CommitProductDone(ctx, f.request("imm"))
	require.NoError(t, err)

	// A later edit of the link's metadata, and its removal, do not reach the receipt.
	f.exec(`UPDATE vcs_links SET metadata='{"head_sha":"`+headB+`","ship_sha":"`+headB+`"}'::jsonb WHERE id=$1`, f.linkID)
	f.exec(`DELETE FROM vcs_links WHERE id=$1`, f.linkID)
	list, err := f.repo.ListReceipts(ctx, f.taskID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.JSONEq(t, string(rc.Evidence), string(list[0].Evidence))
	require.Equal(t, rc.EvidenceHash, list[0].EvidenceHash)
	require.Contains(t, string(list[0].Evidence), headA)

	// The application writer cannot rewrite or delete it.
	_, err = f.db.Exec(`UPDATE task_transition_receipts SET reason='x' WHERE id=$1`, rc.ID)
	require.Error(t, err)
	_, err = f.db.Exec(`DELETE FROM task_transition_receipts WHERE id=$1`, rc.ID)
	require.Error(t, err)
}

func TestCommitProductDone_ResearchExemption(t *testing.T) {
	f := newReceiptFixture(t)
	in := f.request("research")
	in.Request.Class = domain.TransitionClassResearch
	in.Request.Evidence = domain.ProductDoneEvidence{ExemptionType: domain.TransitionClassResearch, ArtifactRef: "doc:abc@v1"}
	rc, _, err := f.repo.CommitProductDone(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, domain.ReceiptEvidenceExemption, rc.EvidenceKind)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// Required DoD gates are read inside the commit transaction: a gate added to
// the project settings after the request was built still refuses the commit.
func TestCommitProductDone_DoDGateReadAtCommit(t *testing.T) {
	f := newReceiptFixture(t)
	in := f.request("dod")
	in.DoDBlocking = func(s domain.ProjectSettings, checks domain.DodChecks) []string {
		var blocking []string
		for _, g := range s.DodGates {
			if c, ok := checks[g.Name]; g.Required && (!ok || c.Status != domain.DodCheckPass) {
				blocking = append(blocking, g.Name)
			}
		}
		return blocking
	}
	_, before := f.state()

	f.exec(`UPDATE projects SET settings='{"dod_gates":[{"name":"qa","required":true}]}'::jsonb WHERE id=$1`, f.projectID)
	_, _, err := f.repo.CommitProductDone(context.Background(), in)
	var r *domain.TransitionRefusal
	require.True(t, errors.As(err, &r), "want refusal, got %v", err)
	require.Equal(t, "dod_gate_blocked", r.Code)
	status, v := f.state()
	require.Equal(t, f.todoID, status)
	require.Equal(t, before, v)
	require.Zero(t, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))

	f.exec(`UPDATE projects SET settings='{}'::jsonb WHERE id=$1`, f.projectID)
	_, _, err = f.repo.CommitProductDone(context.Background(), in)
	require.NoError(t, err)
}

// The commit path holds a share lock on the policy row: a policy update (here
// a disable) waits for the open transaction instead of landing under it.
func TestCommitProductDone_PolicyUpdateWaitsForCommit(t *testing.T) {
	f := newReceiptFixture(t)
	ctx := context.Background()
	tx, err := f.db.BeginTxx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	p, err := getPolicy(ctx, tx, f.projectID, " FOR SHARE")
	require.NoError(t, err)
	require.True(t, p.Enabled)

	off := *p
	off.Enabled = false
	short, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_, err = f.repo.UpsertPolicy(short, &off, uuid.New())
	require.Error(t, err, "the update must block while the share lock is held")

	require.NoError(t, tx.Commit())
	_, err = f.repo.UpsertPolicy(ctx, &off, uuid.New())
	require.NoError(t, err)
}

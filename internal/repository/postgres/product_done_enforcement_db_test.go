package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// enforcedFixture is the receipt fixture with enforce_done armed, plus an
// executor (the producer credential) and an independent reviewer acceptor.
type enforcedFixture struct {
	*receiptFixture
	tasks *TaskRepo
}

func newEnforcedFixture(t *testing.T) *enforcedFixture {
	t.Helper()
	f := &enforcedFixture{receiptFixture: newReceiptFixture(t)}
	f.tasks = NewTaskRepo(f.db)
	f.setEnforce(true)
	return f
}

func (f *enforcedFixture) setEnforce(on bool) {
	f.t.Helper()
	_, err := f.repo.UpsertPolicy(context.Background(), &domain.TransitionReceiptPolicy{
		ProjectID: f.projectID, Enabled: true, EnforceDone: on,
		AllowedProducers:  []domain.AllowedProducer{{ProducerID: "board", ActorID: f.producer}},
		AllowedAcceptors:  []uuid.UUID{f.acceptor},
		AllowedExemptions: []string{domain.TransitionClassResearch},
	}, f.producer)
	require.NoError(f.t, err)
}

func (f *enforcedFixture) intentReq() domain.AcceptanceIntentRequest {
	_, v := f.state()
	return domain.AcceptanceIntentRequest{
		Domain: domain.TransitionDomainProduct, Class: domain.TransitionClassCode, Operation: domain.TransitionOpProductDone,
		ToStatusID: f.doneID, ExpectedStatusID: f.todoID, ExpectedVersion: &v,
		Evidence: domain.ProductDoneEvidence{VCSLinkID: &f.linkID, HeadSHA: headA}, Reason: "reviewed exact head",
		DeployRef: "deploy-1", LiveRef: "live-1",
	}
}

func (f *enforcedFixture) accept() *domain.AcceptanceIntent {
	f.t.Helper()
	it, err := f.repo.CreateAcceptanceIntent(context.Background(), domain.AcceptanceIntentInput{
		TaskID: f.taskID, Request: f.intentReq(), ActorID: f.acceptor, ActorType: domain.ActorTypeAgent})
	require.NoError(f.t, err)
	return it
}

func (f *enforcedFixture) commitInput(key string, it *domain.AcceptanceIntent) domain.TransitionCommitInput {
	in := f.request(key)
	in.Request.AcceptingActorID = it.AcceptorActorID
	in.Request.AcceptanceIntentID = &it.ID
	return in
}

func (f *enforcedFixture) requireDenied(err error, what string) {
	f.t.Helper()
	require.Error(f.t, err, what)
	require.True(f.t, strings.Contains(err.Error(), "product_done_receipt_required"), "%s: want the guard error, got %v", what, err)
	status, _ := f.state()
	require.Equal(f.t, f.todoID, status, what+": state must not change")
}

func TestEnforce_ValidCommitWritesOneStateReceiptAuditOutbox(t *testing.T) {
	f := newEnforcedFixture(t)
	it := f.accept()
	in := f.commitInput("e1", it)
	rc, replayed, err := f.repo.CommitProductDone(context.Background(), in)
	require.NoError(t, err)
	require.False(t, replayed)
	require.Equal(t, it.ID, *rc.AcceptanceIntentID)
	require.Equal(t, it.AcceptorActorID, rc.AcceptingActorID)
	status, _ := f.state()
	require.Equal(t, f.doneID, status)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM task_event_outbox WHERE task_id=$1 AND action='task.moved'`, f.taskID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved' AND source='transition_receipt'`, f.taskID))

	// same key and payload: the same receipt, no second state event
	again, replayed, err := f.repo.CommitProductDone(context.Background(), in)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, rc.ID, again.ID)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM task_event_outbox WHERE task_id=$1 AND action='task.moved'`, f.taskID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved' AND source='transition_receipt'`, f.taskID), "a replay writes no second audit row")
}

// Concurrent same-key/payload requests under enforce_done: exactly one commit
// (state + receipt + audit + outbox), and every other caller gets that receipt.
func TestEnforce_ConcurrentSameKeyOneReceipt(t *testing.T) {
	f := newEnforcedFixture(t)
	it := f.accept()
	in := f.commitInput("race", it)
	const n = 8
	type result struct {
		id       uuid.UUID
		replayed bool
		err      error
	}
	out := make(chan result, n)
	for i := 0; i < n; i++ {
		go func() {
			rc, replayed, err := f.repo.CommitProductDone(context.Background(), in)
			r := result{replayed: replayed, err: err}
			if rc != nil {
				r.id = rc.ID
			}
			out <- r
		}()
	}
	fresh, replays := 0, 0
	var first uuid.UUID
	for i := 0; i < n; i++ {
		r := <-out
		require.NoError(t, r.err)
		if first == uuid.Nil {
			first = r.id
		}
		require.Equal(t, first, r.id, "every caller gets the same receipt")
		if r.replayed {
			replays++
		} else {
			fresh++
		}
	}
	require.Equal(t, 1, fresh)
	require.Equal(t, n-1, replays)
	status, _ := f.state()
	require.Equal(t, f.doneID, status)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM task_event_outbox WHERE task_id=$1 AND action='task.moved'`, f.taskID))
	require.Equal(t, 1, f.count(`SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved' AND source='transition_receipt'`, f.taskID))
}

func TestEnforce_TypedPathRefusals(t *testing.T) {
	cases := map[string]func(f *enforcedFixture, in *domain.TransitionCommitInput, it **domain.AcceptanceIntent){
		"no intent": func(f *enforcedFixture, in *domain.TransitionCommitInput, _ **domain.AcceptanceIntent) {
			in.Request.AcceptanceIntentID = nil
		},
		"forged intent ref": func(f *enforcedFixture, in *domain.TransitionCommitInput, _ **domain.AcceptanceIntent) {
			id := uuid.New()
			in.Request.AcceptanceIntentID = &id
		},
		"accepting_actor_id differs from authenticated acceptor": func(f *enforcedFixture, in *domain.TransitionCommitInput, _ **domain.AcceptanceIntent) {
			in.Request.AcceptingActorID = f.producer
		},
		"wrong class": func(f *enforcedFixture, in *domain.TransitionCommitInput, _ **domain.AcceptanceIntent) {
			in.Request.Class = domain.TransitionClassConfig
		},
		"head changed after acceptance": func(f *enforcedFixture, _ *domain.TransitionCommitInput, _ **domain.AcceptanceIntent) {
			f.exec(`UPDATE vcs_links SET metadata=$2::jsonb WHERE id=$1`, f.linkID, `{"head_sha":"`+headB+`","ship_sha":"`+headB+`"}`)
		},
		"intent revoked": func(f *enforcedFixture, _ *domain.TransitionCommitInput, it **domain.AcceptanceIntent) {
			require.NoError(f.t, f.repo.RevokeAcceptanceIntent(context.Background(), f.taskID, (*it).ID, f.acceptor, "withdrawn"))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newEnforcedFixture(t)
			it := f.accept()
			in := f.commitInput("r-"+uuid.NewString()[:6], it)
			mutate(f, &in, &it)
			// a mutation may legitimately touch the task version (a link edit
			// does); the request carries the current one so the refusal is for
			// the mutated input and not for a stale CAS
			_, vNow := f.state()
			in.Request.ExpectedVersion = &vNow
			_, _, err := f.repo.CommitProductDone(context.Background(), in)
			require.Error(t, err)
			f.assertNoCommit(vNow)
		})
	}
}

func TestEnforce_StaleIntentAfterTaskMoved(t *testing.T) {
	f := newEnforcedFixture(t)
	it := f.accept()
	f.exec(`UPDATE tasks SET title='edited' WHERE id=$1`, f.taskID) // bumps version
	_, _, err := f.repo.CommitProductDone(context.Background(), f.commitInput("stale", it))
	require.Error(t, err)
	status, _ := f.state()
	require.Equal(t, f.todoID, status)
	require.Zero(t, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
}

func TestEnforce_IntentIsSingleUse(t *testing.T) {
	f := newEnforcedFixture(t)
	it := f.accept()
	_, _, err := f.repo.CommitProductDone(context.Background(), f.commitInput("one", it))
	require.NoError(t, err)
	f.exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, f.taskID, f.todoID) // leave done (guard only covers entering)
	_, _, err = f.repo.CommitProductDone(context.Background(), f.commitInput("two", it))
	require.Error(t, err)
	require.Equal(t, 1, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
}

func TestEnforce_AcceptorMustBeIndependent(t *testing.T) {
	f := newEnforcedFixture(t)
	// the executor credential is also on the acceptor allow-list: still refused
	_, err := f.repo.UpsertPolicy(context.Background(), &domain.TransitionReceiptPolicy{
		ProjectID: f.projectID, Enabled: true, EnforceDone: true,
		AllowedProducers: []domain.AllowedProducer{{ProducerID: "board", ActorID: f.producer}},
		AllowedAcceptors: []uuid.UUID{f.acceptor, f.producer},
	}, f.producer)
	require.NoError(t, err)
	_, err = f.repo.CreateAcceptanceIntent(context.Background(), domain.AcceptanceIntentInput{
		TaskID: f.taskID, Request: f.intentReq(), ActorID: f.producer, ActorType: domain.ActorTypeAgent})
	var r *domain.TransitionRefusal
	require.ErrorAs(t, err, &r)
	require.Equal(t, "acceptor_not_independent", r.Code)
}

func TestEnforce_NonAcceptorCannotRecordIntent(t *testing.T) {
	f := newEnforcedFixture(t)
	_, err := f.repo.CreateAcceptanceIntent(context.Background(), domain.AcceptanceIntentInput{
		TaskID: f.taskID, Request: f.intentReq(), ActorID: uuid.New(), ActorType: domain.ActorTypeAgent})
	var r *domain.TransitionRefusal
	require.ErrorAs(t, err, &r)
	require.Equal(t, "acceptor_not_authorized", r.Code)
	require.Zero(t, f.count(`SELECT count(*) FROM task_acceptance_intents WHERE task_id=$1`, f.taskID))
}

// leadFixture enables enforcement with an explicit lead producer that is also
// an allowed acceptor, and returns that lead's identity.
func (f *enforcedFixture) leadFixture() uuid.UUID {
	f.t.Helper()
	lead := uuid.New()
	_, err := f.repo.UpsertPolicy(context.Background(), &domain.TransitionReceiptPolicy{
		ProjectID: f.projectID, Enabled: true, EnforceDone: true,
		AllowedProducers: []domain.AllowedProducer{{ProducerID: "board", ActorID: f.producer}, {ProducerID: "lead.manual", ActorID: lead, Role: domain.ProducerRoleLead}},
		AllowedAcceptors: []uuid.UUID{f.acceptor, lead},
	}, f.producer)
	require.NoError(f.t, err)
	return lead
}

// asCheckoutHolder registers lead as an agent and makes it hold the checkout.
func (f *enforcedFixture) asCheckoutHolder(lead uuid.UUID) {
	f.t.Helper()
	f.exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix)
		SELECT $1, workspace_id, 'lead', $2, 'h', 'p' FROM projects WHERE id=$3`, lead, lead.String(), f.projectID)
	f.exec(`UPDATE tasks SET checked_out_by=$1 WHERE id=$2`, lead, f.taskID)
}

func (f *enforcedFixture) requireNothingWritten(before int) {
	f.t.Helper()
	require.Zero(f.t, f.count(`SELECT count(*) FROM task_acceptance_intents WHERE task_id=$1`, f.taskID))
	require.Zero(f.t, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
	require.Equal(f.t, before, f.count(`SELECT count(*) FROM task_event_outbox WHERE task_id=$1`, f.taskID))
	st, _ := f.state()
	require.Equal(f.t, f.todoID, st)
}

// Positive control: an independent lead (neither assignee nor checkout holder
// of the result) may accept another identity's result and execute the commit.
func TestEnforce_IndependentLeadAcceptsAndCommitsOthersResult(t *testing.T) {
	f := newEnforcedFixture(t)
	lead := f.leadFixture()
	it, err := f.repo.CreateAcceptanceIntent(context.Background(), domain.AcceptanceIntentInput{
		TaskID: f.taskID, Request: f.intentReq(), ActorID: lead, ActorType: domain.ActorTypeUser})
	require.NoError(t, err)
	require.Equal(t, domain.AcceptorRoleLead, it.AcceptorRole)
	in := f.commitInput("lead", it)
	in.ActorID, in.Request.ProducerID = lead, "lead.manual"
	rc, _, err := f.repo.CommitProductDone(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, domain.ProducerRoleLead, rc.ActingRole)
}

// Negative controls: whatever the role, the author of the result cannot accept it.
func TestEnforce_LeadCannotAcceptOwnResult(t *testing.T) {
	for _, tc := range []struct {
		name string
		own  func(f *enforcedFixture, lead uuid.UUID)
	}{
		{"assignee", func(f *enforcedFixture, lead uuid.UUID) {
			f.exec(`UPDATE tasks SET assignee_id=$1 WHERE id=$2`, lead, f.taskID)
		}},
		{"checkout holder", func(f *enforcedFixture, lead uuid.UUID) { f.asCheckoutHolder(lead) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEnforcedFixture(t)
			lead := f.leadFixture()
			tc.own(f, lead)
			outbox := f.count(`SELECT count(*) FROM task_event_outbox WHERE task_id=$1`, f.taskID)
			_, err := f.repo.CreateAcceptanceIntent(context.Background(), domain.AcceptanceIntentInput{
				TaskID: f.taskID, Request: f.intentReq(), ActorID: lead, ActorType: domain.ActorTypeUser})
			var r *domain.TransitionRefusal
			require.ErrorAs(t, err, &r)
			require.Equal(t, "acceptor_not_independent", r.Code)
			f.requireNothingWritten(outbox)
		})
	}
}

// Consumption re-validates: a lead intent recorded while independent is refused
// once that lead became the assignee of the result.
func TestEnforce_LeadIntentRefusedAfterLeadBecomesAssignee(t *testing.T) {
	f := newEnforcedFixture(t)
	lead := f.leadFixture()
	it, err := f.repo.CreateAcceptanceIntent(context.Background(), domain.AcceptanceIntentInput{
		TaskID: f.taskID, Request: f.intentReq(), ActorID: lead, ActorType: domain.ActorTypeUser})
	require.NoError(t, err)
	f.exec(`UPDATE tasks SET assignee_id=$1 WHERE id=$2`, lead, f.taskID)
	in := f.commitInput("lead-late", it)
	in.ActorID, in.Request.ProducerID = lead, "lead.manual"
	_, _, err = f.repo.CommitProductDone(context.Background(), in)
	var r *domain.TransitionRefusal
	require.ErrorAs(t, err, &r)
	require.Equal(t, "acceptor_not_independent", r.Code)
	require.Zero(t, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
	st, _ := f.state()
	require.Equal(t, f.todoID, st)
}

// Route controls: every entry that is not the typed path is denied in an
// enforced project, and the same entry succeeds when enforcement is off.
func TestEnforce_AlternateRoutesDenied(t *testing.T) {
	ctx := context.Background()
	routes := map[string]func(f *enforcedFixture) error{
		"raw sql under the application role": func(f *enforcedFixture) error {
			_, err := f.db.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, f.taskID, f.doneID)
			return err
		},
		"generic TaskRepo.Update": func(f *enforcedFixture) error {
			task, err := f.tasks.GetByID(ctx, f.taskID)
			require.NoError(f.t, err)
			task.StatusID = f.doneID
			return f.tasks.Update(ctx, task)
		},
		"UpdateTransition (MoveTask, auto_transition, VCS auto-done, bulk)": func(f *enforcedFixture) error {
			task, err := f.tasks.GetByID(ctx, f.taskID)
			require.NoError(f.t, err)
			task.StatusID = f.doneID
			return f.tasks.UpdateTransition(ctx, task, domain.TaskTransition{ExpectedVersion: task.Version,
				Audit: &domain.TransitionAudit{ActorID: f.producer, ActorType: domain.ActorTypeSystem, Source: "auto_transition", Reason: "route control",
					Entries: []domain.TaskAuditEntry{{Action: "task.moved", Changes: map[string]any{"route": "control"}}}}})
		},
		"create straight into done": func(f *enforcedFixture) error {
			return f.tasks.Create(ctx, f.newTask(f.projectID, f.doneID, "born done"), nil)
		},
		"re-categorise a populated status as done": func(f *enforcedFixture) error {
			_, err := f.db.Exec(`UPDATE task_statuses SET category='done' WHERE id=$1`, f.todoID)
			return err
		},
		"forged receipt without an intent": func(f *enforcedFixture) error {
			_, v := f.state()
			tx := f.db.MustBegin()
			defer func() { _ = tx.Rollback() }()
			if _, err := tx.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, f.taskID, f.doneID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO task_transition_receipts(id,task_id,project_id,domain,task_class,operation,from_status_id,from_version,to_status_id,to_version,
 producer_id,source_trust,credential_actor_id,credential_actor_type,policy_version,idempotency_key,payload_digest,accepting_actor_id,evidence_kind,evidence,evidence_hash,reason)
 VALUES($1,$2,$3,'product','code','product_done',$4,$5,$6,$7,'board','claimed',$8,'agent',1,'forged','x',$9,'vcs','{}','x','forged')`,
				uuid.New(), f.taskID, f.projectID, f.todoID, v, f.doneID, v+1, f.producer, f.acceptor); err != nil {
				return err
			}
			return tx.Commit()
		},
	}
	for name, route := range routes {
		t.Run(name, func(t *testing.T) {
			f := newEnforcedFixture(t)
			f.requireDenied(route(f), name)
		})
	}

	t.Run("policy-off positive control: the same raw write passes", func(t *testing.T) {
		f := newEnforcedFixture(t)
		f.setEnforce(false)
		_, err := f.db.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, f.taskID, f.doneID)
		require.NoError(t, err)
	})

	t.Run("enabled-only policy (foundation) does not arm the guard", func(t *testing.T) {
		f := newEnforcedFixture(t)
		f.exec(`UPDATE transition_receipt_policies SET enforce_done=false WHERE project_id=$1`, f.projectID)
		_, err := f.db.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, f.taskID, f.doneID)
		require.NoError(t, err)
	})

	t.Run("negative control: with the guard trigger disabled the bypass succeeds", func(t *testing.T) {
		f := newEnforcedFixture(t)
		f.exec(`ALTER TABLE tasks DISABLE TRIGGER tasks_product_done_guard`)
		t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE tasks ENABLE TRIGGER tasks_product_done_guard`) })
		_, err := f.db.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, f.taskID, f.doneID)
		require.NoError(t, err, "the control must be red on the unguarded baseline")
	})
}

func TestEnforce_CrossProjectRemapDenied(t *testing.T) {
	f := newEnforcedFixture(t)
	otherID, otherTodo := uuid.New(), uuid.New()
	f.exec(`INSERT INTO projects (id, workspace_id, name, slug) SELECT $1, workspace_id, 'other', $2 FROM projects WHERE id=$3`, otherID, otherID.String()[:8], f.projectID)
	f.exec(`INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1,$2,'Todo','todo','todo'::task_status_category,0)`, otherTodo, otherID)
	f.exec(`INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1,$2,'Done','done','done'::task_status_category,1)`, uuid.New(), otherID)
	// from an unguarded project into a done status of the enforced project
	otherTask := f.newTask(otherID, otherTodo, "remap")
	require.NoError(t, f.tasks.Create(context.Background(), otherTask, nil))
	err := f.tasks.MoveToProject(context.Background(), otherTask.ID, f.projectID, f.doneID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "product_done_receipt_required")
}

func (f *enforcedFixture) newTask(projectID, statusID uuid.UUID, title string) *domain.Task {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &domain.Task{
		ID: uuid.New(), ProjectID: projectID, StatusID: statusID, Title: title,
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, Position: 1.0,
		CustomFields: json.RawMessage(`{}`), Labels: pq.StringArray{},
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser, CreatedAt: now, UpdatedAt: now,
	}
}

// Policy race: a raw move prepared under policy-off must not slip through when
// enforcement commits before the move does.
func TestEnforce_PolicyEnableRacesWithOpenMove(t *testing.T) {
	f := newEnforcedFixture(t)
	f.setEnforce(false)
	tx := f.db.MustBegin()
	defer func() { _ = tx.Rollback() }()
	_, err := tx.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, f.taskID, f.doneID)
	require.NoError(t, err)
	f.setEnforce(true) // commits while the move is still open
	err = tx.Commit()
	require.Error(t, err)
	require.Contains(t, err.Error(), "product_done_receipt_required")
	status, _ := f.state()
	require.Equal(t, f.todoID, status)
}

// First-time enable race: with no policy row there is nothing for the guard to
// FOR SHARE, so the first enable could commit between the guard's check and the
// move's commit. The enable must wait for the open move instead.
func TestEnforce_FirstTimePolicyCreateRacesWithOpenMove(t *testing.T) {
	f := newEnforcedFixture(t)
	f.exec(`DELETE FROM transition_receipt_policies WHERE project_id=$1`, f.projectID)
	tx := f.db.MustBegin()
	defer func() { _ = tx.Rollback() }()
	_, err := tx.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, f.taskID, f.doneID)
	require.NoError(t, err)
	_, err = tx.Exec(`SET CONSTRAINTS ALL IMMEDIATE`) // the guard sees no policy row now
	require.NoError(t, err)

	enabled := make(chan error, 1)
	go func() {
		_, eerr := f.repo.UpsertPolicy(context.Background(), &domain.TransitionReceiptPolicy{
			ProjectID: f.projectID, Enabled: true, EnforceDone: true,
			AllowedProducers: []domain.AllowedProducer{{ProducerID: "board", ActorID: f.producer}},
			AllowedAcceptors: []uuid.UUID{f.acceptor},
		}, f.producer)
		enabled <- eerr
	}()
	select {
	case eerr := <-enabled:
		t.Fatalf("first-time enable returned while the move was still open: %v", eerr)
	case <-time.After(500 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	select {
	case eerr := <-enabled:
		require.NoError(t, eerr)
	case <-time.After(10 * time.Second):
		t.Fatal("enable never finished after the move committed")
	}
	// Enforcement is on from here: the next entry into done is refused.
	status, _ := f.state()
	require.Equal(t, f.doneID, status)
	_, err = f.db.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, f.taskID, f.todoID)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, f.taskID, f.doneID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "product_done_receipt_required")
}

// Late failure: the typed commit has already updated the task and written the
// receipt and audit row when the outbox insert is made to fail. Everything must
// roll back (task state and version, receipt, audit, outbox), and the same key
// must then commit cleanly, so nothing of the failed attempt is left behind. A
// refusal before the UPDATE does not prove this.
func TestEnforce_LateFailureRollsBackEverything(t *testing.T) {
	f := newEnforcedFixture(t)
	it := f.accept()
	_, versionBefore := f.state()
	trigger := "tr_late_fail_" + strings.ReplaceAll(f.taskID.String()[:8], "-", "")
	f.exec(`CREATE OR REPLACE FUNCTION ` + trigger + `() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.task_id = '` + f.taskID.String() + `' THEN RAISE EXCEPTION 'injected late failure'; END IF;
  RETURN NEW;
END $$`)
	f.exec(`CREATE TRIGGER ` + trigger + ` BEFORE INSERT ON task_event_outbox FOR EACH ROW EXECUTE FUNCTION ` + trigger + `()`)
	dropped := false
	drop := func() {
		if !dropped {
			dropped = true
			f.exec(`DROP TRIGGER ` + trigger + ` ON task_event_outbox`)
			f.exec(`DROP FUNCTION ` + trigger + `()`)
		}
	}
	t.Cleanup(drop)

	in := f.commitInput("late-fail-key", it)
	_, _, err := f.repo.CommitProductDone(context.Background(), in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "injected late failure")

	status, version := f.state()
	require.Equal(t, f.todoID, status, "the task update must roll back")
	require.Equal(t, versionBefore, version)
	require.Zero(t, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
	require.Zero(t, f.count(`SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved' AND source='transition_receipt'`, f.taskID))
	require.Zero(t, f.count(`SELECT count(*) FROM task_event_outbox WHERE task_id=$1 AND action='task.moved'`, f.taskID))

	drop()
	rc, replayed, err := f.repo.CommitProductDone(context.Background(), in)
	require.NoError(t, err)
	require.False(t, replayed, "the failed attempt left no receipt to replay")
	require.NotNil(t, rc)
	status, _ = f.state()
	require.Equal(t, f.doneID, status)
}

// Status race: a task entering a still-empty status must serialise with that
// status being re-categorised as done. Without the row lock both checks pass
// and the task ends up in a done-category status with no receipt.
func TestEnforce_StatusRecategoriseRacesWithTaskEntry(t *testing.T) {
	f := newEnforcedFixture(t)
	emptyID := uuid.New()
	f.exec(`INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1,$2,'Empty','empty','todo'::task_status_category,5)`, emptyID, f.projectID)

	tx := f.db.MustBegin()
	defer func() { _ = tx.Rollback() }()
	_, err := tx.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, f.taskID, emptyID)
	require.NoError(t, err)
	_, err = tx.Exec(`SET CONSTRAINTS ALL IMMEDIATE`) // the guard reads the old category now
	require.NoError(t, err)

	flipped := make(chan error, 1)
	go func() {
		_, ferr := f.db.Exec(`UPDATE task_statuses SET category='done'::task_status_category WHERE id=$1`, emptyID)
		flipped <- ferr
	}()
	select {
	case ferr := <-flipped:
		t.Fatalf("re-categorising returned while the task move was still open: %v", ferr)
	case <-time.After(500 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())

	select {
	case ferr := <-flipped:
		require.Error(t, ferr)
		require.Contains(t, ferr.Error(), "product_done_receipt_required")
	case <-time.After(10 * time.Second):
		t.Fatal("re-categorising never finished after the task move committed")
	}
	var category string
	require.NoError(t, f.db.Get(&category, `SELECT category::text FROM task_statuses WHERE id=$1`, emptyID))
	require.Equal(t, "todo", category)
}

// Recategorise vs enable race: the category guard reads the policy while the
// re-categorisation is still uncommitted. An enable that lands in that window
// would leave a populated done-category status under an enforcing policy, so the
// enable must wait for the open change.
func TestEnforce_StatusRecategoriseRacesWithPolicyEnable(t *testing.T) {
	f := newEnforcedFixture(t)
	f.setEnforce(false)
	emptyID := uuid.New()
	f.exec(`INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1,$2,'Late','late','todo'::task_status_category,6)`, emptyID, f.projectID)
	f.exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, f.taskID, emptyID)

	tx := f.db.MustBegin()
	defer func() { _ = tx.Rollback() }()
	_, err := tx.Exec(`UPDATE task_statuses SET category='done'::task_status_category WHERE id=$1`, emptyID)
	require.NoError(t, err) // policy is off: allowed, and the guard has read that

	enabled := make(chan struct{}, 1)
	go func() {
		f.setEnforce(true)
		enabled <- struct{}{}
	}()
	select {
	case <-enabled:
		t.Fatal("policy enable returned while the re-categorisation was still open")
	case <-time.After(500 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	select {
	case <-enabled:
	case <-time.After(10 * time.Second):
		t.Fatal("policy enable never finished after the re-categorisation committed")
	}
	var category string
	require.NoError(t, f.db.Get(&category, `SELECT category::text FROM task_statuses WHERE id=$1`, emptyID))
	require.Equal(t, "done", category)
}

// Revoke race: a revocation that is still open when the product-done commit
// starts must be seen by that commit. Without the intent row lock the commit
// reads no revocation, passes, and a revoked intent authorises done.
func TestEnforce_RevokeRacesWithCommit(t *testing.T) {
	f := newEnforcedFixture(t)
	it := f.accept()

	tx := f.db.MustBegin()
	defer func() { _ = tx.Rollback() }()
	_, err := tx.Exec(`SELECT 1 FROM task_acceptance_intents WHERE id=$1 FOR UPDATE`, it.ID)
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT INTO task_acceptance_intent_revocations(intent_id, revoked_by, reason) VALUES($1,$2,'racing revoke')`, it.ID, f.acceptor)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, _, cerr := f.repo.CommitProductDone(context.Background(), f.commitInput("revoke-race", it))
		done <- cerr
	}()
	select {
	case cerr := <-done:
		t.Fatalf("the commit returned while a revocation of its intent was still open: %v", cerr)
	case <-time.After(500 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())

	select {
	case cerr := <-done:
		require.Error(t, cerr)
		require.Contains(t, cerr.Error(), "acceptance_intent_revoked")
	case <-time.After(10 * time.Second):
		t.Fatal("the commit never finished after the revocation committed")
	}
	status, _ := f.state()
	require.Equal(t, f.todoID, status)
	require.Zero(t, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
}

// The other order: a commit that already consumed the intent holds it until it
// ends, so a revocation waits for it instead of slipping in underneath.
func TestEnforce_CommitHoldsIntentAgainstRevoke(t *testing.T) {
	f := newEnforcedFixture(t)
	it := f.accept()

	tx := f.db.MustBegin()
	defer func() { _ = tx.Rollback() }()
	_, err := tx.Exec(`SELECT 1 FROM task_acceptance_intents WHERE id=$1 FOR SHARE`, it.ID) // what a commit in flight holds
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		done <- f.repo.RevokeAcceptanceIntent(context.Background(), f.taskID, it.ID, f.acceptor, "late revoke")
	}()
	select {
	case rerr := <-done:
		t.Fatalf("the revocation returned while a commit held the intent: %v", rerr)
	case <-time.After(500 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	select {
	case rerr := <-done:
		require.NoError(t, rerr)
	case <-time.After(10 * time.Second):
		t.Fatal("the revocation never finished after the commit released the intent")
	}
	require.Equal(t, 1, f.count(`SELECT count(*) FROM task_acceptance_intent_revocations WHERE intent_id=$1`, it.ID))
}

// assertNoCommit: a refused request leaves no receipt, no state change, no event.
func (f *receiptFixture) assertNoCommit(before int64) {
	f.t.Helper()
	status, v := f.state()
	require.Equal(f.t, f.todoID, status, "state must not change")
	require.Equal(f.t, before, v, "version must not advance")
	require.Zero(f.t, f.count(`SELECT count(*) FROM task_transition_receipts WHERE task_id=$1`, f.taskID))
	require.Zero(f.t, f.count(`SELECT count(*) FROM task_event_outbox WHERE task_id=$1 AND action='task.moved'`, f.taskID))
}

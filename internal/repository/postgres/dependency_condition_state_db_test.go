package postgres_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// One edge per (condition, blocker state). Every SQL reader of the verdict must
// agree with the Go predicate domain.TaskDependency.Released and with the
// expected column below: the stored edge (condition_satisfied), the graph
// (unfinished_blockers), the card counter (open_dependency_count) and the
// reaper's HasOpenBlockedDependents.
func TestDependencyConditionState_SQLReadersAgreeWithGoPredicate(t *testing.T) {
	type blockerCase struct {
		name     string
		category string
		shipped  bool
		deployed string // "" none, else the metadata.deployed JSON stored on a link
		linkType string // link_type of that link; "" means pr
		sibling  *siblingLink
		// held: a sibling pr link is still on the way to prod, so the deploy
		// above does not release a `deployed` edge (Go HasDeployment false).
		held bool
	}
	const deployedMeta = `{"deployed":{"sha":"0123456789abcdef0123456789abcdef01234567","environment":"prod","at":"2026-10-25T10:00:00Z"}}`
	blockers := []blockerCase{
		{name: "open", category: "in_progress"},
		{name: "done", category: "done"},
		{name: "cancelled", category: "cancelled"},
		{name: "open+shipped", category: "in_progress", shipped: true},
		{name: "done+shipped", category: "done", shipped: true},
		{name: "open+deployed", category: "in_progress", deployed: deployedMeta},
		{name: "done+deployed", category: "done", deployed: deployedMeta},
		{name: "cancelled+deployed", category: "cancelled", deployed: deployedMeta},
		// only the canonical prod environment releases a `deployed` edge by blocker state
		{name: "open+deployed to staging", category: "in_progress", deployed: `{"deployed":{"sha":"0123456789abcdef0123456789abcdef01234567","environment":"staging","at":"2026-10-25T10:00:00Z"}}`},
		{name: "open+deployed to legacy production", category: "in_progress", deployed: `{"deployed":{"sha":"0123456789abcdef0123456789abcdef01234567","environment":"production","at":"2026-10-25T10:00:00Z"}}`},
		{name: "open+deployed without environment", category: "in_progress", deployed: `{"deployed":{"sha":"0123456789abcdef0123456789abcdef01234567","at":"2026-10-25T10:00:00Z"}}`},
		{name: "deploy sha without at", category: "in_progress", deployed: `{"deployed":{"sha":"0123456789abcdef0123456789abcdef01234567","environment":"production"}}`},
		// a deploy record on a non-pr link is not a delivery of the card's change
		{name: "open+deployed on commit link", category: "in_progress", deployed: deployedMeta, linkType: "commit"},
		// several pr links: one deployed link releases only when no other pr
		// link of the card is still open or merged without a prod deploy
		{name: "open+deployed+open sibling", category: "in_progress", deployed: deployedMeta, sibling: &siblingLink{status: "open", meta: `{}`}, held: true},
		{name: "open+deployed+sibling without status", category: "in_progress", deployed: deployedMeta, sibling: &siblingLink{meta: `{}`}, held: true},
		{name: "open+deployed+merged undeployed sibling", category: "in_progress", deployed: deployedMeta, sibling: &siblingLink{status: "merged", meta: `{"merged_sha":"1111111111111111111111111111111111111111"}`}, held: true},
		{name: "open+deployed+sibling on staging", category: "in_progress", deployed: deployedMeta, sibling: &siblingLink{status: "merged", meta: stagingSiblingMeta}, held: true},
		{name: "done+deployed+open sibling", category: "done", deployed: deployedMeta, sibling: &siblingLink{status: "open", meta: `{}`}, held: true},
		{name: "open+shipped+deployed+open sibling", category: "in_progress", shipped: true, deployed: deployedMeta, sibling: &siblingLink{status: "open", meta: `{}`}, held: true},
		{name: "open+deployed+deployed sibling", category: "in_progress", deployed: deployedMeta, sibling: &siblingLink{status: "merged", meta: deployedMeta}},
		{name: "open+deployed+closed sibling", category: "in_progress", deployed: deployedMeta, sibling: &siblingLink{status: "closed", meta: `{}`}},
		{name: "open+deployed+open review-subject sibling", category: "in_progress", deployed: deployedMeta, sibling: &siblingLink{status: "open", meta: `{"role":"review"}`}},
		{name: "open+deployed+open relates_to sibling", category: "in_progress", deployed: deployedMeta, sibling: &siblingLink{status: "open", meta: `{"relation":"relates_to"}`}},
		// a review-subject link is not the card's work: its deploy releases nothing
		{name: "open+deployed on review-subject link only", category: "in_progress", deployed: `{"role":"review","deployed":{"sha":"0123456789abcdef0123456789abcdef01234567","environment":"prod","at":"2026-10-25T10:00:00Z"}}`},
	}
	conditions := []domain.DependencyCondition{
		domain.DependencyConditionTaskDone, domain.DependencyConditionAccessReady,
		domain.DependencyConditionContractReady, domain.DependencyConditionArtifactPublished,
		domain.DependencyConditionShipped, domain.DependencyConditionDeployed, domain.DependencyConditionAccepted,
	}
	// expected[condition] lists the blocker cases that release the edge.
	expected := map[domain.DependencyCondition]map[string]bool{
		domain.DependencyConditionTaskDone:          {"done": true, "cancelled": true, "done+shipped": true, "done+deployed": true, "cancelled+deployed": true, "done+deployed+open sibling": true},
		domain.DependencyConditionAccessReady:       {"done": true, "cancelled": true, "done+shipped": true, "done+deployed": true, "cancelled+deployed": true, "done+deployed+open sibling": true},
		domain.DependencyConditionContractReady:     {"done": true, "cancelled": true, "done+shipped": true, "done+deployed": true, "cancelled+deployed": true, "done+deployed+open sibling": true},
		domain.DependencyConditionArtifactPublished: {"done": true, "cancelled": true, "done+shipped": true, "done+deployed": true, "cancelled+deployed": true, "done+deployed+open sibling": true},
		domain.DependencyConditionShipped:           {"done": true, "open+shipped": true, "done+shipped": true, "done+deployed": true, "done+deployed+open sibling": true, "open+shipped+deployed+open sibling": true},
		domain.DependencyConditionDeployed: {"open+shipped": true, "done+shipped": true, "open+deployed": true, "done+deployed": true, "cancelled+deployed": true,
			// the terminal is_shipped flag still releases on its own
			"open+shipped+deployed+open sibling": true,
			"open+deployed+deployed sibling":     true, "open+deployed+closed sibling": true,
			"open+deployed+open review-subject sibling": true, "open+deployed+open relates_to sibling": true},
		domain.DependencyConditionAccepted: {"done": true, "done+shipped": true, "done+deployed": true, "done+deployed+open sibling": true},
	}

	f := newGraphDBFixture(t, 1)
	ctx := context.Background()
	repo := postgres.NewTaskDependencyRepo(f.db)
	taskRepo := postgres.NewTaskRepo(f.db)
	statusID := map[string]uuid.UUID{"done": f.done}
	for _, cat := range []string{"in_progress", "cancelled"} {
		id := uuid.New()
		f.exec(t, `INSERT INTO task_statuses(id,project_id,name,slug,color,position,category,is_default) VALUES($1,$2,$3::text,$3::text,'#123456',9,$3::task_status_category,false)`, id, f.project, cat)
		statusID[cat] = id
	}

	n := 0
	mkTask := func(status uuid.UUID, shipped bool) uuid.UUID {
		n++
		id := uuid.New()
		f.exec(t, `INSERT INTO tasks(id,project_id,status_id,title,task_number,created_by,is_shipped) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, f.project, status, fmt.Sprintf("t%d", n), 1000+n, f.user, shipped)
		return id
	}

	type probe struct {
		cond     domain.DependencyCondition
		blocker  blockerCase
		dep, blk uuid.UUID
	}
	var probes []probe
	for _, cond := range conditions {
		for _, b := range blockers {
			blk := mkTask(statusID[b.category], b.shipped)
			dep := mkTask(f.todo, false)
			if b.deployed != "" {
				lt := b.linkType
				if lt == "" {
					lt = "pr"
				}
				f.exec(t, `INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,metadata) VALUES($1,$2,'gitlab',$4,'1','https://git.example/mr/1',$3::jsonb)`, uuid.New(), blk, b.deployed, lt)
			}
			if b.sibling != nil {
				f.exec(t, `INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,status,metadata) VALUES($1,$2,'gitlab','pr','2','https://git.example/mr/2',NULLIF($3,''),$4::jsonb)`, uuid.New(), blk, b.sibling.status, b.sibling.meta)
			}
			f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition) VALUES($1,$2,$3,'blocks',$4)`, uuid.New(), dep, blk, string(cond))
			probes = append(probes, probe{cond: cond, blocker: b, dep: dep, blk: blk})
		}
	}

	snapshot := f.read(t, uuid.Nil)
	for _, p := range probes {
		p := p
		t.Run(fmt.Sprintf("%s/%s", p.cond, p.blocker.name), func(t *testing.T) {
			want := expected[p.cond][p.blocker.name]
			edges, err := repo.ListByTask(ctx, p.dep)
			require.NoError(t, err)
			require.Len(t, edges, 1)
			assert.Equal(t, want, edges[0].ConditionSatisfied, "stored edge verdict")

			goState := domain.BlockerState{Category: domain.StatusCategory(p.blocker.category), IsShipped: p.blocker.shipped, HasDeployment: p.blocker.deployed == deployedMeta && p.blocker.linkType == "" && !p.blocker.held}
			assert.Equal(t, want, edges[0].Released(goState), "Go predicate")

			wantOpen := 1
			if want {
				wantOpen = 0
			}
			assert.Equal(t, wantOpen, graphFixtureNode(t, snapshot, p.dep).UnfinishedBlockers, "graph unfinished_blockers")
			got, err := taskRepo.GetByID(ctx, p.dep)
			require.NoError(t, err)
			assert.Equal(t, wantOpen, got.OpenDependencyCount, "open_dependency_count")

			deployedSet, err := repo.DeployedTasks(ctx, []uuid.UUID{p.blk})
			require.NoError(t, err)
			assert.Equal(t, p.blocker.deployed == deployedMeta && p.blocker.linkType == "" && !p.blocker.held, deployedSet[p.blk], "DeployedTasks")
		})
	}

	// A relates_to edge never holds anything: verdict true whatever the blocker.
	blk, dep := mkTask(statusID["in_progress"], false), mkTask(f.todo, false)
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES($1,$2,$3,'relates_to')`, uuid.New(), dep, blk)
	edges, err := repo.ListByTask(ctx, dep)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.True(t, edges[0].ConditionSatisfied, "non-blocks edge is always satisfied")
}

// siblingLink is a second pr link on a parity-test blocker; status "" is
// stored as NULL (a link with no recorded status).
type siblingLink struct{ status, meta string }

const stagingSiblingMeta = `{"merged_sha":"1111111111111111111111111111111111111111","deployed":{"sha":"1111111111111111111111111111111111111111","environment":"staging","at":"2026-10-25T10:00:00Z"}}`

// A card delivered by two MRs. The mcp MR is merged and deployed to
// prod while the server MR is still open: a `deployed` edge on that card must
// keep holding until the second MR is merged and deployed too.
func TestDependencyConditionState_DeployedHoldsWhileSiblingMROpen(t *testing.T) {
	f := newGraphDBFixture(t, 4)
	ids := f.ids(t)
	blocker, waiting, single, waitingSingle := ids[0], ids[1], ids[2], ids[3]
	repo := postgres.NewTaskDependencyRepo(f.db)
	taskRepo := postgres.NewTaskRepo(f.db)
	ctx := context.Background()
	const deployed = `{"merged_sha":"74d72a45aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","deployed":{"sha":"74d72a45aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","environment":"prod","at":"2026-11-13T07:30:57Z"}}`

	f.exec(t, `INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,status,metadata) VALUES($1,$2,'gitlab','pr','220','https://git.example/evc-mesh-mcp/mr/220','merged',$3::jsonb)`, uuid.New(), blocker, deployed)
	server := uuid.New()
	f.exec(t, `INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,status,metadata) VALUES($1,$2,'gitlab','pr','1860','https://git.example/evc-mesh/mr/1860','open','{}'::jsonb)`, server, blocker)
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition) VALUES($1,$2,$3,'blocks','deployed')`, uuid.New(), waiting, blocker)

	// control: a card with a single deployed MR releases, as before
	f.exec(t, `INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,status,metadata) VALUES($1,$2,'gitlab','pr','7','https://git.example/mr/7','merged',$3::jsonb)`, uuid.New(), single, deployed)
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition) VALUES($1,$2,$3,'blocks','deployed')`, uuid.New(), waitingSingle, single)

	check := func(dep, blk uuid.UUID, want bool, msg string) {
		t.Helper()
		edges, err := repo.ListByTask(ctx, dep)
		require.NoError(t, err)
		require.Len(t, edges, 1)
		assert.Equal(t, want, edges[0].ConditionSatisfied, msg+": stored edge verdict")
		set, err := repo.DeployedTasks(ctx, []uuid.UUID{blk})
		require.NoError(t, err)
		assert.Equal(t, want, set[blk], msg+": DeployedTasks")
		assert.Equal(t, want, edges[0].Released(domain.BlockerState{Category: domain.StatusCategoryTodo, HasDeployment: set[blk]}), msg+": Go predicate")
		got, err := taskRepo.GetByID(ctx, dep)
		require.NoError(t, err)
		wantOpen := 1
		if want {
			wantOpen = 0
		}
		assert.Equal(t, wantOpen, got.OpenDependencyCount, msg+": open_dependency_count")
	}

	check(waitingSingle, single, true, "single deployed MR")
	check(waiting, blocker, false, "one MR deployed, the other open")

	f.exec(t, `UPDATE vcs_links SET status='merged', metadata='{"merged_sha":"2222222222222222222222222222222222222222"}'::jsonb WHERE id=$1`, server)
	check(waiting, blocker, false, "second MR merged, not deployed")

	f.exec(t, `UPDATE vcs_links SET metadata='{"merged_sha":"2222222222222222222222222222222222222222","deployed":{"sha":"2222222222222222222222222222222222222222","environment":"prod","at":"2026-11-13T09:00:00Z"}}'::jsonb WHERE id=$1`, server)
	check(waiting, blocker, true, "both MRs merged and deployed")
}

// An 'accepted' edge on a cancelled blocker keeps holding (cancelled is not
// acceptance), and the reaper still sees a dependent waiting on its open blocker.
func TestDependencyConditionState_ReaperAndEvidenceStillRelease(t *testing.T) {
	f := newGraphDBFixture(t, 3)
	ids := f.ids(t)
	blocker, waiting, evidenced := ids[0], ids[1], ids[2]
	repo := postgres.NewTaskDependencyRepo(f.db)
	ctx := context.Background()
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition) VALUES($1,$2,$3,'blocks','accepted')`, uuid.New(), waiting, blocker)
	open, err := repo.HasOpenBlockedDependents(ctx, blocker)
	require.NoError(t, err)
	assert.True(t, open, "an accepted edge on an open blocker holds its dependent")

	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition,evidence_url,condition_met_at,evidence_status) VALUES($1,$2,$3,'blocks','deployed','https://mesh.example/e',now(),'trusted')`, uuid.New(), evidenced, blocker)
	edges, err := repo.ListByTask(ctx, evidenced)
	require.NoError(t, err)
	assert.True(t, edges[0].ConditionSatisfied, "evidence still releases a typed edge whatever the blocker")

	f.exec(t, `DELETE FROM task_dependencies WHERE task_id=$1`, waiting)
	open, err = repo.HasOpenBlockedDependents(ctx, blocker)
	require.NoError(t, err)
	assert.False(t, open, "only the evidence-released edge remains")
}

// Trusted evidence is revoked when the receipt it was bound to stops holding:
// the pr link moves to another HEAD, loses its SHIP, or its acceptance turns
// negative while the HEAD stays the same.
func TestTaskDependencyDB_TrustedEvidenceRevokedOnReceiptDrift(t *testing.T) {
	cases := []struct {
		name, condition, before, after string
	}{
		{"accepted -> failed verdict", "accepted", `{"head_sha":"h1","acceptance":{"result":"passed","sha":"h1"}}`, `{"head_sha":"h1","acceptance":{"result":"failed","sha":"h1"}}`},
		{"accepted -> other sha", "accepted", `{"head_sha":"h1","acceptance":{"result":"passed","sha":"h1"}}`, `{"head_sha":"h1","acceptance":{"result":"passed","sha":"h0"}}`},
		{"shipped -> SHIP removed", "shipped", `{"head_sha":"h1","ship_sha":"h1"}`, `{"head_sha":"h1"}`},
		{"shipped -> new HEAD", "shipped", `{"head_sha":"h1","ship_sha":"h1"}`, `{"head_sha":"h2","ship_sha":"h1"}`},
		{"deployed -> other environment", "deployed", `{"head_sha":"h1","deployed":{"sha":"h1","environment":"prod"}}`, `{"head_sha":"h1","deployed":{"sha":"h1","environment":"staging"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGraphDBFixture(t, 2)
			ids := f.ids(t)
			blocker, waiting := ids[0], ids[1]
			link, edge := uuid.New(), uuid.New()
			f.exec(t, `INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,metadata) VALUES($1,$2,'gitlab','pr','1','https://git.example/mr/1',$3::jsonb)`, link, blocker, tc.before)
			env := "prod"
			f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition,evidence_url,condition_met_at,evidence_status,evidence_head_sha,evidence_environment,evidence_link_id)
				VALUES($1,$2,$3,'blocks',$4,'https://mesh.example/e',now(),'trusted','h1',$5,$6)`, edge, waiting, blocker, tc.condition, env, link)
			repo := postgres.NewTaskDependencyRepo(f.db)
			got, err := repo.GetByID(context.Background(), edge)
			require.NoError(t, err)
			require.True(t, got.ConditionSatisfied, "trusted evidence releases before the drift")

			f.exec(t, `UPDATE vcs_links SET metadata=$2::jsonb WHERE id=$1`, link, tc.after)
			got, err = repo.GetByID(context.Background(), edge)
			require.NoError(t, err)
			assert.Equal(t, domain.EvidenceRevoked, got.EvidenceStatus)
			assert.False(t, got.ConditionSatisfied, "revoked evidence no longer releases")
		})
	}
}

// Evidence verified against a link that moved on before the edge write must not
// end up trusted: the write re-checks the receipt under a lock on the link row.
func TestTaskDependencyDB_EvidenceStoredAfterLinkMovedIsRevoked(t *testing.T) {
	f := newGraphDBFixture(t, 2)
	ids := f.ids(t)
	blocker, waiting := ids[0], ids[1]
	link, edge := uuid.New(), uuid.New()
	// The link already moved to h2; the verifier saw h1.
	f.exec(t, `INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,metadata) VALUES($1,$2,'gitlab','pr','1','https://git.example/mr/1',$3::jsonb)`,
		link, blocker, `{"head_sha":"h2","ship_sha":"h1"}`)
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition) VALUES($1,$2,$3,'blocks','shipped')`, edge, waiting, blocker)
	repo := postgres.NewTaskDependencyRepo(f.db)
	cur, err := repo.GetByID(context.Background(), edge)
	require.NoError(t, err)
	gen := cur.Generation
	now := time.Now()
	got, err := repo.UpdateCondition(context.Background(), edge, domain.DependencyConditionUpdate{
		Condition: domain.DependencyCondition("shipped"), Criterion: cur.EffectiveCriterion(),
		ExpectedGeneration: &gen,
		Evidence: &domain.DependencyEvidence{URL: "https://git.example/mr/1", Status: domain.EvidenceTrusted,
			HeadSHA: "h1", LinkID: &link, MetAt: &now},
	})
	require.NoError(t, err)
	assert.Equal(t, domain.EvidenceRevoked, got.EvidenceStatus)
	assert.False(t, got.ConditionSatisfied, "stale receipt must not release the edge")
}

// Deleting the pr link a trusted receipt is bound to revokes the evidence.
func TestTaskDependencyDB_TrustedEvidenceRevokedWhenLinkDeleted(t *testing.T) {
	f := newGraphDBFixture(t, 2)
	ids := f.ids(t)
	blocker, waiting := ids[0], ids[1]
	link, edge := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,metadata) VALUES($1,$2,'gitlab','pr','1','https://git.example/mr/1',$3::jsonb)`,
		link, blocker, `{"head_sha":"h1","ship_sha":"h1"}`)
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition,evidence_url,condition_met_at,evidence_status,evidence_head_sha,evidence_link_id)
		VALUES($1,$2,$3,'blocks','shipped','https://mesh.example/e',now(),'trusted','h1',$4)`, edge, waiting, blocker, link)
	repo := postgres.NewTaskDependencyRepo(f.db)
	got, err := repo.GetByID(context.Background(), edge)
	require.NoError(t, err)
	require.True(t, got.ConditionSatisfied)

	f.exec(t, `DELETE FROM vcs_links WHERE id=$1`, link)
	got, err = repo.GetByID(context.Background(), edge)
	require.NoError(t, err)
	assert.Equal(t, domain.EvidenceRevoked, got.EvidenceStatus)
	assert.False(t, got.ConditionSatisfied, "evidence without its receipt no longer releases")
}

// Migration 20261113100 rewrites deploys stored as "production" to
// "prod": the held `deployed` edge is released, a staging deploy still is not,
// and a trusted receipt bound to the rewritten link survives the drift trigger.
// The Up section runs in a transaction that is rolled back, so the rows other
// tests in the shared database insert raw are never touched.
func TestDeployedEnvProdMigration_ReleasesProductionDeploys(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/20261113100_vcs_deployed_env_prod.sql")
	require.NoError(t, err)
	up := strings.SplitN(string(raw), "-- +goose Up", 2)
	require.Len(t, up, 2, "migration has no goose Up section")
	upSQL := strings.SplitN(up[1], "-- +goose Down", 2)[0]

	f := newGraphDBFixture(t, 6)
	ids := f.ids(t)
	prodBlk, prodDep, stgBlk, stgDep, evBlk, evDep := ids[0], ids[1], ids[2], ids[3], ids[4], ids[5]
	const s = "0123456789abcdef0123456789abcdef01234567"
	meta := func(env string) string {
		return `{"head_sha":"` + s + `","deployed":{"sha":"` + s + `","environment":"` + env + `","at":"2026-11-10T10:00:00Z"}}`
	}
	prodLink, evLink := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,metadata) VALUES($1,$2,'gitlab','pr','1','https://git.example/mr/1',$3::jsonb)`, prodLink, prodBlk, meta(" Production"))
	f.exec(t, `INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,metadata) VALUES($1,$2,'gitlab','pr','2','https://git.example/mr/2',$3::jsonb)`, uuid.New(), stgBlk, meta("staging"))
	f.exec(t, `INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,metadata) VALUES($1,$2,'gitlab','pr','3','https://git.example/mr/3',$3::jsonb)`, evLink, evBlk, meta("production"))
	prodEdge, stgEdge, evEdge := uuid.New(), uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition) VALUES($1,$2,$3,'blocks','deployed')`, prodEdge, prodDep, prodBlk)
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition) VALUES($1,$2,$3,'blocks','deployed')`, stgEdge, stgDep, stgBlk)
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition,evidence_url,condition_met_at,evidence_status,evidence_head_sha,evidence_environment,evidence_link_id)
		VALUES($1,$2,$3,'blocks','deployed','https://mesh.example/e',now(),'trusted',$4,'production',$5)`, evEdge, evDep, evBlk, s, evLink)

	tx, err := f.db.Beginx()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	released := func(edge uuid.UUID) bool {
		t.Helper()
		var ok bool
		require.NoError(t, tx.Get(&ok, `SELECT `+domain.DependencyReleasedSQL("d", "bt", "bts.category")+`
			FROM task_dependencies d JOIN tasks bt ON bt.id=d.depends_on_task_id LEFT JOIN task_statuses bts ON bts.id=bt.status_id WHERE d.id=$1`, edge))
		return ok
	}
	require.False(t, released(prodEdge), "red: a deploy stored as production holds the edge before the migration")
	require.False(t, released(stgEdge))

	_, err = tx.Exec(upSQL)
	require.NoError(t, err)

	assert.True(t, released(prodEdge), "production deploy releases the deployed edge after the migration")
	assert.False(t, released(stgEdge), "staging still does not release")
	var env string
	require.NoError(t, tx.Get(&env, `SELECT metadata->'deployed'->>'environment' FROM vcs_links WHERE id=$1`, prodLink))
	assert.Equal(t, domain.VCSEnvProd, env)
	var evStatus, evEnv string
	require.NoError(t, tx.QueryRow(`SELECT evidence_status, evidence_environment FROM task_dependencies WHERE id=$1`, evEdge).Scan(&evStatus, &evEnv))
	assert.Equal(t, string(domain.EvidenceTrusted), evStatus, "bound receipt is not revoked by the rewrite")
	assert.Equal(t, domain.VCSEnvProd, evEnv)
	var left int
	require.NoError(t, tx.Get(&left, `SELECT count(*) FROM vcs_links WHERE lower(btrim(metadata->'deployed'->>'environment'))='production'`))
	assert.Zero(t, left)
}

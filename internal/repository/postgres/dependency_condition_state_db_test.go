package postgres_test

import (
	"context"
	"fmt"
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
	}
	conditions := []domain.DependencyCondition{
		domain.DependencyConditionTaskDone, domain.DependencyConditionAccessReady,
		domain.DependencyConditionContractReady, domain.DependencyConditionArtifactPublished,
		domain.DependencyConditionShipped, domain.DependencyConditionDeployed, domain.DependencyConditionAccepted,
	}
	// expected[condition] lists the blocker cases that release the edge.
	expected := map[domain.DependencyCondition]map[string]bool{
		domain.DependencyConditionTaskDone:          {"done": true, "cancelled": true, "done+shipped": true, "done+deployed": true, "cancelled+deployed": true},
		domain.DependencyConditionAccessReady:       {"done": true, "cancelled": true, "done+shipped": true, "done+deployed": true, "cancelled+deployed": true},
		domain.DependencyConditionContractReady:     {"done": true, "cancelled": true, "done+shipped": true, "done+deployed": true, "cancelled+deployed": true},
		domain.DependencyConditionArtifactPublished: {"done": true, "cancelled": true, "done+shipped": true, "done+deployed": true, "cancelled+deployed": true},
		domain.DependencyConditionShipped:           {"done": true, "open+shipped": true, "done+shipped": true, "done+deployed": true},
		domain.DependencyConditionDeployed:          {"open+shipped": true, "done+shipped": true, "open+deployed": true, "done+deployed": true, "cancelled+deployed": true},
		domain.DependencyConditionAccepted:          {"done": true, "done+shipped": true, "done+deployed": true},
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

			goState := domain.BlockerState{Category: domain.StatusCategory(p.blocker.category), IsShipped: p.blocker.shipped, HasDeployment: p.blocker.deployed == deployedMeta && p.blocker.linkType == ""}
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
			assert.Equal(t, p.blocker.deployed == deployedMeta && p.blocker.linkType == "", deployedSet[p.blk], "DeployedTasks")
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

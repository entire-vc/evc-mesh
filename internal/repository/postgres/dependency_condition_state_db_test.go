package postgres_test

import (
	"context"
	"fmt"
	"testing"

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
	const deployedMeta = `{"deployed":{"sha":"0123456789abcdef0123456789abcdef01234567","environment":"production","at":"2026-10-25T10:00:00Z"}}`
	blockers := []blockerCase{
		{name: "open", category: "in_progress"},
		{name: "done", category: "done"},
		{name: "cancelled", category: "cancelled"},
		{name: "open+shipped", category: "in_progress", shipped: true},
		{name: "done+shipped", category: "done", shipped: true},
		{name: "open+deployed", category: "in_progress", deployed: deployedMeta},
		{name: "done+deployed", category: "done", deployed: deployedMeta},
		{name: "cancelled+deployed", category: "cancelled", deployed: deployedMeta},
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

	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition,evidence_url,condition_met_at) VALUES($1,$2,$3,'blocks','deployed','https://mesh.example/e',now())`, uuid.New(), evidenced, blocker)
	edges, err := repo.ListByTask(ctx, evidenced)
	require.NoError(t, err)
	assert.True(t, edges[0].ConditionSatisfied, "evidence still releases a typed edge whatever the blocker")

	f.exec(t, `DELETE FROM task_dependencies WHERE task_id=$1`, waiting)
	open, err = repo.HasOpenBlockedDependents(ctx, blocker)
	require.NoError(t, err)
	assert.False(t, open, "only the evidence-released edge remains")
}

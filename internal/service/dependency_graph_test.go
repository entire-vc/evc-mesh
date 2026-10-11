package service

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func graphFixture(n int) (*domain.GraphSnapshot, uuid.UUID) {
	p := uuid.New()
	s := &domain.GraphSnapshot{Revision: "fixture"}
	for i := 0; i < n; i++ {
		s.Nodes = append(s.Nodes, domain.GraphNode{ID: uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1)), ProjectID: &p, Title: fmt.Sprintf("Task %d", i), Category: domain.StatusCategoryTodo, Availability: "available"})
	}
	return s, p
}
func dependencyFixtureEdge(s *domain.GraphSnapshot, a, b int, kind domain.DependencyType) {
	d := domain.TaskDependency{ID: uuid.New(), DependsOnTaskID: s.Nodes[a].ID, TaskID: s.Nodes[b].ID, DependencyType: kind}
	s.Dependencies = append(s.Dependencies, d)
	s.Topology = append(s.Topology, d)
}
func TestProjectGraphDirectionDiamondAndHierarchy(t *testing.T) {
	s, p := graphFixture(6)
	dependencyFixtureEdge(s, 0, 1, domain.DependencyTypeBlocks)
	dependencyFixtureEdge(s, 0, 2, domain.DependencyTypeBlocks)
	dependencyFixtureEdge(s, 1, 3, domain.DependencyTypeBlocks)
	dependencyFixtureEdge(s, 2, 3, domain.DependencyTypeBlocks)
	dependencyFixtureEdge(s, 4, 3, domain.DependencyTypeIsChildOf)
	r, err := ProjectGraph(s, p, GraphOptions{Focus: s.Nodes[3].ID})
	require.NoError(t, err)
	require.ElementsMatch(t, []uuid.UUID{s.Nodes[1].ID, s.Nodes[2].ID}, r.Before.IDs)
	require.Empty(t, r.After.IDs)
	require.Equal(t, 1, r.Relations.Total)
	require.Len(t, r.Tasks, 3)
	r, err = ProjectGraph(s, p, GraphOptions{Focus: s.Nodes[0].ID})
	require.NoError(t, err)
	require.Len(t, r.After.IDs, 2)
	require.Len(t, r.Tasks, 3)
	r, err = ProjectGraph(s, p, GraphOptions{Focus: s.Nodes[5].ID})
	require.NoError(t, err)
	require.True(t, r.Complete)
	require.Len(t, r.Tasks, 1)
}
func TestProjectGraphTerminalBridgeAndRestricted(t *testing.T) {
	s, p := graphFixture(4)
	s.Nodes[1].Category = domain.StatusCategoryDone
	s.Nodes[2].Category = domain.StatusCategoryCancelled
	s.Nodes[3] = domain.GraphNode{ID: s.Nodes[3].ID, Availability: "restricted"}
	dependencyFixtureEdge(s, 0, 1, domain.DependencyTypeBlocks)
	dependencyFixtureEdge(s, 1, 2, domain.DependencyTypeBlocks)
	dependencyFixtureEdge(s, 3, 2, domain.DependencyTypeBlocks)
	r, err := ProjectGraph(s, p, GraphOptions{Focus: s.Nodes[0].ID})
	require.NoError(t, err)
	require.Equal(t, 1, r.After.Completed)
	require.Empty(t, r.After.IDs)
	require.False(t, r.Complete)
	r, err = ProjectGraph(s, p, GraphOptions{Focus: s.Nodes[2].ID, Completed: true, Search: "does not match"})
	require.NoError(t, err)
	require.Len(t, r.Before.IDs, 2)
	require.Len(t, r.Dependencies, 2)
	require.Empty(t, r.Tasks[2].Title)
}
func TestProjectGraphCyclesAcrossProjectsAndSelfLoop(t *testing.T) {
	s, p := graphFixture(4)
	other := uuid.New()
	s.Nodes[1].ProjectID = &other
	dependencyFixtureEdge(s, 0, 1, domain.DependencyTypeBlocks)
	dependencyFixtureEdge(s, 1, 0, domain.DependencyTypeBlocks)
	dependencyFixtureEdge(s, 2, 2, domain.DependencyTypeBlocks)
	dependencyFixtureEdge(s, 3, 3, domain.DependencyTypeIsChildOf)
	r, err := ProjectGraph(s, p, GraphOptions{Focus: s.Nodes[0].ID})
	require.NoError(t, err)
	require.Equal(t, 2, r.CycleCount)
	require.Len(t, r.CycleIDs, 2)
	require.Len(t, r.Tasks, 2)
	require.Equal(t, 2, r.Total, "a shared before/after neighbor is one unique node")
	r, err = ProjectGraph(s, p, GraphOptions{Focus: s.Nodes[2].ID})
	require.NoError(t, err)
	require.Equal(t, 1, r.Total, "a self-loop never adds another node")
	components := GraphCycleComponents(s.Topology)
	require.Len(t, components[s.Nodes[2].ID], 1)
	require.Empty(t, components[s.Nodes[3].ID])
}
func TestProjectGraphDensePaginationAndBudget(t *testing.T) {
	s, p := graphFixture(1000)
	for i := 0; i < 1000; i++ {
		for k := 1; k <= 3; k++ {
			dependencyFixtureEdge(s, i, (i+k)%1000, domain.DependencyTypeBlocks)
		}
	}
	ids := map[uuid.UUID]bool{}
	for page := 1; page <= 5; page++ {
		r, err := ProjectGraph(s, p, GraphOptions{Page: page, PageSize: 200})
		require.NoError(t, err)
		require.Equal(t, 1000, r.Total)
		require.Equal(t, page < 5, r.HasMore)
		for _, n := range r.Tasks {
			require.False(t, ids[n.ID])
			ids[n.ID] = true
		}
	}
	require.Len(t, ids, 1000)
	// 200 prerequisites and dependents remain discoverable in pages of ten.
	for i := 4; i < 205; i++ {
		dependencyFixtureEdge(s, 0, i, domain.DependencyTypeBlocks)
		dependencyFixtureEdge(s, i, 0, domain.DependencyTypeBlocks)
	}
	r, err := ProjectGraph(s, p, GraphOptions{Focus: s.Nodes[0].ID})
	require.NoError(t, err)
	require.LessOrEqual(t, len(r.Tasks), 21)
	require.True(t, r.Before.HasMore)
	require.True(t, r.After.HasMore)
	r, err = ProjectGraph(s, p, GraphOptions{Focus: s.Nodes[0].ID, BeforePage: 2, AfterPage: 2})
	require.NoError(t, err)
	require.LessOrEqual(t, len(r.Tasks), 21)
	_, err = ProjectGraph(s, p, GraphOptions{Revision: "old"})
	require.Error(t, err)
}
func BenchmarkProjectGraphDenseFocus(b *testing.B) {
	s, p := graphFixture(1000)
	for i := 0; i < 1000; i++ {
		for k := 1; k <= 3; k++ {
			dependencyFixtureEdge(s, i, (i+k)%1000, domain.DependencyTypeBlocks)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ProjectGraph(s, p, GraphOptions{Focus: s.Nodes[i%1000].ID})
	}
}

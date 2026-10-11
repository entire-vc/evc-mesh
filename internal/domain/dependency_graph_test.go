package domain

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Reachability is an independent oracle for SCC membership. Exhausting all
// directed three-node graphs includes self-loops, forks, disconnected
// components and edges into an already completed component.
func TestGraphCycleComponentsAllThreeNodeGraphs(t *testing.T) {
	ids := []uuid.UUID{
		uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		uuid.MustParse("00000000-0000-0000-0000-000000000003"),
	}
	for mask := 0; mask < 1<<9; mask++ {
		t.Run(fmt.Sprintf("edges-%03x", mask), func(t *testing.T) {
			var reachable [3][3]bool
			var edges []TaskDependency
			for from := range ids {
				for to := range ids {
					if mask&(1<<(from*3+to)) == 0 {
						continue
					}
					reachable[from][to] = true
					edges = append(edges, TaskDependency{
						DependsOnTaskID: ids[from], TaskID: ids[to], DependencyType: DependencyTypeBlocks,
					})
				}
			}
			for via := range ids {
				for from := range ids {
					for to := range ids {
						reachable[from][to] = reachable[from][to] || (reachable[from][via] && reachable[via][to])
					}
				}
			}
			want := map[uuid.UUID][]uuid.UUID{}
			for from := range ids {
				if !reachable[from][from] {
					continue
				}
				for to := range ids {
					if reachable[from][to] && reachable[to][from] {
						want[ids[from]] = append(want[ids[from]], ids[to])
					}
				}
			}
			require.Equal(t, want, GraphCycleComponents(edges))
			// Reversing insertion order and duplicating an edge must preserve
			// sorted, unique membership even when map traversal changes.
			for left, right := 0, len(edges)-1; left < right; left, right = left+1, right-1 {
				edges[left], edges[right] = edges[right], edges[left]
			}
			if len(edges) > 0 {
				edges = append(edges, edges[0])
			}
			require.Equal(t, want, GraphCycleComponents(edges))
		})
	}
}

func TestGraphCycleComponentsSeparatesConnectedCycles(t *testing.T) {
	ids := []uuid.UUID{
		uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		uuid.MustParse("00000000-0000-0000-0000-000000000004"),
	}
	var edges []TaskDependency
	for _, pair := range [][2]int{{0, 1}, {1, 0}, {1, 2}, {2, 3}, {3, 2}} {
		edges = append(edges, TaskDependency{
			DependsOnTaskID: ids[pair[0]], TaskID: ids[pair[1]], DependencyType: DependencyTypeBlocks,
		})
	}
	require.Equal(t, map[uuid.UUID][]uuid.UUID{
		ids[0]: ids[:2], ids[1]: ids[:2], ids[2]: ids[2:], ids[3]: ids[2:],
	}, GraphCycleComponents(edges), "one-way bridge must not merge distinct cycle components")
}

func TestGraphCycleComponentsIgnoresNonBlockingRelations(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	for _, relation := range []DependencyType{DependencyTypeRelatesTo, DependencyTypeIsChildOf} {
		t.Run(string(relation), func(t *testing.T) {
			edges := []TaskDependency{
				{DependsOnTaskID: a, TaskID: b, DependencyType: DependencyTypeBlocks},
				{DependsOnTaskID: b, TaskID: a, DependencyType: relation},
				{DependsOnTaskID: a, TaskID: a, DependencyType: relation},
			}
			require.Empty(t, GraphCycleComponents(edges), "non-blocking relations cannot close a cycle")
			edges = append(edges, TaskDependency{DependsOnTaskID: b, TaskID: b, DependencyType: DependencyTypeBlocks})
			require.Equal(t, map[uuid.UUID][]uuid.UUID{b: {b}}, GraphCycleComponents(edges))
		})
	}
}

func TestGraphNodeTerminalUsesStatusCategory(t *testing.T) {
	for _, category := range []StatusCategory{
		StatusCategoryBacklog, StatusCategoryTodo, StatusCategoryInProgress,
		StatusCategoryReview, StatusCategoryDone, StatusCategoryCancelled, "",
	} {
		t.Run(string(category), func(t *testing.T) {
			want := category == StatusCategoryDone || category == StatusCategoryCancelled
			require.Equal(t, want, (GraphNode{Category: category}).Terminal())
		})
	}
}

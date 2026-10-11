package domain

import (
	"sort"

	"github.com/google/uuid"
)

// GraphCycleComponents uses Tarjan in O(nodes+edges). Direction is prerequisite
// -> dependent. Parent/subtask and relates_to never imply blocking or cycles.
func GraphCycleComponents(edges []TaskDependency) map[uuid.UUID][]uuid.UUID {
	adj := map[uuid.UUID][]uuid.UUID{}
	self := map[uuid.UUID]bool{}
	for _, d := range edges {
		if d.DependencyType != DependencyTypeBlocks {
			continue
		}
		adj[d.DependsOnTaskID] = append(adj[d.DependsOnTaskID], d.TaskID)
		if _, ok := adj[d.TaskID]; !ok {
			adj[d.TaskID] = nil
		}
		if d.TaskID == d.DependsOnTaskID {
			self[d.TaskID] = true
		}
	}
	index := map[uuid.UUID]int{}
	low := map[uuid.UUID]int{}
	on := map[uuid.UUID]bool{}
	stack := []uuid.UUID{}
	next := 0
	result := map[uuid.UUID][]uuid.UUID{}
	var visit func(uuid.UUID)
	visit = func(v uuid.UUID) {
		next++
		index[v] = next
		low[v] = next
		stack = append(stack, v)
		on[v] = true
		for _, w := range adj[v] {
			if index[w] == 0 {
				visit(w)
				if low[w] < low[v] {
					low[v] = low[w]
				}
			} else if on[w] && index[w] < low[v] {
				low[v] = index[w]
			}
		}
		if low[v] == index[v] {
			component := []uuid.UUID{}
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w] = false
				component = append(component, w)
				if w == v {
					break
				}
			}
			if len(component) > 1 || self[v] {
				sort.Slice(component, func(i, j int) bool { return component[i].String() < component[j].String() })
				for _, w := range component {
					result[w] = component
				}
			}
		}
	}
	for v := range adj {
		if index[v] == 0 {
			visit(v)
		}
	}
	return result
}

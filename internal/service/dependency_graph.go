package service

import (
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// GraphOptions controls presentation only. It never alters workflow or edges.
type GraphOptions struct {
	Legacy        bool
	Focus         uuid.UUID
	Page          int
	PageSize      int
	BeforePage    int
	AfterPage     int
	RelationsPage int
	Completed     bool
	Search        string
	Revision      string
}

type GraphBranch struct {
	IDs       []uuid.UUID `json:"ids"`
	Total     int         `json:"total_count"`
	Completed int         `json:"completed_count"`
	Unknown   int         `json:"unknown_count"`
	Page      int         `json:"page"`
	HasMore   bool        `json:"has_more"`
}

type GraphResponse struct {
	Mode               string                  `json:"mode"`
	Tasks              []domain.GraphNode      `json:"tasks"`
	Dependencies       []domain.TaskDependency `json:"dependencies"`
	Focus              *uuid.UUID              `json:"focus,omitempty"`
	Before             GraphBranch             `json:"before"`
	After              GraphBranch             `json:"after"`
	Relations          GraphBranch             `json:"relations"`
	Total              int                     `json:"total_count"`
	ScopeTotal         int                     `json:"scope_total_count"`
	Completed          int                     `json:"completed_count"`
	Unknown            int                     `json:"unknown_count"`
	Page               int                     `json:"page"`
	PageSize           int                     `json:"page_size"`
	HasMore            bool                    `json:"has_more"`
	Complete           bool                    `json:"complete"`
	VisibilityComplete bool                    `json:"visibility_complete"`
	Revision           string                  `json:"revision"`
	// Authorized-workspace cycle membership; IDs remain bounded to the payload.
	CycleIDs   []uuid.UUID `json:"cycle_ids"`
	CycleCount int         `json:"cycle_count"`
}

// GraphCycleComponents returns canonical blocking-cycle membership.
func GraphCycleComponents(edges []domain.TaskDependency) map[uuid.UUID][]uuid.UUID {
	return domain.GraphCycleComponents(edges)
}

func graphLess(a, b domain.GraphNode) bool {
	rank := map[domain.Priority]int{domain.PriorityUrgent: 0, domain.PriorityHigh: 1, domain.PriorityMedium: 2, domain.PriorityLow: 3, domain.PriorityNone: 4, "": 5}
	if rank[a.Priority] != rank[b.Priority] {
		return rank[a.Priority] < rank[b.Priority]
	}
	return a.ID.String() < b.ID.String()
}

func graphBranch(nodes []domain.GraphNode, page int, completed bool) (GraphBranch, []domain.GraphNode) {
	sort.Slice(nodes, func(i, j int) bool { return graphLess(nodes[i], nodes[j]) })
	branch := GraphBranch{IDs: []uuid.UUID{}, Total: len(nodes), Page: page}
	eligible := []domain.GraphNode{}
	for _, n := range nodes {
		if n.Availability != "available" {
			branch.Unknown++
		}
		if n.Terminal() {
			branch.Completed++
		}
		if completed || !n.Terminal() {
			eligible = append(eligible, n)
		}
	}
	if page < 1 {
		page = 1
		branch.Page = 1
	}
	start := (page - 1) * 10
	if start > len(eligible) {
		start = len(eligible)
	}
	end := start + 10
	if end > len(eligible) {
		end = len(eligible)
	}
	branch.HasMore = end < len(eligible)
	for _, n := range eligible[start:end] {
		branch.IDs = append(branch.IDs, n.ID)
	}
	return branch, eligible[start:end]
}

// ProjectGraph paginates AFTER counting the complete canonical data, never after
// deleting absent endpoints. Scope filters apply to overview only; a focused
// chain keeps hidden-by-overview-filter blockers and all boundary identities.
func ProjectGraph(snapshot *domain.GraphSnapshot, projectID uuid.UUID, opts GraphOptions) (*GraphResponse, error) {
	if opts.Revision != "" && opts.Revision != snapshot.Revision {
		return nil, apierror.Conflict("dependency graph changed; restart pagination")
	}
	if opts.Page < 1 {
		opts.Page = 1
	}
	if opts.PageSize < 1 {
		opts.PageSize = 50
	}
	if opts.PageSize > 200 {
		return nil, apierror.BadRequest("page_size must be between 1 and 200")
	}
	result := &GraphResponse{Tasks: []domain.GraphNode{}, Dependencies: []domain.TaskDependency{}, CycleIDs: []uuid.UUID{}, Revision: snapshot.Revision, Page: opts.Page, PageSize: opts.PageSize, Before: GraphBranch{IDs: []uuid.UUID{}}, After: GraphBranch{IDs: []uuid.UUID{}}, Relations: GraphBranch{IDs: []uuid.UUID{}}}
	components := GraphCycleComponents(snapshot.Topology)
	nodes := map[uuid.UUID]domain.GraphNode{}
	for _, n := range snapshot.Nodes {
		n.Cycle = n.Availability == "available" && len(components[n.ID]) > 0
		nodes[n.ID] = n
	}
	result.VisibilityComplete = true
	if opts.Legacy {
		result.Mode = "legacy"
		for _, n := range snapshot.Nodes {
			result.Tasks = append(result.Tasks, nodes[n.ID])
			if n.Availability != "available" {
				result.Unknown++
			}
			if n.ProjectID != nil && *n.ProjectID == projectID {
				result.ScopeTotal++
				if n.Terminal() {
					result.Completed++
				}
			}
		}
		result.Dependencies = snapshot.Dependencies
		result.Total = len(result.Tasks)
		result.PageSize = result.Total
		result.VisibilityComplete = result.Unknown == 0
		result.Complete = result.VisibilityComplete
		return result, nil
	}
	if opts.Focus == uuid.Nil {
		result.Mode = "overview"
		eligible := []domain.GraphNode{}
		scopeIDs := map[uuid.UUID]bool{}
		search := strings.ToLower(strings.TrimSpace(opts.Search))
		for _, n := range nodes {
			if n.ProjectID == nil || *n.ProjectID != projectID {
				continue
			}
			if search != "" && !strings.Contains(strings.ToLower(n.Title), search) && !strings.Contains(n.ID.String(), search) {
				continue
			}
			result.ScopeTotal++
			scopeIDs[n.ID] = true
			if n.Terminal() {
				result.Completed++
			}
			if n.UnknownBlockers > 0 {
				result.VisibilityComplete = false
			}
			if !opts.Completed && n.Terminal() {
				continue
			}
			eligible = append(eligible, n)
		}
		unknown := map[uuid.UUID]bool{}
		for _, d := range snapshot.Dependencies {
			if scopeIDs[d.TaskID] && nodes[d.DependsOnTaskID].Availability != "available" {
				unknown[d.DependsOnTaskID] = true
			}
			if scopeIDs[d.DependsOnTaskID] && nodes[d.TaskID].Availability != "available" {
				unknown[d.TaskID] = true
			}
		}
		for id := range scopeIDs {
			if parent := nodes[id].ParentTaskID; parent != nil && nodes[*parent].Availability != "available" {
				unknown[*parent] = true
			}
		}
		result.Unknown = len(unknown)
		result.VisibilityComplete = result.VisibilityComplete && result.Unknown == 0
		sort.Slice(eligible, func(i, j int) bool { return graphLess(eligible[i], eligible[j]) })
		result.Total = len(eligible)
		start := (opts.Page - 1) * opts.PageSize
		if start > len(eligible) {
			start = len(eligible)
		}
		end := start + opts.PageSize
		if end > len(eligible) {
			end = len(eligible)
		}
		result.Tasks = eligible[start:end]
		result.HasMore = end < len(eligible)
		result.Complete = !result.HasMore && result.VisibilityComplete
		return result, nil
	}
	center, ok := nodes[opts.Focus]
	if !ok || center.Availability != "available" {
		return nil, apierror.NotFound("Task")
	}
	result.Focus = &opts.Focus
	result.Mode = "focus"
	result.Tasks = append(result.Tasks, center)
	before := map[uuid.UUID]domain.GraphNode{}
	after := map[uuid.UUID]domain.GraphNode{}
	relations := map[uuid.UUID]domain.GraphNode{}
	node := func(id uuid.UUID) domain.GraphNode {
		if n, exists := nodes[id]; exists {
			return n
		}
		return domain.GraphNode{ID: id, Availability: "missing"}
	}
	for _, d := range snapshot.Dependencies {
		if d.DependencyType == domain.DependencyTypeBlocks {
			if d.TaskID == opts.Focus {
				before[d.DependsOnTaskID] = node(d.DependsOnTaskID)
			}
			if d.DependsOnTaskID == opts.Focus {
				after[d.TaskID] = node(d.TaskID)
			}
		} else {
			if d.TaskID == opts.Focus {
				relations[d.DependsOnTaskID] = node(d.DependsOnTaskID)
			}
			if d.DependsOnTaskID == opts.Focus {
				relations[d.TaskID] = node(d.TaskID)
			}
		}
	}
	// The canonical parent pointer is a hierarchy link, never a blocker.
	if center.ParentTaskID != nil {
		relations[*center.ParentTaskID] = node(*center.ParentTaskID)
	}
	for _, n := range nodes {
		if n.ParentTaskID != nil && *n.ParentTaskID == opts.Focus {
			relations[n.ID] = n
		}
	}
	values := func(m map[uuid.UUID]domain.GraphNode) []domain.GraphNode {
		out := []domain.GraphNode{}
		for _, n := range m {
			out = append(out, n)
		}
		return out
	}
	var bn, an, rn []domain.GraphNode
	result.Before, bn = graphBranch(values(before), opts.BeforePage, opts.Completed)
	result.After, an = graphBranch(values(after), opts.AfterPage, opts.Completed)
	result.Relations, rn = graphBranch(values(relations), opts.RelationsPage, true)
	// Hierarchy/related details are opt-in, so initial focus has <=21 task nodes.
	if opts.RelationsPage < 1 {
		rn = nil
		result.Relations.IDs = []uuid.UUID{}
		result.Relations.HasMore = result.Relations.Total > 0
	}
	included := map[uuid.UUID]bool{center.ID: true}
	for _, list := range [][]domain.GraphNode{bn, an, rn} {
		for _, n := range list {
			if !included[n.ID] {
				result.Tasks = append(result.Tasks, n)
				included[n.ID] = true
			}
		}
	}
	for _, d := range snapshot.Dependencies {
		if (d.TaskID == opts.Focus || d.DependsOnTaskID == opts.Focus) && included[d.TaskID] && included[d.DependsOnTaskID] {
			result.Dependencies = append(result.Dependencies, d)
		}
	}
	all := map[uuid.UUID]bool{center.ID: true}
	for _, branch := range []map[uuid.UUID]domain.GraphNode{before, after, relations} {
		for id := range branch {
			all[id] = true
		}
	}
	result.Total = len(all)
	result.ScopeTotal = result.Total
	for id := range all {
		n := node(id)
		if n.Terminal() {
			result.Completed++
		}
		if n.Availability != "available" {
			result.Unknown++
		}
	}
	result.VisibilityComplete = result.Unknown == 0
	result.HasMore = result.Before.HasMore || result.After.HasMore || result.Relations.HasMore
	// A collapsed terminal group is explicitly partial presentation.
	result.Complete = !result.HasMore && result.VisibilityComplete && (opts.Completed || result.Before.Completed+result.After.Completed == 0)
	component := components[opts.Focus]
	result.CycleCount = len(component)
	for _, id := range component {
		if included[id] && nodes[id].Availability == "available" {
			result.CycleIDs = append(result.CycleIDs, id)
		}
	}
	return result, nil
}

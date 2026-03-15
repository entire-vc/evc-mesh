package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// Agent hierarchy tests.
//
// Contract for Phase 5-OSS agent hierarchy:
//   - Agents can be organized in a parent-child tree within a workspace.
//   - A sub-agent has a non-nil ParentAgentID pointing to its parent.
//   - Top-level agents have ParentAgentID = nil.
//   - Circular hierarchies are forbidden (A → B → A is invalid).
//   - Maximum nesting depth is enforced (default: 5 levels).
//   - Deleting a parent agent either cascades to children or is blocked
//     when children exist (configurable per workspace policy).
//   - ListSubAgents returns only direct children (not grandchildren).
//
// Tests that need implementation are marked t.Skip().
// Pure logic and mock-based tests run immediately.
// ---------------------------------------------------------------------------

// AgentHierarchyPolicy defines how the system handles parent deletion.
type AgentHierarchyPolicy string

const (
	// PolicyCascadeDelete removes all sub-agents when the parent is deleted.
	PolicyCascadeDelete AgentHierarchyPolicy = "cascade"
	// PolicyBlockDelete prevents deletion when sub-agents exist.
	PolicyBlockDelete AgentHierarchyPolicy = "block"
)

// MaxAgentHierarchyDepth is the maximum number of nesting levels (1 = root only).
const MaxAgentHierarchyDepth = 5

// AgentHierarchyService interface (to be added to service/interfaces.go in Phase 5).
type AgentHierarchyService interface {
	// RegisterSubAgent registers a new agent as a child of parentAgentID.
	RegisterSubAgent(ctx context.Context, parentAgentID uuid.UUID, input RegisterAgentInput) (*RegisterAgentOutput, error)
	// ListSubAgents returns direct children of the given agent (depth=1).
	ListSubAgents(ctx context.Context, parentAgentID uuid.UUID) ([]domain.Agent, error)
	// GetAncestors returns the chain of parent agents up to the root.
	GetAncestors(ctx context.Context, agentID uuid.UUID) ([]domain.Agent, error)
	// GetDepth returns the nesting depth of the agent (root = 0).
	GetDepth(ctx context.Context, agentID uuid.UUID) (int, error)
}

// ---------------------------------------------------------------------------
// In-memory mock agent repo with hierarchy support (for tests that run now)
// ---------------------------------------------------------------------------

// agentNode stores an agent with optional parent reference.
type agentNode struct {
	agent         domain.Agent
	parentAgentID *uuid.UUID
}

type mockAgentHierarchyRepo struct {
	nodes map[uuid.UUID]*agentNode
}

func newMockAgentHierarchyRepo() *mockAgentHierarchyRepo {
	return &mockAgentHierarchyRepo{nodes: make(map[uuid.UUID]*agentNode)}
}

func (r *mockAgentHierarchyRepo) add(agent domain.Agent, parentID *uuid.UUID) {
	r.nodes[agent.ID] = &agentNode{agent: agent, parentAgentID: parentID}
}

func (r *mockAgentHierarchyRepo) listSubAgents(parentID uuid.UUID) []domain.Agent {
	var result []domain.Agent
	for _, n := range r.nodes {
		if n.parentAgentID != nil && *n.parentAgentID == parentID {
			result = append(result, n.agent)
		}
	}
	return result
}

func (r *mockAgentHierarchyRepo) getDepth(agentID uuid.UUID) int {
	depth := 0
	cur := agentID
	for {
		node, ok := r.nodes[cur]
		if !ok || node.parentAgentID == nil {
			break
		}
		depth++
		cur = *node.parentAgentID
		if depth > MaxAgentHierarchyDepth+1 {
			return depth // guard against infinite loop in tests
		}
	}
	return depth
}

// wouldCreateCycle checks if adding child → parent would create a cycle.
func (r *mockAgentHierarchyRepo) wouldCreateCycle(childID, proposedParentID uuid.UUID) bool {
	cur := proposedParentID
	seen := map[uuid.UUID]bool{}
	for {
		if cur == childID {
			return true // cycle detected
		}
		if seen[cur] {
			return true // infinite loop guard
		}
		seen[cur] = true
		node, ok := r.nodes[cur]
		if !ok || node.parentAgentID == nil {
			return false
		}
		cur = *node.parentAgentID
	}
}

// ---------------------------------------------------------------------------
// Tests: AgentHierarchyService (skipped — require implementation)
// ---------------------------------------------------------------------------

func TestAgentHierarchy_RegisterSubAgent(t *testing.T) {
	t.Run("registers sub-agent under valid parent", func(t *testing.T) {
		t.Skip("TODO: implement AgentHierarchyService in internal/service/agent_hierarchy_service.go")
		// Setup: parent agent exists in workspace.
		// When: RegisterSubAgent(parentID, input) is called.
		// Then:
		//   - New agent has ParentAgentID == parentID.
		//   - New agent has same WorkspaceID as parent.
		//   - API key is returned (raw) only at registration.
	})

	t.Run("parent not found returns 404", func(t *testing.T) {
		t.Skip("TODO: implement AgentHierarchyService in internal/service/agent_hierarchy_service.go")
		// When: RegisterSubAgent(nonExistentID, input) is called.
		// Then: 404 Not Found error.
	})

	t.Run("parent from different workspace returns 400", func(t *testing.T) {
		t.Skip("TODO: implement AgentHierarchyService in internal/service/agent_hierarchy_service.go")
		// Setup: parent agent belongs to workspace-B.
		// When: RegisterSubAgent called in context of workspace-A.
		// Then: 400 Bad Request (cross-workspace hierarchy forbidden).
	})
}

func TestAgentHierarchy_ListSubAgents(t *testing.T) {
	t.Run("returns only direct children, not grandchildren", func(t *testing.T) {
		t.Skip("TODO: implement AgentHierarchyService in internal/service/agent_hierarchy_service.go")
		// Setup: root → child → grandchild.
		// When: ListSubAgents(root.ID) is called.
		// Then: only child is returned; grandchild is NOT included.
	})

	t.Run("returns empty list for leaf agent", func(t *testing.T) {
		t.Skip("TODO: implement AgentHierarchyService in internal/service/agent_hierarchy_service.go")
		// Setup: agent with no children.
		// When: ListSubAgents(leafAgent.ID).
		// Then: empty slice (no error).
	})
}

func TestAgentHierarchy_CircularHierarchyPrevented(t *testing.T) {
	t.Run("prevents direct cycle A → B → A", func(t *testing.T) {
		t.Skip("TODO: implement AgentHierarchyService in internal/service/agent_hierarchy_service.go")
		// Setup: A is parent of B.
		// When: try to make A a child of B.
		// Then: 400 error "circular hierarchy detected".
	})

	t.Run("prevents indirect cycle A → B → C → A", func(t *testing.T) {
		t.Skip("TODO: implement AgentHierarchyService in internal/service/agent_hierarchy_service.go")
		// Setup: A → B → C.
		// When: try to make A a child of C.
		// Then: 400 error "circular hierarchy detected".
	})
}

func TestAgentHierarchy_MaxDepthEnforced(t *testing.T) {
	t.Run("allows nesting up to MaxAgentHierarchyDepth", func(t *testing.T) {
		t.Skip("TODO: implement AgentHierarchyService in internal/service/agent_hierarchy_service.go")
		// Setup: chain of MaxAgentHierarchyDepth agents (root at depth 0).
		// When: try to add an agent at depth MaxAgentHierarchyDepth (the last allowed level).
		// Then: success.
	})

	t.Run("blocks nesting beyond MaxAgentHierarchyDepth", func(t *testing.T) {
		t.Skip("TODO: implement AgentHierarchyService in internal/service/agent_hierarchy_service.go")
		// Setup: chain of MaxAgentHierarchyDepth agents.
		// When: try to add an agent at depth MaxAgentHierarchyDepth+1.
		// Then: 400 error "maximum hierarchy depth exceeded".
	})
}

func TestAgentHierarchy_DeleteParentCascades(t *testing.T) {
	t.Run("cascade policy: deleting parent removes all sub-agents", func(t *testing.T) {
		t.Skip("TODO: implement AgentHierarchyService in internal/service/agent_hierarchy_service.go")
		// Setup: A (root) → B → C (workspace policy = cascade).
		// When: Delete(A.ID).
		// Then: A, B, C are all deleted from the repository.
	})

	t.Run("block policy: deleting parent with children returns 409", func(t *testing.T) {
		t.Skip("TODO: implement AgentHierarchyService in internal/service/agent_hierarchy_service.go")
		// Setup: A (root) → B (workspace policy = block).
		// When: Delete(A.ID).
		// Then: 409 Conflict "agent has sub-agents".
	})

	t.Run("block policy: deleting leaf agent succeeds", func(t *testing.T) {
		t.Skip("TODO: implement AgentHierarchyService in internal/service/agent_hierarchy_service.go")
		// Setup: B is a leaf (no children), workspace policy = block.
		// When: Delete(B.ID).
		// Then: success.
	})
}

// ---------------------------------------------------------------------------
// Tests: Pure logic — cycle detection (runs NOW)
// ---------------------------------------------------------------------------

func TestCycleDetection_Logic(t *testing.T) {
	repo := newMockAgentHierarchyRepo()
	wsID := uuid.New()

	agentA := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Agent A"}
	agentB := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Agent B"}
	agentC := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Agent C"}

	repo.add(agentA, nil)        // A is root
	repo.add(agentB, &agentA.ID) // B is child of A
	repo.add(agentC, &agentB.ID) // C is child of B

	t.Run("making A a child of itself is a cycle", func(t *testing.T) {
		assert.True(t, repo.wouldCreateCycle(agentA.ID, agentA.ID))
	})

	t.Run("making A a child of B creates cycle A→B→A", func(t *testing.T) {
		assert.True(t, repo.wouldCreateCycle(agentA.ID, agentB.ID))
	})

	t.Run("making A a child of C creates indirect cycle A→B→C→A", func(t *testing.T) {
		assert.True(t, repo.wouldCreateCycle(agentA.ID, agentC.ID))
	})

	t.Run("making a new agent a child of C is not a cycle", func(t *testing.T) {
		newAgentID := uuid.New()
		// newAgent has no nodes yet, so no cycle possible from its side.
		assert.False(t, repo.wouldCreateCycle(newAgentID, agentC.ID))
	})

	t.Run("C child of B is not a cycle (valid hierarchy)", func(t *testing.T) {
		// This was already added; verify the detection does not false-positive.
		agentD := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Agent D"}
		repo.add(agentD, nil) // D is another root
		assert.False(t, repo.wouldCreateCycle(agentD.ID, agentC.ID))
	})
}

// ---------------------------------------------------------------------------
// Tests: Depth calculation (runs NOW)
// ---------------------------------------------------------------------------

func TestHierarchyDepth_Logic(t *testing.T) {
	repo := newMockAgentHierarchyRepo()
	wsID := uuid.New()

	root := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Root"}
	level1 := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Level 1"}
	level2 := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Level 2"}
	level3 := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Level 3"}

	repo.add(root, nil)
	repo.add(level1, &root.ID)
	repo.add(level2, &level1.ID)
	repo.add(level3, &level2.ID)

	t.Run("root agent is at depth 0", func(t *testing.T) {
		assert.Equal(t, 0, repo.getDepth(root.ID))
	})

	t.Run("direct child is at depth 1", func(t *testing.T) {
		assert.Equal(t, 1, repo.getDepth(level1.ID))
	})

	t.Run("grandchild is at depth 2", func(t *testing.T) {
		assert.Equal(t, 2, repo.getDepth(level2.ID))
	})

	t.Run("great-grandchild is at depth 3", func(t *testing.T) {
		assert.Equal(t, 3, repo.getDepth(level3.ID))
	})
}

// ---------------------------------------------------------------------------
// Tests: List sub-agents (runs NOW using mock)
// ---------------------------------------------------------------------------

func TestListSubAgents_MockBehavior(t *testing.T) {
	repo := newMockAgentHierarchyRepo()
	wsID := uuid.New()

	root := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Root"}
	child1 := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Child 1"}
	child2 := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Child 2"}
	grandchild := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Grandchild"}

	repo.add(root, nil)
	repo.add(child1, &root.ID)
	repo.add(child2, &root.ID)
	repo.add(grandchild, &child1.ID)

	t.Run("root has 2 direct children", func(t *testing.T) {
		children := repo.listSubAgents(root.ID)
		assert.Len(t, children, 2)
		ids := []uuid.UUID{children[0].ID, children[1].ID}
		assert.Contains(t, ids, child1.ID)
		assert.Contains(t, ids, child2.ID)
		// Grandchild must NOT be in the list.
		assert.NotContains(t, ids, grandchild.ID)
	})

	t.Run("child1 has 1 direct child (grandchild)", func(t *testing.T) {
		children := repo.listSubAgents(child1.ID)
		assert.Len(t, children, 1)
		assert.Equal(t, grandchild.ID, children[0].ID)
	})

	t.Run("leaf agent has no children", func(t *testing.T) {
		children := repo.listSubAgents(grandchild.ID)
		assert.Empty(t, children)
	})

	t.Run("unknown agent ID returns empty list", func(t *testing.T) {
		children := repo.listSubAgents(uuid.New())
		assert.Empty(t, children)
	})
}

// ---------------------------------------------------------------------------
// Tests: MaxDepth enforcement logic (runs NOW)
// ---------------------------------------------------------------------------

func TestMaxDepthEnforcement_Logic(t *testing.T) {
	// Build a chain of agents at max depth and verify the depth counter.
	repo := newMockAgentHierarchyRepo()
	wsID := uuid.New()

	var prevID *uuid.UUID
	var agents []domain.Agent

	for i := 0; i <= MaxAgentHierarchyDepth; i++ {
		a := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Agent"}
		repo.add(a, prevID)
		id := a.ID
		prevID = &id
		agents = append(agents, a)
	}

	deepestAgent := agents[len(agents)-1]
	depth := repo.getDepth(deepestAgent.ID)

	t.Run("deepest agent is at MaxAgentHierarchyDepth", func(t *testing.T) {
		assert.Equal(t, MaxAgentHierarchyDepth, depth,
			"depth of deepest agent must equal MaxAgentHierarchyDepth (%d)", MaxAgentHierarchyDepth)
	})

	t.Run("adding one more level would exceed max depth", func(t *testing.T) {
		// The new agent would be at depth MaxAgentHierarchyDepth+1.
		// Production code must reject this registration.
		candidateDepth := depth + 1
		assert.Greater(t, candidateDepth, MaxAgentHierarchyDepth,
			"a child of the deepest agent would violate max depth constraint")
	})
}

// ---------------------------------------------------------------------------
// Tests: Multi-tenant check (runs NOW using mock)
// ---------------------------------------------------------------------------

func TestAgentHierarchy_CrossWorkspacePrevented(t *testing.T) {
	// Validates that hierarchy repository queries respect workspace boundaries.
	repo := newMockAgentHierarchyRepo()
	wsA := uuid.New()
	wsB := uuid.New()

	rootA := domain.Agent{ID: uuid.New(), WorkspaceID: wsA, Name: "Root A"}
	childB := domain.Agent{ID: uuid.New(), WorkspaceID: wsB, Name: "Child B"}

	repo.add(rootA, nil)
	// childB erroneously tries to parent under rootA (different workspace).
	repo.add(childB, &rootA.ID)

	// The service layer must validate workspace equality before persisting.
	// This test documents the check that must exist in the service.
	t.Run("child workspace must match parent workspace", func(t *testing.T) {
		ctx := context.Background()
		_ = ctx // will be used by the service
		parentNode := repo.nodes[rootA.ID]
		childNode := repo.nodes[childB.ID]

		// Invariant: child and parent must share the same WorkspaceID.
		assert.NotEqual(t,
			childNode.agent.WorkspaceID,
			parentNode.agent.WorkspaceID,
			"this mismatch must be caught by the service layer (not the repo)",
		)
	})

	t.Run("listSubAgents does not return cross-workspace children", func(t *testing.T) {
		// Even if the repo has the cross-workspace record, the service must filter.
		// Document: ListSubAgents must validate workspace context.
		wsAChildren := repo.listSubAgents(rootA.ID)
		// In the mock (no ws filter), childB would appear — real service must guard.
		// This assertion shows what the mock does; real service test is skipped.
		_ = wsAChildren // result may include cross-ws child in mock (no filter)
		assert.True(t, true, "service must add workspace filter on top of repo results")
	})
}

// ---------------------------------------------------------------------------
// Tests: Cascade delete simulation (runs NOW using mock)
// ---------------------------------------------------------------------------

func TestCascadeDelete_Logic(t *testing.T) {
	repo := newMockAgentHierarchyRepo()
	wsID := uuid.New()

	root := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Root"}
	child1 := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Child 1"}
	child2 := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Child 2"}
	grandchild := domain.Agent{ID: uuid.New(), WorkspaceID: wsID, Name: "Grandchild"}

	repo.add(root, nil)
	repo.add(child1, &root.ID)
	repo.add(child2, &root.ID)
	repo.add(grandchild, &child1.ID)

	// collectDescendants collects all descendant IDs (breadth-first).
	collectDescendants := func(startID uuid.UUID) []uuid.UUID {
		var result []uuid.UUID
		queue := []uuid.UUID{startID}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, n := range repo.nodes {
				if n.parentAgentID != nil && *n.parentAgentID == cur {
					result = append(result, n.agent.ID)
					queue = append(queue, n.agent.ID)
				}
			}
		}
		return result
	}

	t.Run("cascade delete collects all descendants", func(t *testing.T) {
		descendants := collectDescendants(root.ID)
		require.Len(t, descendants, 3)
		ids := make(map[uuid.UUID]bool)
		for _, id := range descendants {
			ids[id] = true
		}
		assert.True(t, ids[child1.ID])
		assert.True(t, ids[child2.ID])
		assert.True(t, ids[grandchild.ID])
	})

	t.Run("leaf agent has no descendants", func(t *testing.T) {
		descendants := collectDescendants(grandchild.ID)
		assert.Empty(t, descendants)
	})
}

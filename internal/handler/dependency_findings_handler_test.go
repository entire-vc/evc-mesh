package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/middleware"
)

func findingsEdge(task, blocker uuid.UUID, cond domain.DependencyCondition, released bool, age int) domain.TaskDependency {
	return domain.TaskDependency{
		ID: uuid.New(), TaskID: task, DependsOnTaskID: blocker, DependencyType: domain.DependencyTypeBlocks,
		Condition: cond, ConditionSatisfied: released, CreatedAt: time.Unix(int64(1000+age), 0),
	}
}

func getFindings(t *testing.T, h *DependencyHandler, project uuid.UUID, setup func(c echo.Context)) (int, DependencyFindingsResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.SetParamNames("proj_id")
	c.SetParamValues(project.String())
	setup(c)
	require.NoError(t, h.DependencyFindings(c))
	var out DependencyFindingsResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	}
	return rec.Code, out
}

// The production route reaches domain.GraphFindings through the actor-scoped graph read and
// reports the golden shapes: the release<->acceptance loop (prep proceeds, final waits), the
// AND of two blockers and a diamond. Released edges hold nothing; nothing is deleted.
func TestDependencyFindings_GoldenCases(t *testing.T) {
	project, actor := uuid.New(), uuid.New()
	e0, rel, consumerA, consumerB, root, a, b, join, free := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	loopEdge := findingsEdge(e0, rel, domain.DependencyConditionDeployed, false, 2)
	deps := []domain.TaskDependency{
		// e0 waits for the release to be deployed; the release waits for e0 to be accepted.
		loopEdge,
		findingsEdge(rel, e0, domain.DependencyConditionAccepted, false, 1),
		// AND: consumerA needs two unreleased blockers at once.
		findingsEdge(consumerA, consumerB, domain.DependencyConditionTaskDone, false, 3),
		findingsEdge(consumerA, free, domain.DependencyConditionTaskDone, false, 4),
		// Diamond: join <- a <- root, join <- b <- root.
		findingsEdge(join, a, domain.DependencyConditionTaskDone, false, 5),
		findingsEdge(join, b, domain.DependencyConditionTaskDone, false, 6),
		findingsEdge(a, root, domain.DependencyConditionTaskDone, false, 7),
		findingsEdge(b, root, domain.DependencyConditionTaskDone, false, 8),
		// A released edge must not count anywhere.
		findingsEdge(free, root, domain.DependencyConditionTaskDone, true, 9),
	}
	nodes := []domain.GraphNode{{ID: e0, AssigneeName: "Dev-A"}, {ID: rel, AssigneeName: "Dev-B"}, {ID: root, AssigneeName: "Dev-C"}, {ID: consumerB, AssigneeName: "Dev-D"},
		{ID: consumerA}, {ID: a}, {ID: b}, {ID: join}, {ID: free}}
	for i := range nodes {
		nodes[i].Availability = "available"
	}
	h := NewDependencyHandler(nil, nil).WithGraphReader(graphReaderFunc(func(_ context.Context, p, focus uuid.UUID, s domain.GraphScope) (*domain.GraphSnapshot, error) {
		require.Equal(t, project, p)
		require.Equal(t, uuid.Nil, focus)
		require.Equal(t, actor, s.ActorID)
		return &domain.GraphSnapshot{Nodes: nodes, Dependencies: deps, Revision: "rev-7"}, nil
	}))
	code, out := getFindings(t, h, project, func(c echo.Context) {
		c.Set(middleware.ContextKeyUserID, actor)
		c.Set(middleware.ContextKeyWorkspaceID, uuid.New())
	})
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, FindingsVersion, out.Version)
	require.Equal(t, "rev-7", out.Revision)
	kinds := map[string][]domain.Finding{}
	for _, f := range out.Findings {
		kinds[f.Kind] = append(kinds[f.Kind], f)
	}
	// Both loop edges are final-stage conditions: not a hard cycle, a semantic loop.
	require.Empty(t, kinds[domain.FindingHardCycle])
	require.Len(t, kinds[domain.FindingSemanticLoop], 1)
	loop := kinds[domain.FindingSemanticLoop][0]
	require.ElementsMatch(t, []uuid.UUID{e0, rel}, loop.Tasks)
	require.Equal(t, "Dev-A", loop.Owner, "the owner of the newest edge's dependent is asked to act")
	require.NotEmpty(t, loop.Action)
	require.Len(t, kinds[domain.FindingAndBlockers], 2)
	require.Len(t, kinds[domain.FindingDiamond], 1)
	require.Equal(t, root, kinds[domain.FindingDiamond][0].Tasks[0])
	require.Equal(t, "Dev-C", kinds[domain.FindingDiamond][0].Owner)
}

// With every edge released there is nothing to report, and the answer is an empty list, not null.
func TestDependencyFindings_ReleasedEdgesReportNothing(t *testing.T) {
	project, actor := uuid.New(), uuid.New()
	x, y := uuid.New(), uuid.New()
	h := NewDependencyHandler(nil, nil).WithGraphReader(graphReaderFunc(func(context.Context, uuid.UUID, uuid.UUID, domain.GraphScope) (*domain.GraphSnapshot, error) {
		return &domain.GraphSnapshot{
			Nodes: []domain.GraphNode{{ID: x, Availability: "available"}, {ID: y, Availability: "available"}},
			Dependencies: []domain.TaskDependency{
				findingsEdge(x, y, domain.DependencyConditionAccepted, true, 1),
				findingsEdge(y, x, domain.DependencyConditionDeployed, true, 2),
			}}, nil
	}))
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.SetParamNames("proj_id")
	c.SetParamValues(project.String())
	c.Set(middleware.ContextKeyUserID, actor)
	c.Set(middleware.ContextKeyWorkspaceID, uuid.New())
	require.NoError(t, h.DependencyFindings(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"findings":[]`)
}

// Guards run before any read: bad project id, no workspace, no actor.
func TestDependencyFindings_Guards(t *testing.T) {
	h := NewDependencyHandler(nil, nil).WithGraphReader(graphReaderFunc(func(context.Context, uuid.UUID, uuid.UUID, domain.GraphScope) (*domain.GraphSnapshot, error) {
		t.Fatal("must fail before reading")
		return nil, nil
	}))
	for name, tc := range map[string]struct {
		project string
		setup   func(c echo.Context)
	}{
		"bad project":  {"nope", func(c echo.Context) {}},
		"no workspace": {uuid.NewString(), func(c echo.Context) { c.Set(middleware.ContextKeyUserID, uuid.New()) }},
		"no actor":     {uuid.NewString(), func(c echo.Context) { c.Set(middleware.ContextKeyWorkspaceID, uuid.New()) }},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
			c.SetParamNames("proj_id")
			c.SetParamValues(tc.project)
			tc.setup(c)
			require.NoError(t, h.DependencyFindings(c))
			require.True(t, rec.Code == http.StatusBadRequest || rec.Code == http.StatusForbidden, rec.Code)
		})
	}
}

// For an edge whose blocker the actor may not open, ReadGraph exposes only the evidence half of
// the verdict. The blocker may well be done (edge released) while that half reads false, so such
// an edge must not feed a finding: a cycle through it would be a false positive.
func TestDependencyFindings_RestrictedBlockerEdgesAreNotDiagnosed(t *testing.T) {
	project, actor := uuid.New(), uuid.New()
	x, y, hidden := uuid.New(), uuid.New(), uuid.New()
	nodes := []domain.GraphNode{{ID: x, Availability: "available"}, {ID: y, Availability: "available"}, {ID: hidden, Availability: "restricted"}}
	deps := []domain.TaskDependency{
		// A loop x <-> y closed through a restricted card: its state is unreadable here.
		findingsEdge(x, hidden, domain.DependencyConditionTaskDone, false, 1),
		findingsEdge(hidden, x, domain.DependencyConditionTaskDone, false, 2),
		// AND on y: one readable unreleased blocker plus one restricted blocker.
		findingsEdge(y, x, domain.DependencyConditionTaskDone, false, 3),
		findingsEdge(y, hidden, domain.DependencyConditionTaskDone, false, 4),
	}
	h := NewDependencyHandler(nil, nil).WithGraphReader(graphReaderFunc(func(context.Context, uuid.UUID, uuid.UUID, domain.GraphScope) (*domain.GraphSnapshot, error) {
		return &domain.GraphSnapshot{Nodes: nodes, Dependencies: deps}, nil
	}))
	code, out := getFindings(t, h, project, func(c echo.Context) {
		c.Set(middleware.ContextKeyUserID, actor)
		c.Set(middleware.ContextKeyWorkspaceID, uuid.New())
	})
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, out.Findings, "no finding may be built on an edge whose blocker state is unreadable")
}

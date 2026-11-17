package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// analyticsService implements AnalyticsService using raw SQL for efficiency.
type analyticsService struct {
	db *sqlx.DB
}

// NewAnalyticsService creates a new analyticsService.
func NewAnalyticsService(db *sqlx.DB) AnalyticsService {
	return &analyticsService{db: db}
}

// GetMetrics returns aggregated workspace/project metrics for the given filter,
// counted only over the projects filter.Viewer may access. An explicit project
// must belong to the workspace for every role (owner and admin included) and be
// visible to the viewer; otherwise the answer is 403.
func (s *analyticsService) GetMetrics(ctx context.Context, filter AnalyticsFilter) (*AnalyticsMetrics, error) {
	if filter.ProjectID != nil {
		var ok bool
		a := &sqlArgs{}
		q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM projects p WHERE p.id = %s AND p.workspace_id = %s AND %s)`,
			a.add(*filter.ProjectID), a.add(filter.WorkspaceID), visibleProject(a, filter.Viewer, "p.id"))
		if err := s.db.GetContext(ctx, &ok, q, a.vals...); err != nil {
			return nil, fmt.Errorf("analytics project access: %w", err)
		}
		if !ok {
			return nil, apierror.Forbidden("not a member of this project")
		}
	}

	taskMetrics, err := s.queryTaskMetrics(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("analytics task metrics: %w", err)
	}

	agentMetrics, err := s.queryAgentMetrics(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("analytics agent metrics: %w", err)
	}

	eventMetrics, err := s.queryEventMetrics(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("analytics event metrics: %w", err)
	}

	timeline, err := s.queryTimeline(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("analytics timeline: %w", err)
	}

	costMetrics, err := s.queryCostMetrics(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("analytics cost metrics: %w", err)
	}

	return &AnalyticsMetrics{
		TaskMetrics:  *taskMetrics,
		AgentMetrics: *agentMetrics,
		EventMetrics: *eventMetrics,
		CostMetrics:  *costMetrics,
		Timeline:     timeline,
	}, nil
}

// sqlArgs collects positional arguments; add returns the placeholder of the value.
type sqlArgs struct{ vals []interface{} }

func (a *sqlArgs) add(v interface{}) string {
	a.vals = append(a.vals, v)
	return fmt.Sprintf("$%d", len(a.vals))
}

// visibleProject is the predicate "the viewer may access the project in projCol",
// the SQL form of middleware.UserMayAccessProject (agents: own membership only).
func visibleProject(a *sqlArgs, v AnalyticsViewer, projCol string) string {
	if v.AllProjects {
		return "TRUE"
	}
	col := "vm.user_id"
	if v.Agent {
		col = "vm.agent_id"
	}
	return fmt.Sprintf("EXISTS (SELECT 1 FROM project_members vm WHERE vm.project_id = %s AND %s = %s)", projCol, col, a.add(v.ActorID))
}

// projectScope narrows task rows (alias t) to the workspace, the optional
// project filter and the viewer's projects. The caller joins projects p.
func projectScope(a *sqlArgs, f AnalyticsFilter) string {
	w := "p.workspace_id = " + a.add(f.WorkspaceID)
	if f.ProjectID != nil {
		w += " AND t.project_id = " + a.add(*f.ProjectID)
	}
	return w + " AND " + visibleProject(a, f.Viewer, "t.project_id")
}

// taskStatusCategoryRow holds one row from the status-category aggregation.
type taskStatusCategoryRow struct {
	Category string `db:"category"`
	Count    int    `db:"cnt"`
}

// taskPriorityRow holds one row from the priority aggregation.
type taskPriorityRow struct {
	Priority string `db:"priority"`
	Count    int    `db:"cnt"`
}

// assigneeCountQueryRow holds one row from the per-assignee aggregation.
type assigneeCountQueryRow struct {
	AssigneeID   *uuid.UUID `db:"assignee_id"`
	AssigneeType string     `db:"assignee_type"`
	Name         string     `db:"name"`
	Count        int        `db:"cnt"`
}

// projectCountQueryRow holds one row from the per-project aggregation.
type projectCountQueryRow struct {
	ProjectID   uuid.UUID `db:"project_id"`
	ProjectName string    `db:"project_name"`
	Count       int       `db:"cnt"`
}

// breakdownLimit caps the assignee and project breakdowns; the rest is "other" for the client.
const breakdownLimit = 50

func (s *analyticsService) queryTaskMetrics(ctx context.Context, filter AnalyticsFilter) (*TaskMetrics, error) {
	// Total tasks.
	a := &sqlArgs{}
	totalQ := `SELECT COUNT(*) FROM tasks t JOIN projects p ON p.id = t.project_id WHERE ` + projectScope(a, filter)
	var total int
	if err := s.db.GetContext(ctx, &total, totalQ, a.vals...); err != nil {
		return nil, err
	}

	// By status category.
	a = &sqlArgs{}
	catQ := `
		SELECT ts.category, COUNT(*) AS cnt
		FROM tasks t
		JOIN task_statuses ts ON ts.id = t.status_id
		JOIN projects p ON p.id = t.project_id
		WHERE ` + projectScope(a, filter) + `
		GROUP BY ts.category`
	var catRows []taskStatusCategoryRow
	if err := s.db.SelectContext(ctx, &catRows, catQ, a.vals...); err != nil {
		return nil, err
	}
	byCategory := map[string]int{
		"backlog":     0,
		"todo":        0,
		"in_progress": 0,
		"review":      0,
		"done":        0,
		"cancelled":   0,
	}
	for _, row := range catRows {
		byCategory[row.Category] = row.Count
	}

	// By priority.
	a = &sqlArgs{}
	priQ := `
		SELECT t.priority, COUNT(*) AS cnt
		FROM tasks t
		JOIN projects p ON p.id = t.project_id
		WHERE ` + projectScope(a, filter) + `
		GROUP BY t.priority`
	var priRows []taskPriorityRow
	if err := s.db.SelectContext(ctx, &priRows, priQ, a.vals...); err != nil {
		return nil, err
	}
	byPriority := map[string]int{
		"urgent": 0,
		"high":   0,
		"medium": 0,
		"low":    0,
		"none":   0,
	}
	for _, row := range priRows {
		byPriority[row.Priority] = row.Count
	}

	// By assignee: an agent by its name, a user by display name, nobody as "unassigned".
	a = &sqlArgs{}
	assigneeQ := fmt.Sprintf(`
		SELECT CASE WHEN t.assignee_type IN ('agent', 'user') THEN t.assignee_id END AS assignee_id,
		       CASE WHEN t.assignee_type IN ('agent', 'user') AND t.assignee_id IS NOT NULL THEN t.assignee_type::text ELSE 'unassigned' END AS assignee_type,
		       COALESCE(MAX(CASE t.assignee_type WHEN 'agent' THEN ag.name WHEN 'user' THEN u.display_name END), '') AS name,
		       COUNT(*) AS cnt
		FROM tasks t
		JOIN projects p ON p.id = t.project_id
		LEFT JOIN agents ag ON t.assignee_type = 'agent' AND ag.id = t.assignee_id
		LEFT JOIN users u ON t.assignee_type = 'user' AND u.id = t.assignee_id
		WHERE %s
		GROUP BY 1, 2
		ORDER BY cnt DESC, name ASC
		LIMIT %d`, projectScope(a, filter), breakdownLimit)
	var assigneeRows []assigneeCountQueryRow
	if err := s.db.SelectContext(ctx, &assigneeRows, assigneeQ, a.vals...); err != nil {
		return nil, err
	}
	byAssignee := make([]AssigneeCountRow, len(assigneeRows))
	for i, r := range assigneeRows {
		byAssignee[i] = AssigneeCountRow(r)
	}

	// By project.
	a = &sqlArgs{}
	projectQ := fmt.Sprintf(`
		SELECT p.id AS project_id, p.name AS project_name, COUNT(*) AS cnt
		FROM tasks t
		JOIN projects p ON p.id = t.project_id
		WHERE %s
		GROUP BY p.id, p.name
		ORDER BY cnt DESC, p.name ASC
		LIMIT %d`, projectScope(a, filter), breakdownLimit)
	var projectRows []projectCountQueryRow
	if err := s.db.SelectContext(ctx, &projectRows, projectQ, a.vals...); err != nil {
		return nil, err
	}
	byProject := make([]ProjectCountRow, len(projectRows))
	for i, r := range projectRows {
		byProject[i] = ProjectCountRow(r)
	}

	// Created this period.
	a = &sqlArgs{}
	scope := projectScope(a, filter)
	createdQ := fmt.Sprintf(`
		SELECT COUNT(*) FROM tasks t
		JOIN projects p ON p.id = t.project_id
		WHERE %s AND t.created_at >= %s AND t.created_at <= %s`, scope, a.add(filter.From), a.add(filter.To))
	var createdCount int
	if err := s.db.GetContext(ctx, &createdCount, createdQ, a.vals...); err != nil {
		return nil, err
	}

	a = &sqlArgs{}
	scope = projectScope(a, filter)
	completedQ := fmt.Sprintf(`
		SELECT COUNT(*) FROM tasks t
		JOIN projects p ON p.id = t.project_id
		WHERE %s AND t.completed_at >= %s AND t.completed_at <= %s`, scope, a.add(filter.From), a.add(filter.To))
	var completedCount int
	if err := s.db.GetContext(ctx, &completedCount, completedQ, a.vals...); err != nil {
		return nil, err
	}

	// Overdue now: a due date in the past and a status that is neither done nor
	// cancelled, the board's rule. A snapshot like Total, not bound to the period.
	a = &sqlArgs{}
	scope = projectScope(a, filter)
	overdueQ := fmt.Sprintf(`
		SELECT COUNT(*) FROM tasks t
		JOIN projects p ON p.id = t.project_id
		JOIN task_statuses ts ON ts.id = t.status_id
		WHERE %s AND t.due_date IS NOT NULL AND t.due_date < now()
		  AND ts.category NOT IN ('done', 'cancelled')`, scope)
	var overdueCount int
	if err := s.db.GetContext(ctx, &overdueCount, overdueQ, a.vals...); err != nil {
		return nil, err
	}

	return &TaskMetrics{
		Total:               total,
		Overdue:             overdueCount,
		ByStatusCategory:    byCategory,
		ByPriority:          byPriority,
		CreatedThisPeriod:   createdCount,
		CompletedThisPeriod: completedCount,
		ByAssignee:          byAssignee,
		ByProject:           byProject,
	}, nil
}

// agentCountRow holds one row from the workspace-agent aggregation.
type agentCountRow struct {
	Total  int `db:"total"`
	Active int `db:"active_count"`
}

// agentTaskRow holds one row from the per-agent task completion query.
type agentTaskQueryRow struct {
	AgentID   uuid.UUID `db:"agent_id"`
	AgentName string    `db:"agent_name"`
	Completed int       `db:"completed"`
}

func (s *analyticsService) queryAgentMetrics(ctx context.Context, filter AnalyticsFilter) (*AgentMetrics, error) {
	// The agent roster is workspace-level (the team directory shows it to every
	// member), so the counts are not narrowed by project.
	const countQ = `
		SELECT COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE status IN ('online', 'busy')) AS active_count
		FROM agents
		WHERE workspace_id = $1 AND deleted_at IS NULL
	`
	var counts agentCountRow
	if err := s.db.GetContext(ctx, &counts, countQ, filter.WorkspaceID); err != nil {
		return nil, err
	}

	// Per-agent count of tasks in a done status completed within the period,
	// over the projects in scope (same bounds as CompletedThisPeriod).
	a := &sqlArgs{}
	scope := projectScope(a, filter)
	tasksQ := fmt.Sprintf(`
		SELECT a.id AS agent_id, a.name AS agent_name, COUNT(t.id) AS completed
		FROM agents a
		JOIN tasks t ON t.assignee_id = a.id AND t.assignee_type = 'agent'
		JOIN task_statuses ts ON ts.id = t.status_id AND ts.category = 'done'
		JOIN projects p ON p.id = t.project_id
		WHERE a.deleted_at IS NULL AND a.workspace_id = p.workspace_id AND %s
		  AND t.completed_at >= %s AND t.completed_at <= %s
		GROUP BY a.id, a.name
		ORDER BY completed DESC
		LIMIT 20`, scope, a.add(filter.From), a.add(filter.To))
	var rows []agentTaskQueryRow
	if err := s.db.SelectContext(ctx, &rows, tasksQ, a.vals...); err != nil {
		return nil, err
	}
	agentTasks := make([]AgentTaskRow, len(rows))
	for i, r := range rows {
		agentTasks[i] = AgentTaskRow(r)
	}

	return &AgentMetrics{
		TotalAgents:  counts.Total,
		ActiveAgents: counts.Active,
		TasksByAgent: agentTasks,
	}, nil
}

// eventTypeRow holds one row from the event-type aggregation.
type eventTypeRow struct {
	EventType string `db:"event_type"`
	Count     int    `db:"cnt"`
}

// eventScope narrows event_bus_messages em (every row has a project) to the
// workspace, the optional project filter and the viewer's projects.
func eventScope(a *sqlArgs, f AnalyticsFilter) string {
	w := "em.workspace_id = " + a.add(f.WorkspaceID)
	if f.ProjectID != nil {
		w += " AND em.project_id = " + a.add(*f.ProjectID)
	}
	return w + " AND " + visibleProject(a, f.Viewer, "em.project_id")
}

func (s *analyticsService) queryEventMetrics(ctx context.Context, filter AnalyticsFilter) (*EventMetrics, error) {
	// floorQ finds the oldest row the TTL-bounded queue still physically
	// holds for this scope RIGHT NOW — deliberately without a period filter.
	// It tells us how far back the queue's current contents can be trusted,
	// not what happened in the requested period.
	a := &sqlArgs{}
	floorQ := `SELECT MIN(em.created_at) FROM event_bus_messages em WHERE ` + eventScope(a, filter)
	var retainedSince sql.NullTime
	if err := s.db.GetContext(ctx, &retainedSince, floorQ, a.vals...); err != nil {
		return nil, err
	}
	var retainedSincePtr *time.Time
	if retainedSince.Valid {
		t := retainedSince.Time
		retainedSincePtr = &t
	}
	// The queue's contents cover the requested period only if the oldest row
	// it still has predates (or equals) the requested start. No rows at all
	// for this scope proves nothing about the past — it stays uncovered.
	fullyCovered := retainedSincePtr != nil && !filter.From.Before(*retainedSincePtr)

	a = &sqlArgs{}
	scope := eventScope(a, filter)
	totalQ := fmt.Sprintf(`
		SELECT COUNT(*) FROM event_bus_messages em
		WHERE %s AND em.created_at >= %s AND em.created_at <= %s`, scope, a.add(filter.From), a.add(filter.To))
	var total int
	if err := s.db.GetContext(ctx, &total, totalQ, a.vals...); err != nil {
		return nil, err
	}

	a = &sqlArgs{}
	scope = eventScope(a, filter)
	byTypeQ := fmt.Sprintf(`
		SELECT em.event_type, COUNT(*) AS cnt
		FROM event_bus_messages em
		WHERE %s AND em.created_at >= %s AND em.created_at <= %s
		GROUP BY em.event_type
		ORDER BY cnt DESC`, scope, a.add(filter.From), a.add(filter.To))
	var typeRows []eventTypeRow
	if err := s.db.SelectContext(ctx, &typeRows, byTypeQ, a.vals...); err != nil {
		return nil, err
	}
	byType := make(map[string]int, len(typeRows))
	for _, row := range typeRows {
		byType[row.EventType] = row.Count
	}

	return &EventMetrics{
		TotalEvents:        total,
		ByType:             byType,
		RetainedSince:      retainedSincePtr,
		PeriodFullyCovered: fullyCovered,
	}, nil
}

// dayMetricRow holds one row from the daily timeline aggregation.
type dayMetricRow struct {
	Date      time.Time `db:"day"`
	Created   int       `db:"created"`
	Completed int       `db:"completed"`
}

func (s *analyticsService) queryTimeline(ctx context.Context, filter AnalyticsFilter) ([]DayMetric, error) {
	a := &sqlArgs{}
	scope := projectScope(a, filter)
	from, to := a.add(filter.From), a.add(filter.To)
	timelineQ := fmt.Sprintf(`
		SELECT
			day::date AS day,
			COALESCE(SUM(created), 0) AS created,
			COALESCE(SUM(completed), 0) AS completed
		FROM (
			SELECT date_trunc('day', t.created_at) AS day, 1 AS created, 0 AS completed
			FROM tasks t
			JOIN projects p ON p.id = t.project_id
			WHERE %[1]s
			AND t.created_at >= %[2]s AND t.created_at <= %[3]s
			UNION ALL
			SELECT date_trunc('day', t.completed_at) AS day, 0 AS created, 1 AS completed
			FROM tasks t
			JOIN projects p ON p.id = t.project_id
			WHERE %[1]s
			AND t.completed_at >= %[2]s AND t.completed_at <= %[3]s
		) sub
		GROUP BY day
		ORDER BY day ASC`, scope, from, to)

	var rows []dayMetricRow
	if err := s.db.SelectContext(ctx, &rows, timelineQ, a.vals...); err != nil {
		return nil, err
	}

	result := make([]DayMetric, len(rows))
	for i, row := range rows {
		result[i] = DayMetric{
			Date:      row.Date.Format("2006-01-02"),
			Created:   row.Created,
			Completed: row.Completed,
		}
	}
	return result, nil
}

// costTotalsRow holds the workspace-wide cost/token totals for the period.
type costTotalsRow struct {
	Cost                 float64 `db:"cost"`
	TokensIn             int64   `db:"tokens_in"`
	TokensOut            int64   `db:"tokens_out"`
	SessionCount         int     `db:"session_count"`
	ReportedSessionCount int     `db:"reported_session_count"`
	// A reporter said the figure is unknown for at least one session in the period
	// (the totals above are then the known sum). Sessions that never reported are
	// counted by ReportedSessionCount, not here.
	CostIncomplete      bool `db:"cost_incomplete"`
	TokensInIncomplete  bool `db:"tokens_in_incomplete"`
	TokensOutIncomplete bool `db:"tokens_out_incomplete"`
}

// agentCostQueryRow holds one row from the per-agent cost aggregation.
type agentCostQueryRow struct {
	AgentID   uuid.UUID `db:"agent_id"`
	AgentName string    `db:"agent_name"`
	Cost      float64   `db:"cost"`
	TokensIn  int64     `db:"tokens_in"`
	TokensOut int64     `db:"tokens_out"`
}

// projectCostQueryRow holds one row from the per-project cost aggregation.
type projectCostQueryRow struct {
	ProjectID   uuid.UUID `db:"project_id"`
	ProjectName string    `db:"project_name"`
	Cost        float64   `db:"cost"`
	TokensIn    int64     `db:"tokens_in"`
	TokensOut   int64     `db:"tokens_out"`
}

// dayCostQueryRow holds one row from the daily cost aggregation.
type dayCostQueryRow struct {
	Date      time.Time `db:"day"`
	Cost      float64   `db:"cost"`
	TokensIn  int64     `db:"tokens_in"`
	TokensOut int64     `db:"tokens_out"`
}

// taskCostQueryRow holds one row from the top-N task cost aggregation.
type taskCostQueryRow struct {
	TaskID       uuid.UUID `db:"task_id"`
	TaskTitle    string    `db:"task_title"`
	Cost         float64   `db:"cost"`
	TokensIn     int64     `db:"tokens_in"`
	TokensOut    int64     `db:"tokens_out"`
	SessionCount int       `db:"session_count"`
}

// queryCostMetrics aggregates agent_sessions cost/token data (populated by the fiddler
// session_report MCP tool) for the workspace/period, grouped by agent, project, and day,
// plus a top-N most expensive tasks leaderboard.
func (s *analyticsService) queryCostMetrics(ctx context.Context, filter AnalyticsFilter) (*CostMetrics, error) {
	// agent_sessions carries its own workspace_id but only an optional task_id — project
	// grouping requires a join through tasks, so sessions never joined to a task have no
	// project affiliation and are naturally excluded from the by-project/top-tasks views.
	// ags.workspace_id says nothing about the task's workspace: a session may point at a
	// task of another workspace. The joins keep only tasks of this workspace's projects;
	// a session whose task is elsewhere counts as a session without a task.
	args := []interface{}{filter.WorkspaceID, filter.From, filter.To}
	leftJoinProjectFilter := ""
	innerJoinProjectFilter := ""
	if filter.ProjectID != nil {
		args = append(args, *filter.ProjectID)
		leftJoinProjectFilter = "AND t.project_id = $4"
		innerJoinProjectFilter = "AND t.project_id = $4"
	}
	// Outside owner/admin a session counts only through a task of a project the
	// viewer may access; a session without a task cannot be placed, so it is left out.
	if !filter.Viewer.AllProjects {
		a := &sqlArgs{vals: args}
		vis := " AND t.project_id IS NOT NULL AND " + visibleProject(a, filter.Viewer, "t.project_id")
		args = a.vals
		leftJoinProjectFilter += vis
		innerJoinProjectFilter += vis
	}

	totalsQ := fmt.Sprintf(`
		SELECT COALESCE(SUM(ags.estimated_cost), 0) AS cost,
		       COALESCE(SUM(ags.tokens_in), 0) AS tokens_in,
		       COALESCE(SUM(ags.tokens_out), 0) AS tokens_out,
		       COUNT(*) AS session_count,
		       COUNT(*) FILTER (WHERE ags.model_used <> '' AND ags.model_used IS NOT NULL) AS reported_session_count,
		       COALESCE(bool_or(ags.cost_unknown), false) AS cost_incomplete,
		       COALESCE(bool_or(ags.tokens_in_unknown), false) AS tokens_in_incomplete,
		       COALESCE(bool_or(ags.tokens_out_unknown), false) AS tokens_out_incomplete
		FROM agent_sessions ags
		LEFT JOIN tasks t ON t.id = ags.task_id
			AND EXISTS (SELECT 1 FROM projects tp WHERE tp.id = t.project_id AND tp.workspace_id = $1)
		WHERE ags.workspace_id = $1
		  AND ags.started_at >= $2 AND ags.started_at <= $3
		  %s
	`, leftJoinProjectFilter)
	var totals costTotalsRow
	if err := s.db.GetContext(ctx, &totals, totalsQ, args...); err != nil {
		return nil, err
	}

	agentQ := fmt.Sprintf(`
		SELECT ags.agent_id, a.name AS agent_name,
		       COALESCE(SUM(ags.estimated_cost), 0) AS cost,
		       COALESCE(SUM(ags.tokens_in), 0) AS tokens_in,
		       COALESCE(SUM(ags.tokens_out), 0) AS tokens_out
		FROM agent_sessions ags
		JOIN agents a ON a.id = ags.agent_id
		LEFT JOIN tasks t ON t.id = ags.task_id
			AND EXISTS (SELECT 1 FROM projects tp WHERE tp.id = t.project_id AND tp.workspace_id = $1)
		WHERE ags.workspace_id = $1
		  AND ags.started_at >= $2 AND ags.started_at <= $3
		  %s
		GROUP BY ags.agent_id, a.name
		ORDER BY cost DESC
		LIMIT 20
	`, leftJoinProjectFilter)
	var agentRows []agentCostQueryRow
	if err := s.db.SelectContext(ctx, &agentRows, agentQ, args...); err != nil {
		return nil, err
	}
	byAgent := make([]AgentCostRow, len(agentRows))
	for i, r := range agentRows {
		byAgent[i] = AgentCostRow(r)
	}

	projectQ := fmt.Sprintf(`
		SELECT t.project_id, p.name AS project_name,
		       COALESCE(SUM(ags.estimated_cost), 0) AS cost,
		       COALESCE(SUM(ags.tokens_in), 0) AS tokens_in,
		       COALESCE(SUM(ags.tokens_out), 0) AS tokens_out
		FROM agent_sessions ags
		JOIN tasks t ON t.id = ags.task_id
		JOIN projects p ON p.id = t.project_id AND p.workspace_id = $1
		WHERE ags.workspace_id = $1
		  AND ags.started_at >= $2 AND ags.started_at <= $3
		  %s
		GROUP BY t.project_id, p.name
		ORDER BY cost DESC
		LIMIT 20
	`, innerJoinProjectFilter)
	var projectRows []projectCostQueryRow
	if err := s.db.SelectContext(ctx, &projectRows, projectQ, args...); err != nil {
		return nil, err
	}
	byProject := make([]ProjectCostRow, len(projectRows))
	for i, r := range projectRows {
		byProject[i] = ProjectCostRow(r)
	}

	dayQ := fmt.Sprintf(`
		SELECT date_trunc('day', ags.started_at)::date AS day,
		       COALESCE(SUM(ags.estimated_cost), 0) AS cost,
		       COALESCE(SUM(ags.tokens_in), 0) AS tokens_in,
		       COALESCE(SUM(ags.tokens_out), 0) AS tokens_out
		FROM agent_sessions ags
		LEFT JOIN tasks t ON t.id = ags.task_id
			AND EXISTS (SELECT 1 FROM projects tp WHERE tp.id = t.project_id AND tp.workspace_id = $1)
		WHERE ags.workspace_id = $1
		  AND ags.started_at >= $2 AND ags.started_at <= $3
		  %s
		GROUP BY day
		ORDER BY day ASC
	`, leftJoinProjectFilter)
	var dayRows []dayCostQueryRow
	if err := s.db.SelectContext(ctx, &dayRows, dayQ, args...); err != nil {
		return nil, err
	}
	byDay := make([]DayCostMetric, len(dayRows))
	for i, r := range dayRows {
		byDay[i] = DayCostMetric{
			Date:      r.Date.Format("2006-01-02"),
			Cost:      r.Cost,
			TokensIn:  r.TokensIn,
			TokensOut: r.TokensOut,
		}
	}

	// ORDER BY comes from a closed set; user input is never interpolated.
	var topTasksOrder string
	switch filter.TopTasksSort {
	case "", TopTasksSortCost:
		topTasksOrder = "cost DESC, ags.task_id ASC"
	case TopTasksSortTokens:
		topTasksOrder = "(COALESCE(SUM(ags.tokens_in), 0) + COALESCE(SUM(ags.tokens_out), 0)) DESC, ags.task_id ASC"
	default:
		return nil, fmt.Errorf("invalid top_tasks_sort %q", filter.TopTasksSort)
	}

	topTasksQ := fmt.Sprintf(`
		SELECT ags.task_id, t.title AS task_title,
		       COALESCE(SUM(ags.estimated_cost), 0) AS cost,
		       COALESCE(SUM(ags.tokens_in), 0) AS tokens_in,
		       COALESCE(SUM(ags.tokens_out), 0) AS tokens_out,
		       COUNT(*) AS session_count
		FROM agent_sessions ags
		JOIN tasks t ON t.id = ags.task_id
		JOIN projects p ON p.id = t.project_id AND p.workspace_id = $1
		WHERE ags.workspace_id = $1
		  AND ags.started_at >= $2 AND ags.started_at <= $3
		  %s
		GROUP BY ags.task_id, t.title
		ORDER BY %s
		LIMIT 10
	`, innerJoinProjectFilter, topTasksOrder)
	var taskRows []taskCostQueryRow
	if err := s.db.SelectContext(ctx, &taskRows, topTasksQ, args...); err != nil {
		return nil, err
	}
	topTasks := make([]TaskCostRow, len(taskRows))
	for i, r := range taskRows {
		topTasks[i] = TaskCostRow(r)
	}

	return &CostMetrics{
		TotalCost:            totals.Cost,
		TotalTokensIn:        totals.TokensIn,
		TotalTokensOut:       totals.TokensOut,
		SessionCount:         totals.SessionCount,
		ReportedSessionCount: totals.ReportedSessionCount,
		CostIncomplete:       totals.CostIncomplete,
		TokensInIncomplete:   totals.TokensInIncomplete,
		TokensOutIncomplete:  totals.TokensOutIncomplete,
		ByAgent:              byAgent,
		ByProject:            byProject,
		ByDay:                byDay,
		TopTasks:             topTasks,
	}, nil
}

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// analyticsService implements AnalyticsService using raw SQL for efficiency.
type analyticsService struct {
	db *sqlx.DB
}

// NewAnalyticsService creates a new analyticsService.
func NewAnalyticsService(db *sqlx.DB) AnalyticsService {
	return &analyticsService{db: db}
}

// GetMetrics returns aggregated workspace/project metrics for the given filter.
func (s *analyticsService) GetMetrics(ctx context.Context, filter AnalyticsFilter) (*AnalyticsMetrics, error) {
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

	return &AnalyticsMetrics{
		TaskMetrics:  *taskMetrics,
		AgentMetrics: *agentMetrics,
		EventMetrics: *eventMetrics,
		Timeline:     timeline,
	}, nil
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

func (s *analyticsService) queryTaskMetrics(ctx context.Context, filter AnalyticsFilter) (*TaskMetrics, error) {
	// Build common WHERE clause depending on whether a project_id filter is set.
	var projectFilter string
	var args []interface{}

	if filter.ProjectID != nil {
		projectFilter = "AND t.project_id = $2"
		args = []interface{}{filter.WorkspaceID, *filter.ProjectID}
	} else {
		args = []interface{}{filter.WorkspaceID}
	}

	// Total tasks.
	totalQ := fmt.Sprintf(`
		SELECT COUNT(*) FROM tasks t
		JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1 %s
	`, projectFilter)
	var total int
	if err := s.db.GetContext(ctx, &total, totalQ, args...); err != nil {
		return nil, err
	}

	// By status category.
	catQ := fmt.Sprintf(`
		SELECT ts.category, COUNT(*) AS cnt
		FROM tasks t
		JOIN task_statuses ts ON ts.id = t.status_id
		JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1 %s
		GROUP BY ts.category
	`, projectFilter)
	var catRows []taskStatusCategoryRow
	if err := s.db.SelectContext(ctx, &catRows, catQ, args...); err != nil {
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
	priQ := fmt.Sprintf(`
		SELECT t.priority, COUNT(*) AS cnt
		FROM tasks t
		JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1 %s
		GROUP BY t.priority
	`, projectFilter)
	var priRows []taskPriorityRow
	if err := s.db.SelectContext(ctx, &priRows, priQ, args...); err != nil {
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

	// Created this period.
	var periodArgs []interface{}
	var periodFilter string
	if filter.ProjectID != nil {
		periodArgs = []interface{}{filter.WorkspaceID, *filter.ProjectID, filter.From, filter.To}
		periodFilter = "AND t.project_id = $2"
	} else {
		periodArgs = []interface{}{filter.WorkspaceID, filter.From, filter.To}
		periodFilter = ""
	}

	createdQ := fmt.Sprintf(`
		SELECT COUNT(*) FROM tasks t
		JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1 %s
		AND t.created_at >= $%d AND t.created_at <= $%d
	`, periodFilter, len(periodArgs)-1, len(periodArgs))
	var createdCount int
	if err := s.db.GetContext(ctx, &createdCount, createdQ, periodArgs...); err != nil {
		return nil, err
	}

	completedQ := fmt.Sprintf(`
		SELECT COUNT(*) FROM tasks t
		JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1 %s
		AND t.completed_at >= $%d AND t.completed_at <= $%d
	`, periodFilter, len(periodArgs)-1, len(periodArgs))
	var completedCount int
	if err := s.db.GetContext(ctx, &completedCount, completedQ, periodArgs...); err != nil {
		return nil, err
	}

	return &TaskMetrics{
		Total:               total,
		ByStatusCategory:    byCategory,
		ByPriority:          byPriority,
		CreatedThisPeriod:   createdCount,
		CompletedThisPeriod: completedCount,
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
	const countQ = `
		SELECT COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE status = 'active') AS active_count
		FROM agents
		WHERE workspace_id = $1 AND deleted_at IS NULL
	`
	var counts agentCountRow
	if err := s.db.GetContext(ctx, &counts, countQ, filter.WorkspaceID); err != nil {
		return nil, err
	}

	// Per-agent completed task count.
	const tasksQ = `
		SELECT a.id AS agent_id, a.name AS agent_name, COUNT(t.id) AS completed
		FROM agents a
		JOIN tasks t ON t.assignee_id = a.id AND t.assignee_type = 'agent'
		JOIN task_statuses ts ON ts.id = t.status_id AND ts.category = 'done'
		JOIN projects p ON p.id = t.project_id
		WHERE a.workspace_id = $1 AND a.deleted_at IS NULL AND p.workspace_id = $1
		GROUP BY a.id, a.name
		ORDER BY completed DESC
		LIMIT 20
	`
	var rows []agentTaskQueryRow
	if err := s.db.SelectContext(ctx, &rows, tasksQ, filter.WorkspaceID); err != nil {
		return nil, err
	}
	agentTasks := make([]AgentTaskRow, len(rows))
	for i, r := range rows {
		agentTasks[i] = AgentTaskRow{
			AgentID:   r.AgentID,
			AgentName: r.AgentName,
			Completed: r.Completed,
		}
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

func (s *analyticsService) queryEventMetrics(ctx context.Context, filter AnalyticsFilter) (*EventMetrics, error) {
	var args []interface{}
	var projectFilter string
	if filter.ProjectID != nil {
		args = []interface{}{filter.WorkspaceID, *filter.ProjectID}
		projectFilter = "AND em.project_id = $2"
	} else {
		args = []interface{}{filter.WorkspaceID}
	}

	totalQ := fmt.Sprintf(`
		SELECT COUNT(*) FROM event_bus_messages em
		WHERE em.workspace_id = $1 %s
	`, projectFilter)
	var total int
	if err := s.db.GetContext(ctx, &total, totalQ, args...); err != nil {
		return nil, err
	}

	byTypeQ := fmt.Sprintf(`
		SELECT em.event_type, COUNT(*) AS cnt
		FROM event_bus_messages em
		WHERE em.workspace_id = $1 %s
		GROUP BY em.event_type
		ORDER BY cnt DESC
	`, projectFilter)
	var typeRows []eventTypeRow
	if err := s.db.SelectContext(ctx, &typeRows, byTypeQ, args...); err != nil {
		return nil, err
	}
	byType := make(map[string]int, len(typeRows))
	for _, row := range typeRows {
		byType[row.EventType] = row.Count
	}

	return &EventMetrics{
		TotalEvents: total,
		ByType:      byType,
	}, nil
}

// dayMetricRow holds one row from the daily timeline aggregation.
type dayMetricRow struct {
	Date      time.Time `db:"day"`
	Created   int       `db:"created"`
	Completed int       `db:"completed"`
}

func (s *analyticsService) queryTimeline(ctx context.Context, filter AnalyticsFilter) ([]DayMetric, error) {
	var args []interface{}
	var projectFilter string
	if filter.ProjectID != nil {
		args = []interface{}{filter.WorkspaceID, *filter.ProjectID, filter.From, filter.To}
		projectFilter = "AND t.project_id = $2"
	} else {
		args = []interface{}{filter.WorkspaceID, filter.From, filter.To}
		projectFilter = ""
	}

	fromArgIdx := len(args) - 1
	toArgIdx := len(args)

	timelineQ := fmt.Sprintf(`
		SELECT
			day::date AS day,
			COALESCE(SUM(created), 0) AS created,
			COALESCE(SUM(completed), 0) AS completed
		FROM (
			SELECT date_trunc('day', t.created_at) AS day, 1 AS created, 0 AS completed
			FROM tasks t
			JOIN projects p ON p.id = t.project_id
			WHERE p.workspace_id = $1 %s
			AND t.created_at >= $%d AND t.created_at <= $%d
			UNION ALL
			SELECT date_trunc('day', t.completed_at) AS day, 0 AS created, 1 AS completed
			FROM tasks t
			JOIN projects p ON p.id = t.project_id
			WHERE p.workspace_id = $1 %s
			AND t.completed_at >= $%d AND t.completed_at <= $%d
		) sub
		GROUP BY day
		ORDER BY day ASC
	`, projectFilter, fromArgIdx, toArgIdx, projectFilter, fromArgIdx, toArgIdx)

	var rows []dayMetricRow
	if err := s.db.SelectContext(ctx, &rows, timelineQ, args...); err != nil {
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

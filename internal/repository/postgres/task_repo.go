package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// safeSlugRe validates that a custom field slug is a safe SQL identifier.
var safeSlugRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

var (
	hexPrefixRe = regexp.MustCompile(`^[0-9a-fA-F]{6,}$`)
	uuidRe      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// searchMode returns the detection mode for a search query: "uuid" for a full
// UUID, "prefix" for a hex-prefix short ID (≥6 hex chars, with optional leading
// #), or "text" for a plain text query. cleanInput has the leading # stripped
// and is lowercased for hex modes so it matches Postgres UUID::text output.
func searchMode(raw string) (mode, cleanInput string) {
	s := strings.TrimPrefix(strings.TrimSpace(raw), "#")
	if uuidRe.MatchString(s) {
		return "uuid", strings.ToLower(s)
	}
	if hexPrefixRe.MatchString(s) {
		return "prefix", strings.ToLower(s)
	}
	return "text", raw
}

// ErrCheckoutConflict is returned when AtomicCheckout finds the task is already
// checked out by a different agent and the lock has not yet expired.
var ErrCheckoutConflict = errors.New("task is already checked out")

// ErrInvalidCheckoutToken is returned when ReleaseCheckout or ExtendCheckout
// is called with a token that does not match the stored checkout_token.
var ErrInvalidCheckoutToken = errors.New("invalid checkout token")

// FalseOpenStaleDays is how many days an umbrella's own content (its
// updated_at, its newest comment) must have sat untouched before the
// false-open signal (domain.FalseOpenSignal, task #c80fe88f) is allowed to
// fire. Chosen at 14 days: long enough that a parent which simply hasn't
// caught up with subtasks closed minutes/hours ago never lights up, short
// enough to still catch the class of problem the signal exists for — #65dc5949
// sat 80 days, and task #c80fe88f's own live measurement found false-open
// umbrellas averaging 38-52 days idle (max 72) even without any staleness
// gate applied. 14 days is two work-weeks of margin under that curve.
const FalseOpenStaleDays = 14

// timeNow is overridable in tests so false-open staleness math is
// deterministic instead of racing wall-clock time.
var timeNow = time.Now

// taskEnrichedSelect is the SELECT clause used for all task queries that need
// computed fields. It appends 4 correlated subqueries to the base columns so
// that callers always receive subtask_count, assignee_name, artifact_count,
// and vcs_link_count without extra round-trips.
//
// The alias "t" must exist in the surrounding FROM clause (e.g. FROM tasks t
// or when tasks is the only table and no alias is used, replace "t." with "").
// We provide two variants below: one without a table alias (for plain
// FROM tasks queries) and one with "t." (for joined queries).
const taskBaseColsNoAlias = `
	id, project_id, status_id, title, description,
	assignee_id, assignee_type, priority, parent_task_id, position,
	due_date, estimated_hours, custom_fields, labels,
	task_number, created_by, created_by_type, created_at, updated_at,
	completed_at, deleted_at,
	recurring_schedule_id, recurring_instance_number,
	checked_out_by, checkout_token, checkout_expires, checkout_acquired_at,
	delegation_level, thread_id, human_gate, is_shipped, assigned_by, dod_checks,
	completion_signal, status_changed_at, pre_review_assignee_id, pre_review_assignee_type,
	reviewer_id, reviewer_type, human_gate_class, human_gate_armed_at,
	gate_author, gate_author_type, gate_reason, recommended_default, gate_deadline`

const taskComputedCols = `
	(SELECT COUNT(*) FROM tasks st WHERE st.parent_task_id = tasks.id AND st.deleted_at IS NULL) AS subtask_count,
	(SELECT COUNT(*) FROM artifacts a WHERE a.task_id = tasks.id) AS artifact_count,
	(SELECT COUNT(*) FROM vcs_links v WHERE v.task_id = tasks.id) AS vcs_link_count,
	` + falseOpenComputedColsNoAlias + `,
	CASE
		WHEN tasks.assignee_type = 'agent' THEN
			(SELECT name FROM agents WHERE id = tasks.assignee_id AND deleted_at IS NULL)
		WHEN tasks.assignee_type = 'user' THEN
			(SELECT display_name FROM users WHERE id = tasks.assignee_id)
		ELSE NULL
	END AS assignee_name,
	CASE
		WHEN tasks.reviewer_type = 'agent' THEN
			(SELECT name FROM agents WHERE id = tasks.reviewer_id AND deleted_at IS NULL)
		WHEN tasks.reviewer_type = 'user' THEN
			(SELECT display_name FROM users WHERE id = tasks.reviewer_id)
		ELSE NULL
	END AS reviewer_name,
	CASE
		WHEN tasks.created_by_type = 'agent' THEN
			(SELECT name FROM agents WHERE id = tasks.created_by AND deleted_at IS NULL)
		WHEN tasks.created_by_type = 'user' THEN
			(SELECT COALESCE(NULLIF(display_name,''), SPLIT_PART(email,'@',1)) FROM users WHERE id = tasks.created_by)
		ELSE NULL
	END AS created_by_name`

// falseOpenComputedColsNoAlias/falseOpenComputedColsAliased expose the raw
// per-row stats domain.FalseOpenSignal (task #c80fe88f) is built from — the
// AllChildrenClosed/OnlyParkedChildrenLeft threshold logic itself lives in Go
// (taskRow.toDomain, alongside the FalseOpenStaleDays constant) rather than
// duplicated across these two SQL variants, so there is exactly one place
// that decides what "false-open" means.
const falseOpenComputedColsNoAlias = `
	(SELECT COUNT(*) FROM tasks st JOIN task_statuses sts ON sts.id = st.status_id
		WHERE st.parent_task_id = tasks.id AND st.deleted_at IS NULL
		AND sts.category NOT IN ('done', 'cancelled')) AS false_open_children_count,
	(SELECT COUNT(*) FROM tasks st JOIN task_statuses sts ON sts.id = st.status_id
		WHERE st.parent_task_id = tasks.id AND st.deleted_at IS NULL
		AND sts.category NOT IN ('done', 'cancelled', 'backlog')) AS false_open_nonparked_children_count,
	(SELECT category FROM task_statuses WHERE id = tasks.status_id) AS false_open_own_category,
	GREATEST(
		tasks.updated_at,
		COALESCE((SELECT MAX(c.created_at) FROM comments c WHERE c.task_id = tasks.id), tasks.updated_at)
	) AS false_open_last_activity_at`

const falseOpenComputedColsAliased = `
	(SELECT COUNT(*) FROM tasks st JOIN task_statuses sts ON sts.id = st.status_id
		WHERE st.parent_task_id = t.id AND st.deleted_at IS NULL
		AND sts.category NOT IN ('done', 'cancelled')) AS false_open_children_count,
	(SELECT COUNT(*) FROM tasks st JOIN task_statuses sts ON sts.id = st.status_id
		WHERE st.parent_task_id = t.id AND st.deleted_at IS NULL
		AND sts.category NOT IN ('done', 'cancelled', 'backlog')) AS false_open_nonparked_children_count,
	(SELECT category FROM task_statuses WHERE id = t.status_id) AS false_open_own_category,
	GREATEST(
		t.updated_at,
		COALESCE((SELECT MAX(c.created_at) FROM comments c WHERE c.task_id = t.id), t.updated_at)
	) AS false_open_last_activity_at`

const taskComputedColsAliased = `
	(SELECT COUNT(*) FROM tasks st WHERE st.parent_task_id = t.id AND st.deleted_at IS NULL) AS subtask_count,
	(SELECT COUNT(*) FROM artifacts a WHERE a.task_id = t.id) AS artifact_count,
	(SELECT COUNT(*) FROM vcs_links v WHERE v.task_id = t.id) AS vcs_link_count,
	` + falseOpenComputedColsAliased + `,
	CASE
		WHEN t.assignee_type = 'agent' THEN
			(SELECT name FROM agents WHERE id = t.assignee_id AND deleted_at IS NULL)
		WHEN t.assignee_type = 'user' THEN
			(SELECT display_name FROM users WHERE id = t.assignee_id)
		ELSE NULL
	END AS assignee_name,
	CASE
		WHEN t.reviewer_type = 'agent' THEN
			(SELECT name FROM agents WHERE id = t.reviewer_id AND deleted_at IS NULL)
		WHEN t.reviewer_type = 'user' THEN
			(SELECT display_name FROM users WHERE id = t.reviewer_id)
		ELSE NULL
	END AS reviewer_name,
	CASE
		WHEN t.created_by_type = 'agent' THEN
			(SELECT name FROM agents WHERE id = t.created_by AND deleted_at IS NULL)
		WHEN t.created_by_type = 'user' THEN
			(SELECT COALESCE(NULLIF(display_name,''), SPLIT_PART(email,'@',1)) FROM users WHERE id = t.created_by)
		ELSE NULL
	END AS created_by_name`

// taskRow is the DB row representation (includes task_number and deleted_at
// that the domain model does not have, plus 4 computed enrichment fields).
type taskRow struct {
	ID             uuid.UUID           `db:"id"`
	ProjectID      uuid.UUID           `db:"project_id"`
	StatusID       uuid.UUID           `db:"status_id"`
	Title          string              `db:"title"`
	Description    string              `db:"description"`
	AssigneeID     *uuid.UUID          `db:"assignee_id"`
	AssigneeType   domain.AssigneeType `db:"assignee_type"`
	Priority       domain.Priority     `db:"priority"`
	ParentTaskID   *uuid.UUID          `db:"parent_task_id"`
	Position       float64             `db:"position"`
	DueDate        *time.Time          `db:"due_date"`
	EstimatedHours *float64            `db:"estimated_hours"`
	CustomFields   json.RawMessage     `db:"custom_fields"`
	Labels         pq.StringArray      `db:"labels"`
	TaskNumber     int                 `db:"task_number"`
	CreatedBy      uuid.UUID           `db:"created_by"`
	CreatedByType  domain.ActorType    `db:"created_by_type"`
	CreatedAt      time.Time           `db:"created_at"`
	UpdatedAt      time.Time           `db:"updated_at"`
	CompletedAt    *time.Time          `db:"completed_at"`
	DeletedAt      *time.Time          `db:"deleted_at"`

	// Recurring series fields.
	RecurringScheduleID     *uuid.UUID `db:"recurring_schedule_id"`
	RecurringInstanceNumber *int       `db:"recurring_instance_number"`

	DelegationLevel  domain.DelegationLevel `db:"delegation_level"`
	ThreadID         *string                `db:"thread_id"`
	HumanGate        bool                   `db:"human_gate"`
	HumanGateClass   domain.HumanGateClass  `db:"human_gate_class"`
	HumanGateArmedAt *time.Time             `db:"human_gate_armed_at"`

	// Gate authorship + default (task #4545660b) — see domain.Task doc comment.
	GateAuthor         *uuid.UUID        `db:"gate_author"`
	GateAuthorType     *domain.ActorType `db:"gate_author_type"`
	GateReason         *string           `db:"gate_reason"`
	RecommendedDefault *string           `db:"recommended_default"`
	GateDeadline       *time.Time        `db:"gate_deadline"`

	IsShipped        bool                    `db:"is_shipped"`
	AssignedBy       domain.AssignmentSource `db:"assigned_by"`
	DodChecks        domain.DodChecks        `db:"dod_checks"`
	CompletionSignal bool                    `db:"completion_signal"`
	StatusChangedAt  *time.Time              `db:"status_changed_at"`

	// Pre-review assignee stash — see domain.Task doc comment.
	PreReviewAssigneeID   *uuid.UUID           `db:"pre_review_assignee_id"`
	PreReviewAssigneeType *domain.AssigneeType `db:"pre_review_assignee_type"`

	// Reviewer — see domain.Task doc comment.
	ReviewerID   *uuid.UUID           `db:"reviewer_id"`
	ReviewerType *domain.AssigneeType `db:"reviewer_type"`

	// Checkout fields.
	CheckedOutBy       *uuid.UUID `db:"checked_out_by"`
	CheckoutToken      *uuid.UUID `db:"checkout_token"`
	CheckoutExpires    *time.Time `db:"checkout_expires"`
	CheckoutAcquiredAt *time.Time `db:"checkout_acquired_at"`

	// Computed enrichment fields populated by enriched queries.
	SubtaskCount  int     `db:"subtask_count"`
	AssigneeName  *string `db:"assignee_name"`
	ReviewerName  *string `db:"reviewer_name"`
	CreatedByName *string `db:"created_by_name"`
	ArtifactCount int     `db:"artifact_count"`
	VCSLinkCount  int     `db:"vcs_link_count"`

	// False-open raw stats (task #c80fe88f) — see falseOpenComputedCols*
	// and toDomain's threshold logic below.
	FalseOpenChildrenCount          int                   `db:"false_open_children_count"`
	FalseOpenNonparkedChildrenCount int                   `db:"false_open_nonparked_children_count"`
	FalseOpenOwnCategory            domain.StatusCategory `db:"false_open_own_category"`
	FalseOpenLastActivityAt         time.Time             `db:"false_open_last_activity_at"`
}

func (r *taskRow) toDomain() domain.Task {
	return domain.Task{
		ID:                      r.ID,
		ProjectID:               r.ProjectID,
		StatusID:                r.StatusID,
		Title:                   r.Title,
		Description:             r.Description,
		AssigneeID:              r.AssigneeID,
		AssigneeType:            r.AssigneeType,
		Priority:                r.Priority,
		ParentTaskID:            r.ParentTaskID,
		Position:                r.Position,
		DueDate:                 r.DueDate,
		EstimatedHours:          r.EstimatedHours,
		CustomFields:            r.CustomFields,
		Labels:                  r.Labels,
		CreatedBy:               r.CreatedBy,
		CreatedByType:           r.CreatedByType,
		CreatedAt:               r.CreatedAt,
		UpdatedAt:               r.UpdatedAt,
		CompletedAt:             r.CompletedAt,
		RecurringScheduleID:     r.RecurringScheduleID,
		RecurringInstanceNumber: r.RecurringInstanceNumber,
		DelegationLevel:         r.DelegationLevel,
		ThreadID:                r.ThreadID,
		HumanGate:               r.HumanGate,
		HumanGateClass:          r.HumanGateClass,
		HumanGateArmedAt:        r.HumanGateArmedAt,
		GateAuthor:              r.GateAuthor,
		GateAuthorType:          r.GateAuthorType,
		GateReason:              r.GateReason,
		RecommendedDefault:      r.RecommendedDefault,
		GateDeadline:            r.GateDeadline,
		IsShipped:               r.IsShipped,
		AssignedBy:              r.AssignedBy,
		DodChecks:               r.DodChecks,
		CompletionSignal:        r.CompletionSignal,
		StatusChangedAt:         r.StatusChangedAt,
		PreReviewAssigneeID:     r.PreReviewAssigneeID,
		PreReviewAssigneeType:   r.PreReviewAssigneeType,
		ReviewerID:              r.ReviewerID,
		ReviewerType:            r.ReviewerType,
		CheckedOutBy:            r.CheckedOutBy,
		CheckoutToken:           r.CheckoutToken,
		CheckoutExpires:         r.CheckoutExpires,
		CheckoutAcquiredAt:      r.CheckoutAcquiredAt,
		SubtaskCount:            r.SubtaskCount,
		AssigneeName:            r.AssigneeName,
		ReviewerName:            r.ReviewerName,
		CreatedByName:           r.CreatedByName,
		ArtifactCount:           r.ArtifactCount,
		VCSLinkCount:            r.VCSLinkCount,
		FalseOpen:               r.falseOpenSignal(),
	}
}

// falseOpenSignal builds domain.FalseOpenSignal from the raw stats the
// false_open_* computed columns fetched, or nil if this row isn't a
// candidate at all: a leaf task (no subtasks) or one whose own status is
// already terminal has nothing to say here. AllChildrenClosed and
// OnlyParkedChildrenLeft are mutually exclusive and both gated on
// FalseOpenStaleDays — see domain.FalseOpenSignal and the const's doc for
// why (task #c80fe88f review comment 2026-08-20: merging the two into one
// flag would equally catch umbrellas correctly blocked on a live parked
// dependency, e.g. #65dc5949 on c0afa1c5).
func (r *taskRow) falseOpenSignal() *domain.FalseOpenSignal {
	if r.SubtaskCount == 0 {
		return nil
	}
	if r.FalseOpenOwnCategory == domain.StatusCategoryDone || r.FalseOpenOwnCategory == domain.StatusCategoryCancelled {
		return nil
	}
	staleDays := int(timeNow().Sub(r.FalseOpenLastActivityAt).Hours() / 24)
	if staleDays < 0 {
		staleDays = 0
	}
	stale := staleDays >= FalseOpenStaleDays
	return &domain.FalseOpenSignal{
		AllChildrenClosed:      stale && r.FalseOpenChildrenCount == 0,
		OnlyParkedChildrenLeft: stale && r.FalseOpenChildrenCount > 0 && r.FalseOpenNonparkedChildrenCount == 0,
		OpenChildrenCount:      r.FalseOpenChildrenCount,
		StaleDays:              staleDays,
	}
}

func taskRowsToSlice(rows []taskRow) []domain.Task {
	result := make([]domain.Task, len(rows))
	for i := range rows {
		result[i] = rows[i].toDomain()
	}
	return result
}

// TaskRepo implements repository.TaskRepository with PostgreSQL.
type TaskRepo struct {
	db *sqlx.DB
}

// NewTaskRepo creates a new TaskRepo.
func NewTaskRepo(db *sqlx.DB) *TaskRepo {
	return &TaskRepo{db: db}
}

// isTaskNumberConflict reports whether err is a PostgreSQL unique-constraint
// violation on uq_tasks_project_number (project_id, task_number). This happens
// when two concurrent paths compute MAX(task_number)+1 simultaneously.
func isTaskNumberConflict(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505" && pqErr.Constraint == "uq_tasks_project_number"
}

func (r *TaskRepo) Create(ctx context.Context, task *domain.Task) error {
	// Serialize task_number allocation per project using pg_advisory_xact_lock.
	//
	// IMPORTANT: the lock MUST be acquired in a separate statement (Statement 1) before
	// the INSERT (Statement 2). PostgreSQL READ COMMITTED takes the snapshot for each
	// statement at statement start — not when the lock is acquired. If the lock and the
	// MAX(task_number) read were in the same statement (single CTE approach), a goroutine
	// that started its statement before a concurrent INSERT committed would see a stale
	// snapshot and compute a duplicate task_number despite holding the lock.
	//
	// By splitting into two statements inside an explicit transaction:
	//   Stmt 1: SELECT pg_advisory_xact_lock(...)  → blocks until prior holder commits
	//   Stmt 2: INSERT ... (SELECT MAX(task_number)+1 ...)  → fresh snapshot, sees all prior commits
	// the MAX read is guaranteed to see the most recent committed task_number.
	const qLock = `SELECT pg_advisory_xact_lock(hashtext($1::text))`
	const qInsert = `
		INSERT INTO tasks (
			id, project_id, status_id, title, description,
			assignee_id, assignee_type, priority, parent_task_id, position,
			due_date, estimated_hours, custom_fields, labels,
			task_number, created_by, created_by_type, created_at, updated_at, completed_at,
			recurring_schedule_id, recurring_instance_number,
			delegation_level, thread_id, human_gate, is_shipped, assigned_by, completion_signal, status_changed_at,
			reviewer_id, reviewer_type
		) VALUES (
			$1, $2::uuid, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12, $13, $14,
			(SELECT COALESCE(MAX(task_number), 0) + 1 FROM tasks WHERE project_id = $2::uuid),
			$15, $16, $17, $18, $19,
			$20, $21,
			$22, $23, $24, $25, $26, $27, NOW(),
			$28, $29
		)
	`
	customFields := task.CustomFields
	if customFields == nil {
		customFields = json.RawMessage(`{}`)
	}
	labels := task.Labels
	if labels == nil {
		labels = pq.StringArray{}
	}
	delegationLevel := task.DelegationLevel
	if delegationLevel == "" {
		delegationLevel = domain.DelegationLevelAuto
	}

	const maxRetries = 3
	for attempt := range maxRetries {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*50) * time.Millisecond):
			}
		}
		dbStart := time.Now()
		err := func() error {
			tx, err := r.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer func() {
				if err != nil {
					_ = tx.Rollback()
				}
			}()
			// Statement 1: acquire the advisory lock; blocks until any concurrent holder
			// for this project_id commits, ensuring the next statement's snapshot is fresh.
			if _, err = tx.ExecContext(ctx, qLock, task.ProjectID); err != nil {
				return err
			}
			// Statement 2: INSERT with fresh READ COMMITTED snapshot (taken now, after
			// the lock was granted and all prior concurrent inserts have committed).
			_, err = tx.ExecContext(ctx, qInsert,
				task.ID, task.ProjectID, task.StatusID, task.Title, task.Description,
				task.AssigneeID, task.AssigneeType, task.Priority, task.ParentTaskID, task.Position,
				task.DueDate, task.EstimatedHours, customFields, labels,
				task.CreatedBy, task.CreatedByType, task.CreatedAt, task.UpdatedAt, task.CompletedAt,
				task.RecurringScheduleID, task.RecurringInstanceNumber,
				delegationLevel, task.ThreadID, task.HumanGate, task.IsShipped, task.AssignedBy, task.CompletionSignal,
				task.ReviewerID, task.ReviewerType,
			)
			if err != nil {
				return err
			}
			return tx.Commit()
		}()
		pkgmetrics.RecordDBQuery("task.create", time.Since(dbStart))
		if err == nil {
			return nil
		}
		if isTaskNumberConflict(err) {
			continue
		}
		return err
	}
	return fmt.Errorf("task_repo.Create: task_number conflict persisted after %d retries for project %s", maxRetries, task.ProjectID)
}

func (r *TaskRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	q := `SELECT ` + taskBaseColsNoAlias + `, ` + taskComputedCols + `
		FROM tasks WHERE id = $1 AND deleted_at IS NULL`
	var row taskRow
	dbStart := time.Now()
	err := r.db.GetContext(ctx, &row, q, id)
	pkgmetrics.RecordDBQuery("task.get", time.Since(dbStart))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	t := row.toDomain()
	return &t, nil
}

// GetByShortID resolves a task by UUID hex prefix (6–12 chars). Returns apierror.NotFound
// if no task matches, apierror.BadRequest if multiple tasks share the prefix.
func (r *TaskRepo) GetByShortID(ctx context.Context, prefix string) (*domain.Task, error) {
	q := `SELECT ` + taskBaseColsNoAlias + `, ` + taskComputedCols + `
		FROM tasks WHERE id::text LIKE $1 AND deleted_at IS NULL LIMIT 2`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, q, prefix+"%"); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, apierror.NotFound("Task")
	}
	if len(rows) > 1 {
		return nil, apierror.BadRequest("ambiguous short ID: multiple tasks match")
	}
	t := rows[0].toDomain()
	return &t, nil
}

// Search searches tasks across all projects in a workspace by text query and optional filters.
func (r *TaskRepo) Search(ctx context.Context, workspaceID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()

	args := []interface{}{workspaceID} // $1
	conditions := []string{"p.workspace_id = $1", "t.deleted_at IS NULL", "p.deleted_at IS NULL"}
	argIdx := 2

	if filter.Search != "" {
		mode, clean := searchMode(filter.Search)
		switch mode {
		case "uuid":
			parsed, err := uuid.Parse(clean)
			if err == nil {
				conditions = append(conditions, fmt.Sprintf("t.id = $%d", argIdx))
				args = append(args, parsed)
				argIdx++
			}
		case "prefix":
			conditions = append(conditions, fmt.Sprintf("t.id::text LIKE $%d", argIdx))
			args = append(args, clean+"%")
			argIdx++
		default:
			pattern := "%" + filter.Search + "%"
			conditions = append(conditions, fmt.Sprintf("(t.title ILIKE $%d OR t.description ILIKE $%d)", argIdx, argIdx))
			args = append(args, pattern)
			argIdx++
		}
	}
	if len(filter.StatusIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("t.status_id = ANY($%d)", argIdx))
		args = append(args, pq.Array(filter.StatusIDs))
		argIdx++
	}
	if filter.AssigneeID != nil {
		conditions = append(conditions, fmt.Sprintf("t.assignee_id = $%d", argIdx))
		args = append(args, *filter.AssigneeID)
		argIdx++
	}
	if filter.Priority != nil {
		conditions = append(conditions, fmt.Sprintf("t.priority = $%d", argIdx))
		args = append(args, *filter.Priority)
		argIdx++
	}

	where := "WHERE " + joinAnd(conditions)

	countQ := fmt.Sprintf(`SELECT COUNT(t.id) FROM tasks t INNER JOIN projects p ON p.id = t.project_id %s`, where)
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, args...); err != nil {
		return nil, err
	}

	dataQ := fmt.Sprintf(`SELECT t.id, t.project_id, t.status_id, t.title, t.description,
		t.assignee_id, t.assignee_type, t.priority, t.parent_task_id, t.position,
		t.due_date, t.estimated_hours, t.custom_fields, t.labels,
		t.task_number, t.created_by, t.created_by_type, t.created_at, t.updated_at,
		t.completed_at, t.deleted_at,
		t.recurring_schedule_id, t.recurring_instance_number, `+taskComputedColsAliased+`
		FROM tasks t INNER JOIN projects p ON p.id = t.project_id
		%s ORDER BY t.updated_at DESC, t.id ASC LIMIT $%d OFFSET $%d`, where, argIdx, argIdx+1)
	args = append(args, pg.Limit(), pg.Offset())
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, args...); err != nil {
		return nil, err
	}

	return pagination.NewPage(taskRowsToSlice(rows), totalCount, pg), nil
}

func (r *TaskRepo) Update(ctx context.Context, task *domain.Task) error {
	const q = `
		UPDATE tasks
		SET status_id = $2, title = $3, description = $4,
		    assignee_id = $5, assignee_type = $6, priority = $7,
		    parent_task_id = $8, position = $9, due_date = $10,
		    estimated_hours = $11, custom_fields = $12, labels = $13,
		    updated_at = $14, completed_at = $15,
		    recurring_schedule_id = $16, recurring_instance_number = $17,
		    delegation_level = $18, thread_id = $19,
		    human_gate = $20, is_shipped = $21, assigned_by = $22,
		    completion_signal = $23, status_changed_at = $24,
		    pre_review_assignee_id = $25, pre_review_assignee_type = $26,
		    reviewer_id = $27, reviewer_type = $28
		WHERE id = $1 AND deleted_at IS NULL
	`
	customFields := task.CustomFields
	if customFields == nil {
		customFields = json.RawMessage(`{}`)
	}
	labels := task.Labels
	if labels == nil {
		labels = pq.StringArray{}
	}
	delegationLevel := task.DelegationLevel
	if delegationLevel == "" {
		delegationLevel = domain.DelegationLevelAuto
	}
	dbStart := time.Now()
	res, err := r.db.ExecContext(ctx, q,
		task.ID, task.StatusID, task.Title, task.Description,
		task.AssigneeID, task.AssigneeType, task.Priority,
		task.ParentTaskID, task.Position, task.DueDate,
		task.EstimatedHours, customFields, labels,
		task.UpdatedAt, task.CompletedAt,
		task.RecurringScheduleID, task.RecurringInstanceNumber,
		delegationLevel, task.ThreadID,
		task.HumanGate, task.IsShipped, task.AssignedBy, task.CompletionSignal, task.StatusChangedAt,
		task.PreReviewAssigneeID, task.PreReviewAssigneeType,
		task.ReviewerID, task.ReviewerType,
	)
	pkgmetrics.RecordDBQuery("task.update", time.Since(dbStart))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Task")
	}
	return nil
}

// ArmHumanGate atomically arms the gate AND writes the whole "waiting on a human"
// answer in one statement (task #4545660b): who asked, what they asked, what happens by
// default and by when. One statement rather than a SetHumanGate + SetHumanGateClass +
// three more calls, because a partially-armed gate is exactly the state every client
// used to paper over by going back to the comment text.
//
// human_gate_armed_at is stamped only on a false->true transition. Re-arming an already
// armed gate refreshes the ask (a later marker supersedes an earlier one, matching how
// SetHumanGateClass already lets a hard marker downgrade a soft one) but must NOT reset
// the age the soft-timeout sweep measures — otherwise a driver's repeat ping would keep
// a soft gate alive forever, the latch shape of #84ab54fd.
func (r *TaskRepo) ArmHumanGate(ctx context.Context, in domain.ArmHumanGateInput) error {
	const q = `
		UPDATE tasks SET
			human_gate = true,
			human_gate_class = $2,
			human_gate_armed_at = CASE WHEN human_gate THEN human_gate_armed_at ELSE NOW() END,
			gate_author = $3,
			gate_author_type = $4,
			gate_reason = NULLIF($5, ''),
			recommended_default = NULLIF($6, ''),
			gate_deadline = $7,
			updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`
	dbStart := time.Now()
	res, err := r.db.ExecContext(ctx, q,
		in.TaskID, in.Class, in.Author, string(in.AuthorType),
		in.Reason, in.RecommendedDefault, in.Deadline,
	)
	pkgmetrics.RecordDBQuery("task.arm_human_gate", time.Since(dbStart))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Task")
	}
	return nil
}

// SetHumanGate atomically sets the human_gate column without touching other task fields.
// Arming (value=true) also stamps human_gate_armed_at = NOW() and leaves
// human_gate_class as-is (default 'hard' unless SetHumanGateClass overrode it before this
// call). Clearing (value=false) resets human_gate_class back to 'hard' — fail-closed, so a
// soft classification never survives past the ask it was set for.
//
// Clearing ALSO nulls the four authorship/default columns ArmHumanGate writes. They
// describe a LIVE ask; leaving them behind on a released gate would leave a
// recommended_default sitting on a task that no longer has a question, which is exactly
// the kind of residue every reader would have to learn to ignore. History is not lost —
// it stays in the marker comment and in human_gate_decisions.
//
// NOTE this is the legacy bool-shaped entry point, kept because the release paths
// (releaseHumanGate, withdrawal, soft-timeout sweep, PATCH) only ever pass false. Arming
// through it is still possible and still leaves the gate authorless; new arm callers must
// use ArmHumanGate.
func (r *TaskRepo) SetHumanGate(ctx context.Context, taskID uuid.UUID, value bool) error {
	const q = `
		UPDATE tasks SET
			human_gate = $2,
			human_gate_class = CASE WHEN $2 THEN human_gate_class ELSE 'hard' END,
			human_gate_armed_at = CASE WHEN $2 THEN NOW() ELSE human_gate_armed_at END,
			gate_author = CASE WHEN $2 THEN gate_author ELSE NULL END,
			gate_author_type = CASE WHEN $2 THEN gate_author_type ELSE NULL END,
			gate_reason = CASE WHEN $2 THEN gate_reason ELSE NULL END,
			recommended_default = CASE WHEN $2 THEN recommended_default ELSE NULL END,
			gate_deadline = CASE WHEN $2 THEN gate_deadline ELSE NULL END,
			updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`
	dbStart := time.Now()
	res, err := r.db.ExecContext(ctx, q, taskID, value)
	pkgmetrics.RecordDBQuery("task.set_human_gate", time.Since(dbStart))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Task")
	}
	return nil
}

// SetHumanGateClass atomically sets the human_gate_class column without touching other
// task fields. See domain.HumanGateClass and interfaces.go for the contract.
func (r *TaskRepo) SetHumanGateClass(ctx context.Context, taskID uuid.UUID, class domain.HumanGateClass) error {
	const q = `UPDATE tasks SET human_gate_class = $2, updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`
	dbStart := time.Now()
	res, err := r.db.ExecContext(ctx, q, taskID, string(class))
	pkgmetrics.RecordDBQuery("task.set_human_gate_class", time.Since(dbStart))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Task")
	}
	return nil
}

// FindSoftTimedOutGates returns armed, soft-classified gates armed at or before cutoff.
//
// The `human_gate_class = 'soft'` predicate is a fixed literal in this query text, not
// derived from cutoff or any other argument — that is the structural proof required by
// contract §5.1: a hard-classified row is unreachable from this function regardless of
// what cutoff is passed, including an arbitrarily far-future one. See
// task_repo_human_gate_class_db_test.go for the live negative control (a hard fixture
// armed decades ago, queried with a decades-future cutoff, still absent from the result).
func (r *TaskRepo) FindSoftTimedOutGates(ctx context.Context, cutoff time.Time) ([]domain.HumanGateSoftTimeoutCandidate, error) {
	const q = `
		SELECT id, human_gate_armed_at
		FROM tasks
		WHERE deleted_at IS NULL
		  AND human_gate = true
		  AND human_gate_class = 'soft'
		  AND human_gate_armed_at IS NOT NULL
		  AND human_gate_armed_at <= $1`
	type row struct {
		ID               uuid.UUID    `db:"id"`
		HumanGateArmedAt sql.NullTime `db:"human_gate_armed_at"`
	}
	var rows []row
	dbStart := time.Now()
	err := r.db.SelectContext(ctx, &rows, q, cutoff)
	pkgmetrics.RecordDBQuery("task.find_soft_timed_out_gates", time.Since(dbStart))
	if err != nil {
		return nil, err
	}
	out := make([]domain.HumanGateSoftTimeoutCandidate, 0, len(rows))
	for _, rw := range rows {
		if !rw.HumanGateArmedAt.Valid {
			continue
		}
		out = append(out, domain.HumanGateSoftTimeoutCandidate{
			TaskID:  rw.ID,
			ArmedAt: rw.HumanGateArmedAt.Time,
		})
	}
	return out, nil
}

// SetShipped atomically sets the is_shipped column without touching other task fields.
func (r *TaskRepo) SetShipped(ctx context.Context, taskID uuid.UUID, value bool) error {
	const q = `UPDATE tasks SET is_shipped = $2, updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`
	dbStart := time.Now()
	res, err := r.db.ExecContext(ctx, q, taskID, value)
	pkgmetrics.RecordDBQuery("task.set_shipped", time.Since(dbStart))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Task")
	}
	return nil
}

// SetDodCheck atomically upserts a single named gate entry in the dod_checks JSONB column.
// The status must be one of: pending, pass, fail.
func (r *TaskRepo) SetDodCheck(ctx context.Context, taskID uuid.UUID, gateName, status, reporter string) error {
	const q = `
		UPDATE tasks
		SET dod_checks = jsonb_set(
			COALESCE(dod_checks, '{}'),
			ARRAY[$2::text],
			jsonb_build_object(
				'status',     $3::text,
				'updated_at', NOW()::text,
				'reporter',   $4::text
			),
			true
		),
		updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`
	dbStart := time.Now()
	res, err := r.db.ExecContext(ctx, q, taskID, gateName, status, reporter)
	pkgmetrics.RecordDBQuery("task.set_dod_check", time.Since(dbStart))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Task")
	}
	return nil
}

// Delete performs a soft delete by setting deleted_at.
func (r *TaskRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE tasks SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`
	dbStart := time.Now()
	res, err := r.db.ExecContext(ctx, q, id)
	pkgmetrics.RecordDBQuery("task.delete", time.Since(dbStart))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Task")
	}
	return nil
}

func (r *TaskRepo) List(ctx context.Context, projectID uuid.UUID, filter repository.TaskFilter, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()

	args := []interface{}{projectID} // $1
	conditions := []string{"project_id = $1", "deleted_at IS NULL"}
	argIdx := 2

	if len(filter.StatusIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("status_id = ANY($%d)", argIdx))
		args = append(args, pq.Array(filter.StatusIDs))
		argIdx++
	}
	if filter.StatusCategory != nil {
		// Subquery avoids a JOIN while staying composable with other conditions.
		conditions = append(conditions, fmt.Sprintf(
			"status_id IN (SELECT id FROM task_statuses WHERE project_id = $1 AND category = $%d)", argIdx))
		args = append(args, *filter.StatusCategory)
		argIdx++
	}
	if filter.AssigneeID != nil {
		conditions = append(conditions, fmt.Sprintf("assignee_id = $%d", argIdx))
		args = append(args, *filter.AssigneeID)
		argIdx++
	}
	if filter.AssigneeType != nil {
		conditions = append(conditions, fmt.Sprintf("assignee_type = $%d", argIdx))
		args = append(args, *filter.AssigneeType)
		argIdx++
	}
	if filter.Priority != nil {
		conditions = append(conditions, fmt.Sprintf("priority = $%d", argIdx))
		args = append(args, *filter.Priority)
		argIdx++
	}
	if filter.ParentTaskID != nil {
		conditions = append(conditions, fmt.Sprintf("parent_task_id = $%d", argIdx))
		args = append(args, *filter.ParentTaskID)
		argIdx++
	}
	if len(filter.Labels) > 0 {
		conditions = append(conditions, fmt.Sprintf("labels && $%d", argIdx))
		args = append(args, pq.Array(filter.Labels))
		argIdx++
	}
	if filter.Search != "" {
		mode, clean := searchMode(filter.Search)
		switch mode {
		case "uuid":
			parsed, err := uuid.Parse(clean)
			if err == nil {
				conditions = append(conditions, fmt.Sprintf("id = $%d", argIdx))
				args = append(args, parsed)
				argIdx++
			}
		case "prefix":
			conditions = append(conditions, fmt.Sprintf("id::text LIKE $%d", argIdx))
			args = append(args, clean+"%")
			argIdx++
		default:
			pattern := "%" + filter.Search + "%"
			conditions = append(conditions, fmt.Sprintf("(title ILIKE $%d OR description ILIKE $%d)", argIdx, argIdx))
			args = append(args, pattern)
			argIdx++
		}
	}
	if filter.HasDueDate != nil {
		if *filter.HasDueDate {
			conditions = append(conditions, "due_date IS NOT NULL")
		} else {
			conditions = append(conditions, "due_date IS NULL")
		}
	}
	if filter.HumanGate != nil {
		conditions = append(conditions, fmt.Sprintf("human_gate = $%d", argIdx))
		args = append(args, *filter.HumanGate)
		argIdx++
	}

	// Custom field JSONB filters.
	for slug, cf := range filter.CustomFields {
		if !safeSlugRe.MatchString(slug) {
			continue // skip unsafe slugs — defense in depth
		}
		if cf.Eq != nil {
			conditions = append(conditions, fmt.Sprintf("custom_fields->>'%s' = $%d", slug, argIdx))
			args = append(args, fmt.Sprintf("%v", cf.Eq))
			argIdx++
		}
		if cf.Gte != nil {
			conditions = append(conditions, fmt.Sprintf("(custom_fields->>'%s')::numeric >= $%d", slug, argIdx))
			args = append(args, *cf.Gte)
			argIdx++
		}
		if cf.Lte != nil {
			conditions = append(conditions, fmt.Sprintf("(custom_fields->>'%s')::numeric <= $%d", slug, argIdx))
			args = append(args, *cf.Lte)
			argIdx++
		}
		if len(cf.In) > 0 {
			conditions = append(conditions, fmt.Sprintf("custom_fields->>'%s' = ANY($%d)", slug, argIdx))
			args = append(args, pq.Array(cf.In))
			argIdx++
		}
		if cf.IsSet != nil {
			if *cf.IsSet {
				conditions = append(conditions, fmt.Sprintf("custom_fields ? '%s'", slug))
			} else {
				conditions = append(conditions, fmt.Sprintf("NOT (custom_fields ? '%s')", slug))
			}
		}
	}

	where := "WHERE " + joinAnd(conditions)
	// Default to updated_at DESC so any task mutation (create/update/move) floats
	// the task to the top of the first page — the "write-through" effect.
	// Callers may override via explicit sort_by/sort_dir query params.
	if pg.SortBy == "" {
		pg.SortBy = "updated_at"
		pg.SortDir = "desc"
	}
	order := orderClause(pg, allowedSortColumns{
		"title":      "title",
		"priority":   "priority",
		"position":   "position",
		"created_at": "created_at",
		"updated_at": "updated_at",
		"due_date":   "due_date",
	}, "updated_at") + ", tasks.id ASC"

	// Count
	countQ := fmt.Sprintf(`SELECT COUNT(*) FROM tasks %s`, where)
	var totalCount int
	dbStart := time.Now()
	if err := r.db.GetContext(ctx, &totalCount, countQ, args...); err != nil {
		pkgmetrics.RecordDBQuery("task.list_count", time.Since(dbStart))
		return nil, err
	}
	pkgmetrics.RecordDBQuery("task.list_count", time.Since(dbStart))

	// Data — use enriched select to populate computed fields in a single query.
	dataQ := fmt.Sprintf(`SELECT `+taskBaseColsNoAlias+`, `+taskComputedCols+` FROM tasks %s %s %s`, where, order, paginationClause(pg))
	var rows []taskRow
	dbStart = time.Now()
	if err := r.db.SelectContext(ctx, &rows, dataQ, args...); err != nil {
		pkgmetrics.RecordDBQuery("task.list", time.Since(dbStart))
		return nil, err
	}
	pkgmetrics.RecordDBQuery("task.list", time.Since(dbStart))

	return pagination.NewPage(taskRowsToSlice(rows), totalCount, pg), nil
}

// ListByAssignee returns the principal's tasks WITHIN one workspace.
//
// The workspace predicate is not optional and not a convenience filter. This
// query backs GET /agents/me/tasks and its long-poll twin, which are how an agent
// discovers work; without it the query returned every task carrying the
// principal's id regardless of tenant, so an assignment made from another
// workspace showed up in the agent's own feed and the notify path then posted the
// task snapshot to that agent's callback_url. Scoping the write path alone would
// have left every row already written still readable.
// filter narrows the feed IN SQL. Every predicate here used to be applied in Go
// after the full row set had already been read and marshalled — see
// repository.AssigneeTaskFilter for why that is not a detail.
//
// The returned total is the count BEFORE the limit, so a caller that asked for a
// page can tell a complete answer from a truncated one. It costs one extra
// COUNT query and is therefore only run when a limit was actually requested.
func (r *TaskRepo) ListByAssignee(
	ctx context.Context,
	workspaceID, assigneeID uuid.UUID,
	assigneeType domain.AssigneeType,
	filter repository.AssigneeTaskFilter,
) ([]domain.Task, int, error) {
	conditions := []string{
		"assignee_id = $1",
		"assignee_type = $2",
		"deleted_at IS NULL",
		"project_id IN (SELECT id FROM projects WHERE workspace_id = $3)",
	}
	args := []any{assigneeID, assigneeType, workspaceID}

	if filter.ProjectID != nil {
		args = append(args, *filter.ProjectID)
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", len(args)))
	}
	if filter.StatusCategory != nil {
		// status_id identifies one (project, status) row, so a global lookup by
		// category needs no project predicate of its own.
		args = append(args, string(*filter.StatusCategory))
		conditions = append(conditions,
			fmt.Sprintf("status_id IN (SELECT id FROM task_statuses WHERE category = $%d)", len(args)))
	}

	where := "WHERE " + joinAnd(conditions)

	total := 0
	if filter.Limit > 0 {
		dbStart := time.Now()
		if err := r.db.GetContext(ctx, &total,
			`SELECT COUNT(*) FROM tasks `+where, args...); err != nil {
			pkgmetrics.RecordDBQuery("task.list_by_assignee_count", time.Since(dbStart))
			return nil, 0, err
		}
		pkgmetrics.RecordDBQuery("task.list_by_assignee_count", time.Since(dbStart))
	}

	q := `SELECT ` + taskBaseColsNoAlias + `, ` + taskComputedCols + `
		FROM tasks ` + where + `
		ORDER BY created_at ASC`
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}

	var rows []taskRow
	dbStart := time.Now()
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		pkgmetrics.RecordDBQuery("task.list_by_assignee", time.Since(dbStart))
		return nil, 0, err
	}
	pkgmetrics.RecordDBQuery("task.list_by_assignee", time.Since(dbStart))

	tasks := taskRowsToSlice(rows)
	if filter.Limit <= 0 {
		total = len(tasks)
	}
	return tasks, total, nil
}

func (r *TaskRepo) ListSubtasks(ctx context.Context, parentTaskID uuid.UUID) ([]domain.Task, error) {
	q := `SELECT ` + taskBaseColsNoAlias + `, ` + taskComputedCols + `
		FROM tasks
		WHERE parent_task_id = $1 AND deleted_at IS NULL
		ORDER BY position ASC, created_at ASC`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, q, parentTaskID); err != nil {
		return nil, err
	}
	return taskRowsToSlice(rows), nil
}

func (r *TaskRepo) CountByStatus(ctx context.Context, projectID uuid.UUID) (map[uuid.UUID]int, error) {
	const q = `
		SELECT status_id, COUNT(*) as cnt
		FROM tasks
		WHERE project_id = $1 AND deleted_at IS NULL
		GROUP BY status_id
	`
	type row struct {
		StatusID uuid.UUID `db:"status_id"`
		Cnt      int       `db:"cnt"`
	}
	var rows []row
	if err := r.db.SelectContext(ctx, &rows, q, projectID); err != nil {
		return nil, err
	}
	result := make(map[uuid.UUID]int, len(rows))
	for _, r := range rows {
		result[r.StatusID] = r.Cnt
	}
	return result, nil
}

// CountByStatusCategory returns task counts grouped by status category for a project.
func (r *TaskRepo) CountByStatusCategory(ctx context.Context, projectID uuid.UUID) (map[domain.StatusCategory]int, error) {
	const q = `
		SELECT ts.category, COUNT(t.id) AS cnt
		FROM tasks t
		INNER JOIN task_statuses ts ON ts.id = t.status_id
		WHERE t.project_id = $1 AND t.deleted_at IS NULL
		GROUP BY ts.category
	`
	type row struct {
		Category domain.StatusCategory `db:"category"`
		Cnt      int                   `db:"cnt"`
	}
	var rows []row
	if err := r.db.SelectContext(ctx, &rows, q, projectID); err != nil {
		return nil, err
	}
	result := make(map[domain.StatusCategory]int, len(rows))
	for _, r := range rows {
		result[r.Category] = r.Cnt
	}
	return result, nil
}

// ListByStatusCategory returns paginated tasks across all projects in a workspace
// that have a status matching the given category.
func (r *TaskRepo) ListByStatusCategory(ctx context.Context, workspaceID uuid.UUID, category domain.StatusCategory, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()

	const countQ = `
		SELECT COUNT(t.id)
		FROM tasks t
		INNER JOIN task_statuses ts ON ts.id = t.status_id
		INNER JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1
		  AND ts.category = $2
		  AND t.deleted_at IS NULL
		  AND p.deleted_at IS NULL
	`
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, workspaceID, category); err != nil {
		return nil, err
	}

	dataQ := `SELECT t.id, t.project_id, t.status_id, t.title, t.description,
		t.assignee_id, t.assignee_type, t.priority, t.parent_task_id, t.position,
		t.due_date, t.estimated_hours, t.custom_fields, t.labels,
		t.task_number, t.created_by, t.created_by_type, t.created_at, t.updated_at,
		t.completed_at, t.deleted_at,
		t.recurring_schedule_id, t.recurring_instance_number, ` + taskComputedColsAliased + `
		FROM tasks t
		INNER JOIN task_statuses ts ON ts.id = t.status_id
		INNER JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1
		  AND ts.category = $2
		  AND t.deleted_at IS NULL
		  AND p.deleted_at IS NULL
		ORDER BY t.created_at DESC
		LIMIT $3 OFFSET $4`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, workspaceID, category, pg.Limit(), pg.Offset()); err != nil {
		return nil, err
	}

	items := taskRowsToSlice(rows)
	return pagination.NewPage(items, totalCount, pg), nil
}

// AtomicCheckout attempts to acquire an exclusive application-level lock on the task
// for the given agent. The UPDATE is conditional: it only succeeds when the task is
// unchecked (checked_out_by IS NULL), the existing checkout has expired
// (checkout_expires < now()), or the requesting agent already holds the checkout
// (same-agent re-checkout is idempotent).
//
// If the task is locked by a different, non-expired agent, the UPDATE matches 0 rows
// and ErrCheckoutConflict is returned. No SELECT FOR UPDATE is needed because the
// single UPDATE statement is itself atomic in PostgreSQL.
func (r *TaskRepo) AtomicCheckout(ctx context.Context, taskID, agentID, token uuid.UUID, expiresAt time.Time) error {
	const query = `
		UPDATE tasks
		SET checked_out_by       = $1,
		    checkout_token        = $2,
		    checkout_expires      = $3,
		    checkout_acquired_at  = CASE
		        WHEN checked_out_by = $1 THEN checkout_acquired_at
		        ELSE now()
		    END,
		    updated_at            = now()
		WHERE id = $4
		  AND deleted_at IS NULL
		  AND (
		        checked_out_by IS NULL
		        OR checkout_expires < now()
		        OR checked_out_by = $1
		      )`
	res, err := r.db.ExecContext(ctx, query, agentID, token, expiresAt, taskID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrCheckoutConflict
	}
	return nil
}

// ReleaseCheckout clears the checkout fields on a task. The token must match the
// stored checkout_token — this prevents a different agent from accidentally
// releasing another agent's lock.
func (r *TaskRepo) ReleaseCheckout(ctx context.Context, taskID, token uuid.UUID) error {
	const query = `
		UPDATE tasks
		SET checked_out_by      = NULL,
		    checkout_token       = NULL,
		    checkout_expires     = NULL,
		    checkout_acquired_at = NULL,
		    updated_at           = now()
		WHERE id = $1
		  AND checkout_token = $2
		  AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, query, taskID, token)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrInvalidCheckoutToken
	}
	return nil
}

// MoveToProject atomically moves a task to a different project by updating
// project_id, status_id, task_number (recalculated within the target project),
// and updated_at in two statements inside an explicit transaction.
// Returns apierror.NotFound("Task") when the task does not exist or is soft-deleted.
func (r *TaskRepo) MoveToProject(ctx context.Context, taskID, targetProjectID, targetStatusID uuid.UUID) error {
	// Same two-statement pattern as Create: acquire advisory lock in Stmt 1 so
	// Stmt 2's snapshot is fresh and sees all prior committed task_numbers.
	const qLock = `SELECT pg_advisory_xact_lock(hashtext($1::text))`
	const qUpdate = `
		UPDATE tasks
		SET project_id  = $2::uuid,
		    status_id   = $3,
		    task_number = (SELECT COALESCE(MAX(task_number), 0) + 1 FROM tasks WHERE project_id = $2::uuid AND deleted_at IS NULL),
		    updated_at  = NOW()
		WHERE id = $1 AND deleted_at IS NULL
	`
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, qLock, targetProjectID); err != nil {
		return err
	}
	var res sql.Result
	res, err = tx.ExecContext(ctx, qUpdate, taskID, targetProjectID, targetStatusID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		err = apierror.NotFound("Task")
		return err
	}
	return tx.Commit()
}

// ForceReleaseCheckout clears the checkout fields without token verification.
// Used by the service layer for auto-release on terminal status transitions
// (done/review/cancelled) and for admin force-unlock via the
// `DELETE /tasks/:id/checkout?force=true` endpoint. Returns nil even when the
// task held no checkout — the caller treats this as idempotent cleanup.
func (r *TaskRepo) ForceReleaseCheckout(ctx context.Context, taskID uuid.UUID) error {
	const query = `
		UPDATE tasks
		SET checked_out_by      = NULL,
		    checkout_token       = NULL,
		    checkout_expires     = NULL,
		    checkout_acquired_at = NULL,
		    updated_at           = now()
		WHERE id = $1
		  AND deleted_at IS NULL`
	_, err := r.db.ExecContext(ctx, query, taskID)
	return err
}

// ReleaseExpiredCheckouts clears checkout fields on all tasks whose checkout_expires
// is in the past. This is called periodically by the background reaper goroutine so
// that orphan locks (held by dead sessions) are reclaimed without waiting for
// another agent to attempt a checkout on the same task.
func (r *TaskRepo) ReleaseExpiredCheckouts(ctx context.Context) (int64, error) {
	const query = `
		UPDATE tasks
		SET checked_out_by      = NULL,
		    checkout_token       = NULL,
		    checkout_expires     = NULL,
		    checkout_acquired_at = NULL,
		    updated_at           = now()
		WHERE checkout_expires < now()
		  AND checkout_expires IS NOT NULL
		  AND deleted_at IS NULL`
	res, err := r.db.ExecContext(ctx, query)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// FindExpiredInProgressCheckouts returns tasks whose checkout_expires is in the past
// and whose status category is in_progress. Uses FOR UPDATE SKIP LOCKED to avoid
// double-processing when multiple reaper goroutines race (unlikely, but safe).
func (r *TaskRepo) FindExpiredInProgressCheckouts(ctx context.Context) ([]domain.Task, error) {
	const q = `
		SELECT ` + taskBaseColsNoAlias + `
		FROM tasks
		WHERE checkout_expires < now()
		  AND checkout_expires IS NOT NULL
		  AND deleted_at IS NULL
		  AND status_id IN (
		      SELECT id FROM task_statuses WHERE category = 'in_progress'
		  )`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, q); err != nil {
		return nil, err
	}
	return taskRowsToSlice(rows), nil
}

// FindStaleUnleasedInProgress returns in_progress tasks that hold NO checkout at
// all and have been untouched for olderThan.
//
// Deliberately the complement of FindExpiredInProgressCheckouts, not an extension
// of it. That query keys on `checkout_expires < now()`, so it can only ever see a
// task whose lease EXPIRED; a task whose lease was CLEARED — by ReleaseCheckout
// (which writes no activity_log entry at all), or by the reaper's own phase 2 on a
// tick where phase 1 did not move it — has a null expiry and is invisible to it
// forever. The agent feed cannot see it either: that polls status_category=todo.
//
// The fix keys on the resulting STATE rather than on each route into it, so a
// future route in is covered without a further change.
//
// olderThan guards against robbing an agent that released its lock and is about to
// move the card itself: any activity on the task resets updated_at. Sized at the
// default checkout TTL — the system's own limit on how long a card may be held
// without renewal, so a card with no lease AND no activity for longer than that is
// abandoned by the system's own standard. Measured on 30d of prod releases: 83%
// (669/809) see follow-up activity inside 120 min and so never reach this query.
//
// Idleness is measured against comments, artifacts and VCS links as well as
// updated_at, and NOT against updated_at alone.
//
// This is load-bearing, not belt-and-braces. `add_comment` does not touch
// tasks.updated_at — only a status change does (measured 2026-08-20 on
// #603f340a: updated_at 20:47:07, last comment 21:02:32, no write in between).
// So an agent that reports progress the way the fleet workflow tells it to —
// a comment every few minutes, status unchanged because the work is unfinished —
// is, to an updated_at-only query, indistinguishable from a dead session. This
// sweep would then take the card away from a lane that is actively working it,
// which is a false positive in the expensive direction: the previous behaviour
// merely re-fed the card, the park behind mid_pipeline.auto_park_stalled hides
// it for a day.
//
// The three extra signals are exactly the three the acceptance criterion names
// as progress ("комментарий/коммит/артефакт"): a comment, a linked commit/PR, an
// uploaded artifact. GREATEST over them and updated_at gives "last sign of life
// by any route", which is the quantity the grace window was always meant to
// measure.
func (r *TaskRepo) FindStaleUnleasedInProgress(ctx context.Context, olderThan time.Duration) ([]domain.Task, error) {
	const q = `
		SELECT ` + taskBaseColsNoAlias + `
		FROM tasks t
		WHERE t.checked_out_by IS NULL
		  AND t.checkout_expires IS NULL
		  AND t.deleted_at IS NULL
		  AND t.status_id IN (
		      SELECT id FROM task_statuses WHERE category = 'in_progress'
		  )
		  AND GREATEST(
		        t.updated_at,
		        COALESCE((SELECT max(c.created_at)  FROM comments c  WHERE c.task_id  = t.id), t.updated_at),
		        COALESCE((SELECT max(a.created_at)  FROM artifacts a WHERE a.task_id  = t.id), t.updated_at),
		        COALESCE((SELECT max(v.created_at)  FROM vcs_links v WHERE v.task_id  = t.id), t.updated_at)
		      ) < now() - $1::interval`
	var rows []taskRow
	iv := fmt.Sprintf("%d seconds", int64(olderThan.Seconds()))
	if err := r.db.SelectContext(ctx, &rows, q, iv); err != nil {
		return nil, err
	}
	return taskRowsToSlice(rows), nil
}

// FindDueMonitorBacklogTasks returns tasks in "backlog" category, labelled
// "kind:monitor", whose due_date has passed. Used by the monitor promotion
// sweeper to auto-unpark passive-wait tasks once their gate time is reached.
func (r *TaskRepo) FindDueMonitorBacklogTasks(ctx context.Context) ([]domain.Task, error) {
	const q = `
		SELECT ` + taskBaseColsNoAlias + `
		FROM tasks
		WHERE deleted_at IS NULL
		  AND due_date IS NOT NULL
		  AND due_date <= now()
		  AND 'kind:monitor' = ANY(labels)
		  AND status_id IN (
		      SELECT id FROM task_statuses WHERE category = 'backlog'
		  )`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, q); err != nil {
		return nil, err
	}
	return taskRowsToSlice(rows), nil
}

// ExtendCheckout pushes the checkout_expires deadline forward. The token must match
// and the existing checkout must not already be expired (to prevent hijacking an
// expired slot via extend).
func (r *TaskRepo) ExtendCheckout(ctx context.Context, taskID, token uuid.UUID, newExpires time.Time) error {
	const query = `
		UPDATE tasks
		SET checkout_expires = $1,
		    updated_at       = now()
		WHERE id = $2
		  AND checkout_token   = $3
		  AND deleted_at IS NULL
		  AND checkout_expires >= now()`
	res, err := r.db.ExecContext(ctx, query, newExpires, taskID, token)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrInvalidCheckoutToken
	}
	return nil
}

// ListByUserActive returns active tasks (excluding done/cancelled) assigned to the given user in a workspace.
func (r *TaskRepo) ListByUserActive(ctx context.Context, workspaceID, userID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Task], error) {
	pg.Normalize()

	const countQ = `
		SELECT COUNT(t.id)
		FROM tasks t
		INNER JOIN task_statuses ts ON ts.id = t.status_id
		INNER JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1
		  AND t.assignee_id = $2
		  AND t.assignee_type = 'user'
		  AND ts.category NOT IN ('done', 'cancelled')
		  AND t.deleted_at IS NULL
		  AND p.deleted_at IS NULL`
	var totalCount int
	if err := r.db.GetContext(ctx, &totalCount, countQ, workspaceID, userID); err != nil {
		return nil, err
	}

	const dataQ = `SELECT t.id, t.project_id, t.status_id, t.title, t.description,
		t.assignee_id, t.assignee_type, t.priority, t.parent_task_id, t.position,
		t.due_date, t.estimated_hours, t.custom_fields, t.labels,
		t.task_number, t.created_by, t.created_by_type, t.created_at, t.updated_at,
		t.completed_at, t.deleted_at,
		t.recurring_schedule_id, t.recurring_instance_number, ` + taskComputedColsAliased + `
		FROM tasks t
		INNER JOIN task_statuses ts ON ts.id = t.status_id
		INNER JOIN projects p ON p.id = t.project_id
		WHERE p.workspace_id = $1
		  AND t.assignee_id = $2
		  AND t.assignee_type = 'user'
		  AND ts.category NOT IN ('done', 'cancelled')
		  AND t.deleted_at IS NULL
		  AND p.deleted_at IS NULL
		ORDER BY t.updated_at DESC, t.id ASC
		LIMIT $3 OFFSET $4`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, dataQ, workspaceID, userID, pg.Limit(), pg.Offset()); err != nil {
		return nil, err
	}
	return pagination.NewPage(taskRowsToSlice(rows), totalCount, pg), nil
}

// ListOpenByRecurringScheduleID returns non-terminal tasks (not done/cancelled/deleted)
// belonging to the given recurring schedule, excluding exceptTaskID (the new instance).
func (r *TaskRepo) ListOpenByRecurringScheduleID(ctx context.Context, scheduleID, exceptTaskID uuid.UUID) ([]domain.Task, error) {
	const q = `SELECT t.id, t.project_id, t.status_id, t.title, t.description,
		t.assignee_id, t.assignee_type, t.priority, t.parent_task_id, t.position,
		t.due_date, t.estimated_hours, t.custom_fields, t.labels,
		t.task_number, t.created_by, t.created_by_type, t.created_at, t.updated_at,
		t.completed_at, t.deleted_at,
		t.recurring_schedule_id, t.recurring_instance_number,
		t.checked_out_by, t.checkout_token, t.checkout_expires, t.checkout_acquired_at,
		t.delegation_level, t.thread_id, ` + taskComputedColsAliased + `
		FROM tasks t
		INNER JOIN task_statuses ts ON ts.id = t.status_id
		WHERE t.recurring_schedule_id = $1
		  AND t.id != $2
		  AND t.deleted_at IS NULL
		  AND ts.category NOT IN ('done', 'cancelled')
		ORDER BY t.created_at ASC`
	var rows []taskRow
	if err := r.db.SelectContext(ctx, &rows, q, scheduleID, exceptTaskID); err != nil {
		return nil, err
	}
	return taskRowsToSlice(rows), nil
}

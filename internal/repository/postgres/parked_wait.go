package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func parkedConflict() error {
	return apierror.Conflict("parked wait snapshot changed or condition is not satisfied")
}

func parkedActor(ctx context.Context) (uuid.UUID, domain.ActorType, error) {
	id, kind := actorctx.FromContext(ctx)
	if id == uuid.Nil || (kind != domain.ActorTypeAgent && kind != domain.ActorTypeUser) {
		return uuid.Nil, "", apierror.Forbidden("authenticated task writer required")
	}
	return id, kind, nil
}

func lockParkedTask(ctx context.Context, tx *sqlx.Tx, id uuid.UUID) (*domain.Task, error) {
	var row taskRow
	err := tx.GetContext(ctx, &row, `SELECT `+taskBaseColsNoAlias+` FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("Task")
	}
	task := row.toDomain()
	return &task, err
}

// Check while holding the task row lock. All task/lease/gate/comment/edge writers
// serialize here and advance task.version; no stale labels are copied back.
func parkedSnapshot(task *domain.Task, p domain.ParkedWaitPlan, actor uuid.UUID, now time.Time) error {
	if p.ExpectedVersion != task.Version || p.ProjectID != task.ProjectID || task.AssigneeID == nil ||
		p.OwnerID != *task.AssigneeID || p.OwnerType != task.AssigneeType || task.HumanGate || task.IsShipped || task.AssigneeType == domain.AssigneeTypeUser || task.DelegationLevel == domain.DelegationLevelSupervised {
		return parkedConflict()
	}
	if (task.StartAfter == nil) != (p.ExpectedStartAfter == nil) ||
		(task.StartAfter != nil && !task.StartAfter.Equal(*p.ExpectedStartAfter)) {
		return parkedConflict()
	}
	if !p.ClearStartAfter && task.StartAfter != nil && task.StartAfter.After(now) {
		return parkedConflict()
	}
	if p.Lease.Generation != task.CheckoutGeneration {
		return parkedConflict()
	}
	switch p.Lease.Mode {
	case "absent":
		if task.CheckedOutBy != nil || task.CheckoutToken != nil || task.CheckoutExpires != nil ||
			task.CheckoutSessionID != nil || task.CheckoutRequestID != nil || task.CheckoutAcquiredAt != nil {
			return parkedConflict()
		}
	case "owned":
		if p.Lease.Holder == nil || *p.Lease.Holder != actor || task.CheckedOutBy == nil ||
			*task.CheckedOutBy != actor || task.CheckoutToken == nil || task.CheckoutExpires == nil ||
			!task.CheckoutExpires.After(now) || p.Lease.SessionID == nil || task.CheckoutSessionID == nil ||
			*p.Lease.SessionID != *task.CheckoutSessionID {
			return parkedConflict()
		}
	default:
		return apierror.BadRequest("expected_lease.mode must be absent or owned")
	}
	allowed := map[string][]string{
		"dependency": {"park:dependency", "park:wait-dependency", "wake:dependency_edges", "park:wait-external"},
		"pipeline":   {"park:date", "park:pipeline", "park:wait-ci"},
		"date":       {"park:date"},
	}
	permit, ok := allowed[p.Reason]
	if !ok {
		return apierror.BadRequest("reason must be dependency, pipeline or date")
	}
	for _, label := range p.RemoveLabels {
		if !slices.Contains(permit, label) || !slices.Contains(task.Labels, label) {
			return parkedConflict()
		}
	}
	for _, original := range task.Labels {
		label := strings.ToLower(strings.TrimSpace(original))
		if slices.Contains(p.RemoveLabels, original) {
			continue
		}
		switch label {
		case "freeze", "no-promote", "no-intake-promote", "hold", "manual-park", "wait-external",
			"awaiting-window", "no-pavel-triage", "backlog-candidate", "golden", "eval-harness", "kind:monitor", "kind:verify":
			return parkedConflict()
		}
		if strings.HasPrefix(label, "park:") || strings.HasPrefix(label, "wake:") ||
			strings.HasPrefix(label, "human:") || strings.HasPrefix(label, "freeze:") {
			return parkedConflict()
		}
	}
	var fields map[string]json.RawMessage
	if len(task.CustomFields) != 0 && json.Unmarshal(task.CustomFields, &fields) != nil {
		return parkedConflict()
	}
	if raw, exists := fields["park_reason"]; exists {
		var reason string
		if json.Unmarshal(raw, &reason) != nil || reason != p.Reason {
			return parkedConflict()
		}
	}
	return nil
}

func (r *TaskRepo) RegisterParkedWait(ctx context.Context, taskID uuid.UUID, p domain.ParkedWaitPlan) (*domain.ParkedWaitResult, error) {
	actor, kind, err := parkedActor(ctx)
	if err != nil {
		return nil, err
	}
	if validationErr := validateParkedPlan(p); validationErr != nil {
		return nil, validationErr
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	task, err := lockParkedTask(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	// Replay identical registrations before checking the now-changed task. A
	// generation can never be refreshed onto a new snapshot with the old WAIT.
	var prior struct {
		Plan   []byte `db:"plan"`
		Result []byte `db:"result"`
	}
	err = tx.GetContext(ctx, &prior, `SELECT plan,result FROM parked_waits WHERE task_id=$1 AND (id=$2 OR wait_comment_id=$3)`, taskID, p.ID, p.WaitCommentID)
	if err == nil {
		var equal bool
		if checkErr := tx.GetContext(ctx, &equal, `SELECT $1::jsonb=$2::jsonb`, data, prior.Plan); checkErr != nil {
			return nil, checkErr
		}
		if !equal {
			return nil, parkedConflict()
		}
		var result domain.ParkedWaitResult
		if checkErr := json.Unmarshal(prior.Result, &result); checkErr != nil {
			return nil, checkErr
		}
		result.Replayed = true
		return &result, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// A future unrelated start_after prevents release, not discovery/registration.
	check := p
	check.ClearStartAfter = true
	if checkErr := parkedSnapshot(task, check, actor, time.Now()); checkErr != nil {
		return nil, checkErr
	}
	var category string
	if checkErr := tx.GetContext(ctx, &category, `SELECT category FROM task_statuses WHERE id=$1 FOR SHARE`, task.StatusID); checkErr != nil {
		return nil, checkErr
	}
	if category != "backlog" {
		return nil, parkedConflict()
	}
	var comment domain.Comment
	if checkErr := tx.GetContext(ctx, &comment, `SELECT id,task_id,author_id,author_type,body,metadata,created_at,updated_at,is_internal,parent_comment_id FROM comments WHERE id=$1 AND task_id=$2`, p.WaitCommentID, taskID); checkErr != nil {
		if errors.Is(checkErr, sql.ErrNoRows) {
			return nil, parkedConflict()
		}
		return nil, checkErr
	}
	if checkErr := validateParkedComment(comment, p); checkErr != nil {
		return nil, checkErr
	}
	var latest uuid.UUID
	if checkErr := tx.GetContext(ctx, &latest, `SELECT id FROM comments WHERE task_id=$1 AND author_id=$2 AND author_type=$3 AND body LIKE '⏳ WAIT %' ORDER BY created_at DESC,id DESC LIMIT 1`, taskID, p.OwnerID, p.OwnerType); checkErr != nil {
		return nil, checkErr
	}
	if latest != p.WaitCommentID {
		return nil, parkedConflict()
	}
	if p.Reason == "dependency" {
		var exists bool
		if checkErr := tx.GetContext(ctx, &exists, `SELECT EXISTS(SELECT 1 FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2 AND dependency_type='blocks')`, taskID, p.Condition.TaskID); checkErr != nil {
			return nil, checkErr
		}
		if !exists {
			return nil, parkedConflict()
		}
	}
	result := domain.ParkedWaitResult{RegistrationID: p.ID, TaskID: taskID, ProjectID: p.ProjectID, OwnerID: p.OwnerID, Version: task.Version}
	resultData, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	insert, err := tx.ExecContext(ctx, `INSERT INTO parked_waits(id,task_id,wait_comment_id,plan,registered_by,registered_by_type,result) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, p.ID, taskID, p.WaitCommentID, data, actor, kind, resultData)
	if err != nil {
		return nil, err
	}
	n, err := insert.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, parkedConflict()
	}
	return &result, tx.Commit()
}

func (r *TaskRepo) GetParkedWait(ctx context.Context, taskID, id uuid.UUID) (*domain.RegisteredParkedWait, error) {
	var row struct {
		Plan   []byte `db:"plan"`
		Result []byte `db:"result"`
	}
	if checkErr := r.db.GetContext(ctx, &row, `SELECT plan,result FROM parked_waits WHERE task_id=$1 AND id=$2`, taskID, id); checkErr != nil {
		if errors.Is(checkErr, sql.ErrNoRows) {
			return nil, apierror.NotFound("Parked wait")
		}
		return nil, checkErr
	}
	var result domain.RegisteredParkedWait
	if checkErr := json.Unmarshal(row.Plan, &result.Plan); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := json.Unmarshal(row.Result, &result.Result); checkErr != nil {
		return nil, checkErr
	}
	return &result, nil
}

// ReleaseParkedWait commits the narrow label subtraction, lease effects, wake,
// receipt and visible activity together. verifiedStatus is server-derived.
func (r *TaskRepo) ReleaseParkedWait(ctx context.Context, taskID uuid.UUID, input domain.ReleaseParkedWait, verifiedStatus string) (*domain.ParkedWaitResult, error) {
	actor, kind, err := parkedActor(ctx)
	if err != nil {
		return nil, err
	}
	if input.RegistrationID == uuid.Nil || input.ReleaseID == uuid.Nil || input.ExpectedVersion < 1 || input.Trigger.EventID == "" || len(input.Trigger.EventID) > 256 {
		return nil, apierror.BadRequest("registration, version, release_id and trigger.event_id required")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	task, err := lockParkedTask(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	var row struct {
		Plan      []byte            `db:"plan"`
		Result    []byte            `db:"result"`
		ReleaseID *uuid.UUID        `db:"release_id"`
		Request   []byte            `db:"release_request"`
		Actor     *uuid.UUID        `db:"released_by"`
		Kind      *domain.ActorType `db:"released_by_type"`
	}
	if checkErr := tx.GetContext(ctx, &row, `SELECT plan,result,release_id,release_request,released_by,released_by_type FROM parked_waits WHERE task_id=$1 AND id=$2 FOR UPDATE`, taskID, input.RegistrationID); checkErr != nil {
		if errors.Is(checkErr, sql.ErrNoRows) {
			return nil, apierror.NotFound("Parked wait")
		}
		return nil, checkErr
	}
	request, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var result domain.ParkedWaitResult
	if checkErr := json.Unmarshal(row.Result, &result); checkErr != nil {
		return nil, checkErr
	}
	if row.ReleaseID != nil {
		var equal bool
		if checkErr := tx.GetContext(ctx, &equal, `SELECT $1::jsonb=$2::jsonb`, request, row.Request); checkErr != nil {
			return nil, checkErr
		}
		if !equal || row.Actor == nil || *row.Actor != actor || row.Kind == nil || *row.Kind != kind {
			return nil, parkedConflict()
		}
		result.Replayed = true
		return &result, tx.Commit()
	}
	var p domain.ParkedWaitPlan
	if checkErr := json.Unmarshal(row.Plan, &p); checkErr != nil {
		return nil, checkErr
	}
	if p.ExpectedVersion != input.ExpectedVersion || input.Trigger.Kind != p.Reason {
		return nil, parkedConflict()
	}
	var now time.Time
	if checkErr := tx.GetContext(ctx, &now, `SELECT clock_timestamp()`); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := parkedSnapshot(task, p, actor, now); checkErr != nil {
		return nil, checkErr
	}
	var old, target struct {
		ID       uuid.UUID `db:"id"`
		Name     string    `db:"name"`
		Category string    `db:"category"`
	}
	if checkErr := tx.GetContext(ctx, &old, `SELECT id,name,category FROM task_statuses WHERE id=$1 FOR SHARE`, task.StatusID); checkErr != nil {
		return nil, checkErr
	}
	if old.Category != "backlog" {
		return nil, parkedConflict()
	}
	if checkErr := tx.GetContext(ctx, &target, `SELECT id,name,category FROM task_statuses WHERE project_id=$1 AND category='todo' ORDER BY position,id LIMIT 1 FOR SHARE`, task.ProjectID); checkErr != nil {
		return nil, checkErr
	}
	// Lock the edge rows and their blockers; blockers reopening must serialize
	// before or after this decision. The edge trigger guards new/deleted edges.
	var blockers []struct {
		ID       uuid.UUID  `db:"id"`
		Category string     `db:"category"`
		Deleted  *time.Time `db:"deleted_at"`
	}
	var blockerIDs []uuid.UUID
	err = tx.SelectContext(ctx, &blockerIDs, `SELECT b.id FROM task_dependencies d JOIN tasks b ON b.id=d.depends_on_task_id WHERE d.task_id=$1 AND d.dependency_type='blocks' ORDER BY b.id FOR SHARE OF d,b`, taskID)
	if err != nil {
		return nil, err
	}
	// A joined category read in the blocking SELECT can retain its old
	// statement snapshot after a blocker reopens. Read it in a fresh statement
	// only once every blocker row is locked, and lock status definitions too.
	err = tx.SelectContext(ctx, &blockers, `SELECT b.id,s.category,b.deleted_at FROM tasks b JOIN task_statuses s ON s.id=b.status_id WHERE b.id=ANY($1::uuid[]) ORDER BY b.id FOR SHARE OF s`, pq.Array(blockerIDs))
	if err != nil {
		return nil, err
	}
	for _, blocker := range blockers {
		if blocker.Deleted != nil || (blocker.Category != "done" && blocker.Category != "cancelled") {
			return nil, parkedConflict()
		}
	}
	switch p.Reason {
	case "dependency":
		if len(blockers) == 0 || !slices.ContainsFunc(blockers, func(b struct {
			ID       uuid.UUID  `db:"id"`
			Category string     `db:"category"`
			Deleted  *time.Time `db:"deleted_at"`
		}) bool {
			return b.ID == *p.Condition.TaskID
		}) {
			return nil, parkedConflict()
		}
	case "date":
		if p.Condition.NotBefore.After(now) {
			return nil, parkedConflict()
		}
	case "pipeline":
		if !terminalPipelineStatus(verifiedStatus) {
			return nil, parkedConflict()
		}
	}
	activityID := uuid.New()
	changes, err := json.Marshal(map[string]any{"status": map[string]string{"old": old.Name, "new": target.Name}, "source": "parked-wait-api", "reason": p.Reason, "trigger": input.Trigger, "pipeline_status": verifiedStatus, "wait_comment_id": p.WaitCommentID, "feed_receipt_id": p.FeedReceiptID, "registration_id": p.ID, "release_id": input.ReleaseID, "removed_labels": p.RemoveLabels, "cleared_start_after": p.ClearStartAfter, "old_version": task.Version, "new_version": task.Version + 1})
	if err != nil {
		return nil, err
	}
	// No full labels PATCH, assignment, title or custom-field snapshot overwrite.
	err = tx.GetContext(ctx, &result.Version, `UPDATE tasks SET status_id=$2,status_changed_at=$3,completed_at=NULL,updated_at=$3,
 labels=ARRAY(SELECT label FROM unnest(labels) label WHERE NOT(label=ANY(COALESCE($4::text[],'{}'::text[])))),
 start_after=CASE WHEN $5 THEN NULL ELSE start_after END,
 checked_out_by=CASE WHEN $6 THEN NULL ELSE checked_out_by END,
 checkout_token=CASE WHEN $6 THEN NULL ELSE checkout_token END,
 checkout_expires=CASE WHEN $6 THEN NULL ELSE checkout_expires END,
 checkout_acquired_at=CASE WHEN $6 THEN NULL ELSE checkout_acquired_at END,
 checkout_session_id=CASE WHEN $6 THEN NULL ELSE checkout_session_id END,
 checkout_request_id=CASE WHEN $6 THEN NULL ELSE checkout_request_id END
 WHERE id=$1 AND version=$7 RETURNING version`, taskID, target.ID, now, pq.Array(p.RemoveLabels), p.ClearStartAfter, p.Lease.Mode == "owned", input.ExpectedVersion)
	if err != nil {
		return nil, err
	}
	activityInsert, err := tx.ExecContext(ctx, `INSERT INTO activity_log(id,workspace_id,entity_type,entity_id,action,actor_id,actor_type,changes,created_at) SELECT $1,workspace_id,'task',$2,'task.moved',$3,$4,$5,$6 FROM projects WHERE id=$7`, activityID, taskID, actor, kind, changes, now, task.ProjectID)
	if err != nil {
		return nil, err
	}
	n, err := activityInsert.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, parkedConflict()
	}
	result.Released = true
	result.ActivityID = &activityID
	resultData, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE parked_waits SET release_id=$2,release_request=$3,released_by=$4,released_by_type=$5,result=$6 WHERE id=$1`, p.ID, input.ReleaseID, request, actor, kind, resultData)
	if err != nil {
		return nil, err
	}
	return &result, tx.Commit()
}

func terminalPipelineStatus(status string) bool {
	return slices.Contains([]string{"success", "failed", "canceled", "skipped"}, status)
}

var parkedProjectPath = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*(/[A-Za-z0-9_-][A-Za-z0-9_.-]*)+$`)
var parkedDateLine = regexp.MustCompile(`^⏳ WAIT ([1-9]\d*)m:`)

func parkedWaitToken(line, token string) bool {
	if !strings.HasPrefix(line, token) {
		return false
	}
	rest := strings.TrimPrefix(line, token)
	return rest == "" || strings.HasPrefix(rest, ":") || strings.HasPrefix(rest, " ")
}
func validateParkedPlan(p domain.ParkedWaitPlan) error {
	if p.FeedSource != "confirmed_feed" {
		return apierror.BadRequest("confirmed feed receipt with captured project and owner required; legacy discovery cannot authorize registration")
	}
	if len(p.RemoveLabels) > 8 || p.Lease.Generation < 0 {
		return apierror.BadRequest("bounded labels and nonnegative lease generation required")
	}
	seen := make(map[string]bool)
	for _, label := range p.RemoveLabels {
		if seen[label] {
			return apierror.BadRequest("duplicate remove_labels")
		}
		seen[label] = true
	}
	if p.Lease.Mode == "absent" && (p.Lease.Holder != nil || p.Lease.SessionID != nil) {
		return apierror.BadRequest("absent lease cannot have a holder or session")
	}
	if p.ID == uuid.Nil || p.ProjectID == uuid.Nil || p.OwnerID == uuid.Nil || p.WaitCommentID == uuid.Nil || p.FeedReceiptID == uuid.Nil || p.ExpectedVersion < 1 || (p.OwnerType != domain.AssigneeTypeAgent && p.OwnerType != domain.AssigneeTypeUser) {
		return apierror.BadRequest("explicit registration, project, owner, version, WAIT comment and feed receipt required")
	}
	if p.FeedReceivedAt.IsZero() || p.FeedClosedAt.Before(p.FeedReceivedAt) || p.FeedClosedAt.After(time.Now().Add(time.Minute)) {
		return apierror.BadRequest("valid persisted feed window required")
	}
	if p.ClearStartAfter && p.ExpectedStartAfter == nil {
		return apierror.BadRequest("clear_start_after requires an owned exact timestamp")
	}
	switch p.Reason {
	case "dependency":
		if p.Condition.TaskID == nil || *p.Condition.TaskID == uuid.Nil || p.Condition.ProjectPath != "" || p.Condition.NotBefore != nil || p.Condition.PipelineID != 0 || p.Condition.TimeSemantics != "" {
			return apierror.BadRequest("explicit dependency condition required")
		}
	case "pipeline":
		if p.Condition.ProjectPath == "" || p.Condition.PipelineID <= 0 || strings.ContainsAny(p.Condition.ProjectPath, "?#\\") || p.Condition.TaskID != nil || p.Condition.NotBefore != nil || p.Condition.TimeSemantics != "" || len(p.Condition.ProjectPath) > 255 || !parkedProjectPath.MatchString(p.Condition.ProjectPath) {
			return apierror.BadRequest("explicit GitLab project/pipeline condition required")
		}
	case "date":
		if p.Condition.NotBefore == nil || p.Condition.TimeSemantics != "not_before" || p.Condition.TaskID != nil || p.Condition.ProjectPath != "" || p.Condition.PipelineID != 0 {
			return apierror.BadRequest("explicit not_before time semantics required")
		}
	default:
		return apierror.BadRequest("unsupported park reason")
	}
	return nil
}

func validateParkedComment(comment domain.Comment, p domain.ParkedWaitPlan) error {
	if comment.AuthorID != p.OwnerID || string(comment.AuthorType) != string(p.OwnerType) || comment.CreatedAt.Before(p.FeedReceivedAt) || comment.CreatedAt.After(p.FeedClosedAt) || !strings.HasPrefix(comment.Body, "⏳ WAIT ") {
		return parkedConflict()
	}
	line, _, _ := strings.Cut(comment.Body, "\n")
	switch p.Reason {
	case "dependency":
		if !parkedWaitToken(line, "⏳ WAIT card:#"+p.Condition.TaskID.String()[:8]) {
			return parkedConflict()
		}
	case "pipeline":
		if !parkedWaitToken(line, fmt.Sprintf("⏳ WAIT pipeline:%s#%d", p.Condition.ProjectPath, p.Condition.PipelineID)) {
			return parkedConflict()
		}
	case "date":
		match := parkedDateLine.FindStringSubmatch(line)
		if match == nil {
			return parkedConflict()
		}
		minutes, err := strconv.Atoi(match[1])
		if err != nil || minutes <= 0 || minutes > 525600 || !comment.CreatedAt.Add(time.Duration(minutes)*time.Minute).Equal(*p.Condition.NotBefore) {
			return parkedConflict()
		}
	}
	// Clearing another scheduler's start_after is forbidden. Ownership must be
	// explicit in the owner-authored WAIT metadata, not asserted by the caller.
	if p.ClearStartAfter {
		var metadata struct {
			ParkedWait struct {
				StartAfter *time.Time `json:"start_after"`
			} `json:"parked_wait"`
		}
		if json.Unmarshal(comment.Metadata, &metadata) != nil || metadata.ParkedWait.StartAfter == nil || !metadata.ParkedWait.StartAfter.Equal(*p.ExpectedStartAfter) {
			return parkedConflict()
		}
	}
	return nil
}

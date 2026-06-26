package service

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/robfig/cron/v3"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// TemplateData holds variables available inside title_template and description_template.
type TemplateData struct {
	Date        string // "2006-01-02"
	DateTime    string // "2006-01-02 15:04"
	Number      int
	Week        string // "W12"
	Month       string // "March"
	PrevSummary string // last comment of previous instance, truncated to 500 chars
}

// recurringService implements RecurringService.
type recurringService struct {
	recurringRepo repository.RecurringRepository
	taskSvc       TaskService
	commentRepo   repository.CommentRepository
	artifactRepo  repository.ArtifactRepository

	// alertFunc is called when a schedule is quarantined after repeated failures.
	// Nil by default (alerts fall back to log.Printf only).
	alertFunc func(scheduleID uuid.UUID, lastErr error)
}

// RecurringServiceOption configures optional dependencies for RecurringService.
type RecurringServiceOption func(*recurringService)

// WithRecurringRepo sets the recurring repository.
func WithRecurringRepo(r repository.RecurringRepository) RecurringServiceOption {
	return func(s *recurringService) {
		s.recurringRepo = r
	}
}

// WithTaskServiceForRecurring sets the task service used to create instances.
func WithTaskServiceForRecurring(ts TaskService) RecurringServiceOption {
	return func(s *recurringService) {
		s.taskSvc = ts
	}
}

// WithCommentRepoForRecurring sets the comment repository for fetching previous-instance summaries.
func WithCommentRepoForRecurring(cr repository.CommentRepository) RecurringServiceOption {
	return func(s *recurringService) {
		s.commentRepo = cr
	}
}

// WithArtifactRepoForRecurring sets the artifact repository for counting artifacts.
func WithArtifactRepoForRecurring(ar repository.ArtifactRepository) RecurringServiceOption {
	return func(s *recurringService) {
		s.artifactRepo = ar
	}
}

// WithAlertFunc registers a callback invoked when a schedule is quarantined after
// maxConsecutiveFailures consecutive createInstance errors. The callback receives the
// schedule ID and the triggering error. Safe for concurrent use from RunDue goroutines.
func WithAlertFunc(fn func(scheduleID uuid.UUID, lastErr error)) RecurringServiceOption {
	return func(s *recurringService) {
		s.alertFunc = fn
	}
}

// NewRecurringService creates a new RecurringService.
func NewRecurringService(
	recurringRepo repository.RecurringRepository,
	taskSvc TaskService,
	opts ...RecurringServiceOption,
) RecurringService {
	s := &recurringService{
		recurringRepo: recurringRepo,
		taskSvc:       taskSvc,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// maxConsecutiveFailures is the number of consecutive createInstance failures that
// triggers schedule quarantine (is_active=false) and an alert.
const maxConsecutiveFailures = 3

// prevSummaryMaxRunes is the maximum number of Unicode code points kept from the
// previous instance's last comment when injecting into {{.PrevSummary}}.
const prevSummaryMaxRunes = 500

// truncateRuneSafe returns s truncated to at most maxRunes Unicode code points.
// Unlike s[:n], this function always cuts on a rune boundary, preventing
// invalid UTF-8 when s contains multibyte sequences (Cyrillic, emoji, etc.).
func truncateRuneSafe(s string, maxRunes int) string {
	runeCount := 0
	for i := range s {
		if runeCount == maxRunes {
			return s[:i]
		}
		runeCount++
	}
	return s
}

// cronParser parses 5-field cron expressions (standard cron without seconds).
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// defaultCronExpr returns the default cron expression for a given frequency.
func defaultCronExpr(freq domain.RecurringFrequency) string {
	switch freq {
	case domain.RecurringFrequencyDaily:
		return "0 9 * * *"
	case domain.RecurringFrequencyWeekly:
		return "0 9 * * 1"
	case domain.RecurringFrequencyMonthly:
		return "0 9 1 * *"
	default:
		return ""
	}
}

// validateAndResolveCron validates the cron expression and fills it in for standard frequencies.
// Returns the resolved cron expression or an error.
func validateAndResolveCron(freq domain.RecurringFrequency, cronExpr string) (string, error) {
	// For standard frequencies, auto-fill if not provided.
	if freq != domain.RecurringFrequencyCustom && cronExpr == "" {
		cronExpr = defaultCronExpr(freq)
	}
	if cronExpr == "" {
		return "", apierror.BadRequestWithDetails("validation failed", "cron_expr is required for frequency=custom")
	}
	// Validate the expression.
	if _, err := cronParser.Parse(cronExpr); err != nil {
		return "", &apierror.Error{
			Code:    422,
			Message: "invalid cron_expr",
			Details: err.Error(),
		}
	}
	return cronExpr, nil
}

// validateTimezone checks that the timezone string is a valid IANA timezone.
func validateTimezone(tz string) error {
	if tz == "" {
		return nil // will default to UTC
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return &apierror.Error{
			Code:    422,
			Message: "invalid timezone",
			Details: err.Error(),
		}
	}
	return nil
}

// computeNextRun calculates the next cron tick after the reference time in the given timezone.
func computeNextRun(cronExpr, timezone string, after time.Time) (*time.Time, error) {
	loc := time.UTC
	if timezone != "" {
		var err error
		loc, err = time.LoadLocation(timezone)
		if err != nil {
			return nil, fmt.Errorf("computeNextRun LoadLocation: %w", err)
		}
	}
	expr, err := cronParser.Parse(cronExpr)
	if err != nil {
		return nil, fmt.Errorf("computeNextRun parse: %w", err)
	}
	next := expr.Next(after.In(loc))
	return &next, nil
}

// renderTemplate renders a Go text/template string with the given data.
func renderTemplate(tmpl string, data TemplateData) (string, error) {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("renderTemplate parse: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("renderTemplate execute: %w", err)
	}
	return buf.String(), nil
}

// buildTemplateData creates TemplateData for the given run time and instance number.
func buildTemplateData(runAt time.Time, loc *time.Location, number int, prevSummary string) TemplateData {
	t := runAt.In(loc)
	_, isoWeek := t.ISOWeek()
	return TemplateData{
		Date:        t.Format("2006-01-02"),
		DateTime:    t.Format("2006-01-02 15:04"),
		Number:      number,
		Week:        fmt.Sprintf("W%02d", isoWeek),
		Month:       t.Month().String(),
		PrevSummary: prevSummary,
	}
}

// getPreviousInstanceSummary fetches a summary of the most recent completed instance.
// Returns nil if no previous instances exist.
func (s *recurringService) getPreviousInstanceSummary(ctx context.Context, scheduleID uuid.UUID) *domain.RecurringInstanceSummary {
	pg := pagination.Params{Page: 1, PageSize: 1, SortDir: "desc"}
	page, err := s.recurringRepo.GetInstanceHistory(ctx, scheduleID, pg)
	if err != nil || len(page.Items) == 0 {
		return nil
	}
	summary := page.Items[0]
	// Truncate PrevSummary on a rune boundary to prevent mid-rune byte cuts
	// that produce invalid UTF-8 when the comment contains multibyte characters.
	if summary.LastComment != nil && utf8.RuneCountInString(*summary.LastComment) > prevSummaryMaxRunes {
		truncated := truncateRuneSafe(*summary.LastComment, prevSummaryMaxRunes)
		summary.LastComment = &truncated
	}
	return &summary
}

// Create validates input, resolves the cron expression, and persists a new recurring schedule.
func (s *recurringService) Create(ctx context.Context, input CreateRecurringInput) (*domain.RecurringSchedule, error) {
	if input.TitleTemplate == "" {
		return nil, apierror.ValidationError(map[string]string{
			"title_template": "title_template is required",
		})
	}

	// Validate timezone.
	if err := validateTimezone(input.Timezone); err != nil {
		return nil, err
	}
	if input.Timezone == "" {
		input.Timezone = "UTC"
	}

	// Validate and resolve cron expression.
	cronExpr, err := validateAndResolveCron(input.Frequency, input.CronExpr)
	if err != nil {
		return nil, err
	}

	// Dry-run template to catch invalid syntax early.
	loc, _ := time.LoadLocation(input.Timezone)
	data := buildTemplateData(time.Now(), loc, 1, "")
	if _, err = renderTemplate(input.TitleTemplate, data); err != nil {
		return nil, &apierror.Error{Code: 422, Message: "invalid title_template", Details: err.Error()}
	}
	if input.DescriptionTemplate != "" {
		if _, err = renderTemplate(input.DescriptionTemplate, data); err != nil {
			return nil, &apierror.Error{Code: 422, Message: "invalid description_template", Details: err.Error()}
		}
	}

	// Compute initial next_run_at.
	startsAt := input.StartsAt
	if startsAt.IsZero() {
		startsAt = time.Now()
	}
	nextRun, err := computeNextRun(cronExpr, input.Timezone, startsAt)
	if err != nil {
		return nil, fmt.Errorf("Create computeNextRun: %w", err)
	}

	now := time.Now()
	schedule := &domain.RecurringSchedule{
		ID:                  uuid.New(),
		WorkspaceID:         input.WorkspaceID,
		ProjectID:           input.ProjectID,
		TitleTemplate:       input.TitleTemplate,
		DescriptionTemplate: input.DescriptionTemplate,
		Frequency:           input.Frequency,
		CronExpr:            cronExpr,
		Timezone:            input.Timezone,
		AssigneeID:          input.AssigneeID,
		AssigneeType:        input.AssigneeType,
		Priority:            input.Priority,
		Labels:              pq.StringArray(input.Labels),
		StatusID:            input.StatusID,
		IsActive:            input.IsActive,
		StartsAt:            startsAt,
		EndsAt:              input.EndsAt,
		MaxInstances:        input.MaxInstances,
		NextRunAt:           nextRun,
		InstanceCount:       0,
		CreatedBy:           input.CreatedBy,
		CreatedByType:       input.CreatedByType,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	if err := s.recurringRepo.Create(ctx, schedule); err != nil {
		return nil, fmt.Errorf("Create repo: %w", err)
	}

	return schedule, nil
}

// GetByID retrieves a recurring schedule by ID.
func (s *recurringService) GetByID(ctx context.Context, id uuid.UUID) (*domain.RecurringSchedule, error) {
	schedule, err := s.recurringRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if schedule == nil {
		return nil, apierror.NotFound("RecurringSchedule")
	}
	return schedule, nil
}

// Update applies partial updates to a recurring schedule.
// If cron_expr or timezone changes, next_run_at is recalculated.
func (s *recurringService) Update(ctx context.Context, id uuid.UUID, input UpdateRecurringInput) (*domain.RecurringSchedule, error) {
	schedule, err := s.recurringRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if schedule == nil {
		return nil, apierror.NotFound("RecurringSchedule")
	}

	cronChanged := false
	tzChanged := false

	if input.TitleTemplate != nil {
		schedule.TitleTemplate = *input.TitleTemplate
	}
	if input.DescriptionTemplate != nil {
		schedule.DescriptionTemplate = *input.DescriptionTemplate
	}
	if input.Frequency != nil {
		schedule.Frequency = *input.Frequency
		cronChanged = true
	}
	if input.CronExpr != nil {
		schedule.CronExpr = *input.CronExpr
		cronChanged = true
	}
	if input.Timezone != nil {
		if err := validateTimezone(*input.Timezone); err != nil {
			return nil, err
		}
		schedule.Timezone = *input.Timezone
		tzChanged = true
	}
	if input.AssigneeID != nil {
		schedule.AssigneeID = input.AssigneeID
	}
	if input.AssigneeType != nil {
		schedule.AssigneeType = *input.AssigneeType
	}
	if input.Priority != nil {
		schedule.Priority = *input.Priority
	}
	if input.Labels != nil {
		schedule.Labels = pq.StringArray(*input.Labels)
	}
	if input.StatusID != nil {
		schedule.StatusID = input.StatusID
	}
	if input.IsActive != nil {
		schedule.IsActive = *input.IsActive
	}
	if input.EndsAt != nil {
		schedule.EndsAt = input.EndsAt
	}
	if input.MaxInstances != nil {
		schedule.MaxInstances = input.MaxInstances
	}

	// Re-validate and resolve cron if frequency or timezone changed.
	if cronChanged || tzChanged {
		resolvedCron, err := validateAndResolveCron(schedule.Frequency, schedule.CronExpr)
		if err != nil {
			return nil, err
		}
		schedule.CronExpr = resolvedCron

		nextRun, err := computeNextRun(schedule.CronExpr, schedule.Timezone, time.Now())
		if err != nil {
			return nil, fmt.Errorf("Update computeNextRun: %w", err)
		}
		schedule.NextRunAt = nextRun
	}

	schedule.UpdatedAt = time.Now()
	if err := s.recurringRepo.Update(ctx, schedule); err != nil {
		return nil, err
	}

	return schedule, nil
}

// Delete soft-deletes a recurring schedule.
func (s *recurringService) Delete(ctx context.Context, id uuid.UUID) error {
	return s.recurringRepo.Delete(ctx, id)
}

// ListByProject returns a paginated list of schedules for a project.
func (s *recurringService) ListByProject(ctx context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.RecurringSchedule], error) {
	return s.recurringRepo.ListByProject(ctx, projectID, pg)
}

// GetHistory returns paginated instance summaries for a recurring schedule.
func (s *recurringService) GetHistory(ctx context.Context, id uuid.UUID, pg pagination.Params) (*pagination.Page[domain.RecurringInstanceSummary], error) {
	// Verify the schedule exists first.
	schedule, err := s.recurringRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if schedule == nil {
		return nil, apierror.NotFound("RecurringSchedule")
	}
	return s.recurringRepo.GetInstanceHistory(ctx, id, pg)
}

// TriggerNow creates the next task instance immediately without advancing the regular schedule.
func (s *recurringService) TriggerNow(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	schedule, err := s.recurringRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if schedule == nil {
		return nil, apierror.NotFound("RecurringSchedule")
	}

	task, err := s.createInstance(ctx, schedule, time.Now())
	if err != nil {
		return nil, err
	}

	// Increment instance count without changing next_run_at (don't disrupt the regular schedule).
	if err := s.recurringRepo.IncrementInstance(ctx, schedule.ID, schedule.NextRunAt); err != nil {
		log.Printf("[recurring] WARNING: TriggerNow IncrementInstance failed for schedule %s: %v", schedule.ID, err)
	}

	// Supersede any open instances from the same schedule (same as runOneSchedule).
	if err := s.taskSvc.SupersedeRecurringInstances(ctx, schedule.ID, task.ID); err != nil {
		log.Printf("[recurring] WARNING: TriggerNow SupersedeRecurringInstances for schedule %s: %v", schedule.ID, err)
	}

	return task, nil
}

// RunDue finds all due schedules and creates task instances for each.
// Each instance is created in a separate goroutine with a 30s timeout.
// Returns the number of instances created.
func (s *recurringService) RunDue(ctx context.Context) (int, error) {
	schedules, err := s.recurringRepo.FindDue(ctx)
	if err != nil {
		return 0, fmt.Errorf("RunDue FindDue: %w", err)
	}
	if len(schedules) == 0 {
		return 0, nil
	}

	type result struct {
		created bool
		err     error
	}

	results := make(chan result, len(schedules))
	var wg sync.WaitGroup

	for i := range schedules {
		schedule := schedules[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			instCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			res := result{}
			res.created, res.err = s.runOneSchedule(instCtx, &schedule)
			results <- res
		}()
	}

	// Wait for all goroutines then close the channel.
	go func() {
		wg.Wait()
		close(results)
	}()

	created := 0
	for r := range results {
		if r.err != nil {
			log.Printf("[recurring] ERROR processing schedule: %v", r.err)
		} else if r.created {
			created++
		}
	}

	return created, nil
}

// runOneSchedule processes a single due schedule: checks limits, creates the instance, updates state.
// Returns true if an instance was created.
//
// Invariants enforced here:
//   - ≤1 instance created per tick regardless of how far behind next_run_at has drifted.
//   - next_run_at is always advanced past the current tick, even on createInstance failure,
//     so a poisoned schedule cannot loop every 60 s forever.
//   - After maxConsecutiveFailures consecutive createInstance errors the schedule is
//     quarantined (is_active=false) and alertFunc is called.
func (s *recurringService) runOneSchedule(ctx context.Context, schedule *domain.RecurringSchedule) (bool, error) {
	// Check ends_at.
	if schedule.EndsAt != nil && time.Now().After(*schedule.EndsAt) {
		log.Printf("[recurring] schedule %s past ends_at, skipping", schedule.ID)
		return false, nil
	}
	// Check max_instances.
	if schedule.MaxInstances != nil && schedule.InstanceCount >= *schedule.MaxInstances {
		log.Printf("[recurring] schedule %s reached max_instances %d, skipping", schedule.ID, *schedule.MaxInstances)
		return false, nil
	}

	now := time.Now()

	// Catch-up detection: if next_run_at is more than one cron interval in the past
	// we are recovering from a scheduler outage or a quarantine. Log a WARN so ops
	// can see that occurrences were skipped.
	if schedule.NextRunAt != nil && schedule.NextRunAt.Before(now) {
		firstAfterScheduled, err := computeNextRun(schedule.CronExpr, schedule.Timezone, *schedule.NextRunAt)
		if err == nil && firstAfterScheduled != nil && firstAfterScheduled.Before(now) {
			// Count how many occurrences we're skipping.
			missed := 0
			cur := *schedule.NextRunAt
			for missed < 10_000 {
				next, err2 := computeNextRun(schedule.CronExpr, schedule.Timezone, cur)
				if err2 != nil || next == nil || !next.Before(now) {
					break
				}
				missed++
				cur = *next
			}
			log.Printf("[recurring] WARN schedule %s recovered: skipped %d missed occurrence(s) (%s → %s), creating 1 catch-up instance",
				schedule.ID, missed, schedule.NextRunAt.Format(time.RFC3339), now.Format(time.RFC3339))
		}
	}

	runAt := now
	if schedule.NextRunAt != nil {
		runAt = *schedule.NextRunAt
	}

	// Compute next_run_at as the first cron occurrence strictly after now — NOT
	// prev+interval — so catch-up never produces a burst of back-dated instances.
	nextRun, nextRunErr := computeNextRun(schedule.CronExpr, schedule.Timezone, now)
	if nextRunErr != nil {
		log.Printf("[recurring] WARNING: computeNextRun for schedule %s failed: %v", schedule.ID, nextRunErr)
		nextRun = nil
	}

	// Create the task instance.
	newTask, err := s.createInstance(ctx, schedule, runAt)
	if err != nil {
		// Capture the failure count before RecordFailure so the quarantine threshold
		// check uses the pre-DB-write value (RecordFailure increments the DB column;
		// in tests the mock may mutate the struct in-place through the pointer).
		priorFailures := schedule.ConsecutiveFailures

		// Always advance next_run_at and record the failure in the DB so the same
		// poisoned INSERT is not retried on every 60 s scheduler tick, even across restarts.
		if rfErr := s.recurringRepo.RecordFailure(ctx, schedule.ID, nextRun, err.Error()); rfErr != nil {
			log.Printf("[recurring] ERROR recording failure for schedule %s: %v", schedule.ID, rfErr)
		}

		// consecutive_failures in the DB is now priorFailures+1. Compare against threshold.
		newFails := priorFailures + 1
		if newFails >= maxConsecutiveFailures {
			if qErr := s.recurringRepo.Quarantine(ctx, schedule.ID); qErr != nil {
				log.Printf("[recurring] ERROR quarantining schedule %s: %v", schedule.ID, qErr)
			} else {
				log.Printf("[recurring] ERROR schedule %s quarantined after %d consecutive failures: %v", schedule.ID, newFails, err)
				if s.alertFunc != nil {
					s.alertFunc(schedule.ID, err)
				}
			}
		}

		return false, fmt.Errorf("runOneSchedule createInstance for schedule %s: %w", schedule.ID, err)
	}

	// Success: reset DB failure counter so a future failure starts fresh from 0.
	if schedule.ConsecutiveFailures > 0 {
		if rfErr := s.recurringRepo.ResetConsecutiveFailures(ctx, schedule.ID); rfErr != nil {
			log.Printf("[recurring] WARNING: ResetConsecutiveFailures for schedule %s: %v", schedule.ID, rfErr)
		}
	}

	// Supersede any previous open instances of this schedule so they don't pile up
	// in review/in-progress waiting for an agent that will never pick them up again.
	if superErr := s.taskSvc.SupersedeRecurringInstances(ctx, schedule.ID, newTask.ID); superErr != nil {
		log.Printf("[recurring] WARNING: SupersedeRecurringInstances for schedule %s: %v", schedule.ID, superErr)
	}

	// Atomically update instance_count, last_triggered_at, next_run_at.
	if err := s.recurringRepo.IncrementInstance(ctx, schedule.ID, nextRun); err != nil {
		return false, fmt.Errorf("runOneSchedule IncrementInstance for schedule %s: %w", schedule.ID, err)
	}

	return true, nil
}

// createInstance creates a task for the given schedule and run time.
// It renders templates, fetches previous-instance context, and calls TaskService.Create().
func (s *recurringService) createInstance(ctx context.Context, schedule *domain.RecurringSchedule, runAt time.Time) (*domain.Task, error) {
	instanceNumber := schedule.InstanceCount + 1

	// Fetch previous instance summary for template variable and notification payload.
	var prevSummaryStr string
	var prevSummary *domain.RecurringInstanceSummary
	if schedule.InstanceCount > 0 {
		prevSummary = s.getPreviousInstanceSummary(ctx, schedule.ID)
		if prevSummary != nil && prevSummary.LastComment != nil {
			prevSummaryStr = *prevSummary.LastComment
		}
	}

	loc := time.UTC
	if schedule.Timezone != "" {
		if l, err := time.LoadLocation(schedule.Timezone); err == nil {
			loc = l
		}
	}

	data := buildTemplateData(runAt, loc, instanceNumber, prevSummaryStr)

	title, err := renderTemplate(schedule.TitleTemplate, data)
	if err != nil {
		return nil, fmt.Errorf("createInstance renderTemplate title: %w", err)
	}

	description := ""
	if schedule.DescriptionTemplate != "" {
		description, err = renderTemplate(schedule.DescriptionTemplate, data)
		if err != nil {
			return nil, fmt.Errorf("createInstance renderTemplate description: %w", err)
		}
	}

	// Defense in depth: strip any invalid UTF-8 byte sequences from the rendered
	// fields before the INSERT so Postgres never sees SQLSTATE 22021 regardless
	// of what upstream data (PrevSummary, template author input) contained.
	title = strings.ToValidUTF8(title, "")
	description = strings.ToValidUTF8(description, "")

	// Resolve status: use schedule's status_id or fall back to project default.
	var statusID uuid.UUID
	if schedule.StatusID != nil {
		statusID = *schedule.StatusID
	} else {
		defaultStatus, err := s.taskSvc.GetDefaultStatus(ctx, schedule.ProjectID)
		if err != nil || defaultStatus == nil {
			return nil, fmt.Errorf("createInstance GetDefaultStatus for project %s: %w", schedule.ProjectID, err)
		}
		statusID = defaultStatus.ID
	}

	assigneeID := schedule.AssigneeID
	assigneeType := schedule.AssigneeType
	// Recurring instances must not be assigned to human users — they belong to
	// agents. Strip the user assignee so the instance is left unassigned and
	// gets picked up by the normal agent dispatch flow.
	if assigneeType == domain.AssigneeTypeUser {
		assigneeID = nil
		assigneeType = domain.AssigneeTypeUnassigned
	}

	task := &domain.Task{
		ID:                      uuid.New(),
		ProjectID:               schedule.ProjectID,
		StatusID:                statusID,
		Title:                   title,
		Description:             description,
		AssigneeID:              assigneeID,
		AssigneeType:            assigneeType,
		AssignedBy:              domain.AssignmentSourceSystem,
		Priority:                schedule.Priority,
		Labels:                  schedule.Labels,
		CreatedBy:               schedule.CreatedBy,
		CreatedByType:           domain.ActorTypeSystem,
		RecurringScheduleID:     &schedule.ID,
		RecurringInstanceNumber: &instanceNumber,
	}

	if err := s.taskSvc.Create(ctx, task); err != nil {
		return nil, fmt.Errorf("createInstance taskSvc.Create: %w", err)
	}

	// Persist the recurring fields (task_repo.Create does not yet write them — we update separately).
	// The task.RecurringScheduleID and task.RecurringInstanceNumber are written via Update
	// since the base Create query doesn't include those columns yet.
	task.RecurringScheduleID = &schedule.ID
	task.RecurringInstanceNumber = &instanceNumber

	return task, nil
}

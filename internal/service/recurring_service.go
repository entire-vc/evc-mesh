package service

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"sync"
	"text/template"
	"time"

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
	// Truncate PrevSummary to 500 chars for template variable.
	if summary.LastComment != nil && len(*summary.LastComment) > 500 {
		truncated := (*summary.LastComment)[:500]
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

	runAt := time.Now()
	if schedule.NextRunAt != nil {
		runAt = *schedule.NextRunAt
	}

	// Create the task instance.
	if _, err := s.createInstance(ctx, schedule, runAt); err != nil {
		return false, fmt.Errorf("runOneSchedule createInstance for schedule %s: %w", schedule.ID, err)
	}

	// Compute next_run_at from NOW() — not from previous next_run_at — so missed ticks
	// don't cause multiple firings (one instance per RunDue call per schedule).
	nextRun, err := computeNextRun(schedule.CronExpr, schedule.Timezone, time.Now())
	if err != nil {
		log.Printf("[recurring] WARNING: computeNextRun for schedule %s failed: %v", schedule.ID, err)
		nextRun = nil
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

	task := &domain.Task{
		ID:                      uuid.New(),
		ProjectID:               schedule.ProjectID,
		StatusID:                statusID,
		Title:                   title,
		Description:             description,
		AssigneeID:              schedule.AssigneeID,
		AssigneeType:            schedule.AssigneeType,
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

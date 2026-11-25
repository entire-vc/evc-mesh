package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// GateIntakeStore is what the intake needs from storage
// (postgres.GateIntakeRepo).
type GateIntakeStore interface {
	PrincipalByTokenHash(ctx context.Context, tokenHash string) (*domain.GateIntakePrincipal, error)
	GetReceipt(ctx context.Context, principalID, approvalID uuid.UUID) (*domain.GateIntakeReceipt, error)
	InsertReceiptTx(ctx context.Context, tx *sql.Tx, rec *domain.GateIntakeReceipt, setBy uuid.UUID) (bool, error)
}

// GateIntakeStatusSource returns a project's default task status.
type GateIntakeStatusSource interface {
	GetDefaultForProject(ctx context.Context, projectID uuid.UUID) (*domain.TaskStatus, error)
}

// GateIntakeService opens gate cards for gate-intake principals: exactly one
// card per (principal, approval_id), whatever the number of deliveries.
type GateIntakeService struct {
	store    GateIntakeStore
	tasks    TaskService
	statuses GateIntakeStatusSource
}

// NewGateIntakeService creates a GateIntakeService.
func NewGateIntakeService(store GateIntakeStore, tasks TaskService, statuses GateIntakeStatusSource) *GateIntakeService {
	return &GateIntakeService{store: store, tasks: tasks, statuses: statuses}
}

// AuthenticateGateIntake resolves a token to its live principal. Every failure
// — malformed, unknown, revoked, rotated out after the grace — is the same
// domain.ErrGateIntakeUnauthorized; a storage failure is returned as is.
func (s *GateIntakeService) AuthenticateGateIntake(ctx context.Context, token string) (*domain.GateIntakePrincipal, error) {
	if !domain.GateIntakeTokenWellFormed(token) {
		return nil, domain.ErrGateIntakeUnauthorized
	}
	p, err := s.store.PrincipalByTokenHash(ctx, domain.HashGateIntakeToken(token))
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, domain.ErrGateIntakeUnauthorized
	}
	return p, nil
}

// errGateIntakeReceiptExists aborts the card's creation transaction when the
// approval already has a receipt.
var errGateIntakeReceiptExists = errors.New("gate intake: receipt exists")

// Intake opens the gate card of req, or answers from the approval's receipt.
// It returns the response and the HTTP status: 201 for a new card, 200 for a
// replay of the same fingerprint. A different fingerprint for a stored approval
// is a 409 approval_payload_conflict and writes nothing.
func (s *GateIntakeService) Intake(ctx context.Context, p *domain.GateIntakePrincipal, req *domain.GateIntakeRequest) (*domain.GateIntakeResponse, int, error) {
	sha := req.RequestSHA256()
	// Fast path for a replay; the receipt INSERT below stays the authority.
	if rec, err := s.store.GetReceipt(ctx, p.ID, req.ApprovalID); err != nil {
		return nil, 0, err
	} else if rec != nil {
		return replayOrConflict(rec, req, sha)
	}

	status, err := s.statuses.GetDefaultForProject(ctx, p.ProjectID)
	if err != nil {
		return nil, 0, fmt.Errorf("gate intake: default status: %w", err)
	}
	if status == nil {
		return nil, 0, apierror.InternalError("the gate project has no default status")
	}
	assignee := p.AssigneeAgentID
	labels := append([]string{}, p.Labels...)
	task := &domain.Task{
		ID:            uuid.New(),
		ProjectID:     p.ProjectID,
		StatusID:      status.ID,
		Title:         req.Title(),
		Description:   req.Description(sha),
		AssigneeID:    &assignee,
		AssigneeType:  domain.AssigneeTypeAgent,
		Priority:      p.Priority,
		Labels:        pq.StringArray(labels),
		CreatedBy:     p.ID,
		CreatedByType: domain.ActorTypeSystem,
	}
	var resp *domain.GateIntakeResponse
	hook := func(ctx context.Context, tx *sql.Tx) error {
		r := &domain.GateIntakeResponse{
			Schema: domain.GateIntakeResponseSchema, ApprovalID: req.ApprovalID, TaskID: task.ID,
			ProjectID: p.ProjectID, RequestSHA256: sha, CreatedAt: task.CreatedAt.UTC().Truncate(time.Microsecond),
		}
		body, merr := json.Marshal(r)
		if merr != nil {
			return merr
		}
		inserted, ierr := s.store.InsertReceiptTx(ctx, tx, &domain.GateIntakeReceipt{
			PrincipalID: p.ID, ApprovalID: req.ApprovalID, RequestSHA256: sha, TaskID: task.ID,
			Response: body, CreatedAt: r.CreatedAt,
		}, p.ID)
		if ierr != nil {
			return ierr
		}
		if !inserted {
			return errGateIntakeReceiptExists
		}
		resp = r
		return nil
	}
	cctx := actorctx.WithActor(ctx, p.ID, domain.ActorTypeSystem)
	cctx = actorctx.WithActorName(cctx, p.Name)
	cctx = withoutDuplicateTitleHint(cctx)
	cctx = repository.WithTaskCreateTxHook(cctx, task.ID, hook)
	err = s.tasks.Create(cctx, task)
	if errors.Is(err, errGateIntakeReceiptExists) {
		// Another delivery of this approval committed first (a concurrent
		// request, or one whose answer was lost): answer from its receipt.
		rec, rerr := s.store.GetReceipt(ctx, p.ID, req.ApprovalID)
		if rerr != nil {
			return nil, 0, rerr
		}
		if rec == nil {
			return nil, 0, fmt.Errorf("gate intake: receipt for approval %s vanished", req.ApprovalID)
		}
		return replayOrConflict(rec, req, sha)
	}
	if err != nil {
		return nil, 0, err
	}
	if resp == nil {
		return nil, 0, errors.New("gate intake: the card was created without its receipt")
	}
	return resp, http.StatusCreated, nil
}

func replayOrConflict(rec *domain.GateIntakeReceipt, req *domain.GateIntakeRequest, sha string) (*domain.GateIntakeResponse, int, error) {
	if rec.RequestSHA256 != sha {
		return nil, 0, &apierror.Error{
			Code:      http.StatusConflict,
			ErrorCode: domain.GateIntakeCodePayloadConflict,
			Message:   "this approval already opened a gate card with a different payload",
			Context: map[string]any{
				"approval_id":             req.ApprovalID,
				"task_id":                 rec.TaskID,
				"request_sha256":          sha,
				"existing_request_sha256": rec.RequestSHA256,
			},
		}
	}
	var resp domain.GateIntakeResponse
	if err := json.Unmarshal(rec.Response, &resp); err != nil {
		return nil, 0, fmt.Errorf("gate intake: stored response: %w", err)
	}
	resp.Replayed = true
	return &resp, http.StatusOK, nil
}

type skipDuplicateTitleHintKey struct{}

// withoutDuplicateTitleHint turns off the duplicate-title label of
// TaskService.Create: a gate card's labels are fixed by its principal, and its
// uniqueness is already guaranteed by the receipt.
func withoutDuplicateTitleHint(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipDuplicateTitleHintKey{}, true)
}

func skipDuplicateTitleHint(ctx context.Context) bool {
	v, _ := ctx.Value(skipDuplicateTitleHintKey{}).(bool)
	return v
}

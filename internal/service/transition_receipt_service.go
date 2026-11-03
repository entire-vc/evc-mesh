package service

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TransitionReceiptStore is the persistence side of the receipt-bound transition.
type TransitionReceiptStore interface {
	GetPolicy(ctx context.Context, projectID uuid.UUID) (*domain.TransitionReceiptPolicy, error)
	UpsertPolicy(ctx context.Context, p *domain.TransitionReceiptPolicy, actor uuid.UUID) (*domain.TransitionReceiptPolicy, error)
	CommitProductDone(ctx context.Context, in domain.TransitionCommitInput) (*domain.TransitionReceipt, bool, error)
	ListReceipts(ctx context.Context, taskID uuid.UUID) ([]domain.TransitionReceipt, error)
	CreateAcceptanceIntent(ctx context.Context, in domain.AcceptanceIntentInput) (*domain.AcceptanceIntent, error)
	RevokeAcceptanceIntent(ctx context.Context, taskID, intentID, actor uuid.UUID, reason string) error
	ListAcceptanceIntents(ctx context.Context, taskID uuid.UUID) ([]domain.AcceptanceIntent, error)
	GetProductDoneAcceptance(ctx context.Context, taskID, actor uuid.UUID) (map[string]any, error)
}

// TransitionReceiptService is the opt-in product done path. It has no system
// or label bypass: the credential actor must be an allowed producer under the
// project's policy, whatever its type.
type TransitionReceiptService struct {
	store TransitionReceiptStore
	tasks repository.TaskRepository
}

func NewTransitionReceiptService(store TransitionReceiptStore, tasks repository.TaskRepository) *TransitionReceiptService {
	return &TransitionReceiptService{store: store, tasks: tasks}
}

func (s *TransitionReceiptService) ProductDone(ctx context.Context, taskID uuid.UUID, req domain.ProductDoneRequest) (*domain.TransitionReceipt, bool, error) {
	actorID, actorType := actorctx.FromContext(ctx)
	if actorID == uuid.Nil {
		return nil, false, apierror.Unauthorized("an authenticated actor is required")
	}
	if err := req.Validate(); err != nil {
		return nil, false, apierror.BadRequest(err.Error())
	}
	if req.PayloadDigestV1 == "" {
		// Direct callers (no raw body): digest the struct form.
		if raw, jerr := json.Marshal(req); jerr == nil {
			req.PayloadDigestV1, _ = domain.PayloadDigestV1(raw)
		}
	}
	in := domain.TransitionCommitInput{TaskID: taskID, Request: req, ActorID: actorID, ActorType: actorType}

	// Required DoD gates apply here exactly as on MoveTask. The store evaluates
	// them inside the commit transaction (settings under a share lock).
	in.DoDBlocking = dodBlockingGates
	task, err := s.tasks.GetByID(ctx, taskID)
	if err != nil {
		return nil, false, err
	}
	if task == nil {
		return nil, false, apierror.NotFound("Task")
	}
	return s.store.CommitProductDone(ctx, in)
}

func (s *TransitionReceiptService) GetPolicy(ctx context.Context, projectID uuid.UUID) (*domain.TransitionReceiptPolicy, error) {
	return s.store.GetPolicy(ctx, projectID)
}

func (s *TransitionReceiptService) SetPolicy(ctx context.Context, p *domain.TransitionReceiptPolicy) (*domain.TransitionReceiptPolicy, error) {
	actorID, _ := actorctx.FromContext(ctx)
	if actorID == uuid.Nil {
		return nil, apierror.Unauthorized("an authenticated actor is required")
	}
	return s.store.UpsertPolicy(ctx, p, actorID)
}

func (s *TransitionReceiptService) ListReceipts(ctx context.Context, taskID uuid.UUID) ([]domain.TransitionReceipt, error) {
	return s.store.ListReceipts(ctx, taskID)
}

// RecordAcceptance stores the calling credential's acceptance of one result.
// The acceptor identity is the authenticated actor, never a request field.
func (s *TransitionReceiptService) RecordAcceptance(ctx context.Context, taskID uuid.UUID, req domain.AcceptanceIntentRequest) (*domain.AcceptanceIntent, error) {
	actorID, actorType := actorctx.FromContext(ctx)
	if actorID == uuid.Nil {
		return nil, apierror.Unauthorized("an authenticated actor is required")
	}
	if err := req.Validate(); err != nil {
		return nil, apierror.BadRequest(err.Error())
	}
	return s.store.CreateAcceptanceIntent(ctx, domain.AcceptanceIntentInput{TaskID: taskID, Request: req, ActorID: actorID, ActorType: actorType})
}

func (s *TransitionReceiptService) RevokeAcceptance(ctx context.Context, taskID, intentID uuid.UUID, reason string) error {
	actorID, _ := actorctx.FromContext(ctx)
	if actorID == uuid.Nil {
		return apierror.Unauthorized("an authenticated actor is required")
	}
	if reason == "" {
		return apierror.BadRequest("reason is required")
	}
	return s.store.RevokeAcceptanceIntent(ctx, taskID, intentID, actorID, reason)
}

func (s *TransitionReceiptService) ListAcceptances(ctx context.Context, taskID uuid.UUID) ([]domain.AcceptanceIntent, error) {
	return s.store.ListAcceptanceIntents(ctx, taskID)
}

// ProductDoneAcceptance is the acceptance record the product-done producer reads
// before it transitions; the credential actor is the authenticated caller.
func (s *TransitionReceiptService) ProductDoneAcceptance(ctx context.Context, taskID uuid.UUID) (map[string]any, error) {
	actorID, _ := actorctx.FromContext(ctx)
	if actorID == uuid.Nil {
		return nil, apierror.Unauthorized("an authenticated actor is required")
	}
	return s.store.GetProductDoneAcceptance(ctx, taskID, actorID)
}

// Capabilities is the discovery record for the calling credential.
func (s *TransitionReceiptService) Capabilities(ctx context.Context, projectID uuid.UUID) (domain.ProductDoneCapabilities, error) {
	actorID, _ := actorctx.FromContext(ctx)
	if actorID == uuid.Nil {
		return domain.ProductDoneCapabilities{}, apierror.Unauthorized("an authenticated actor is required")
	}
	p, err := s.store.GetPolicy(ctx, projectID)
	if err != nil {
		return domain.ProductDoneCapabilities{}, err
	}
	return p.Capabilities(actorID), nil
}

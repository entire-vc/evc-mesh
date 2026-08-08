package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type inviteService struct {
	inviteRepo    repository.WorkspaceInviteRepository
	userRepo      repository.UserRepository
	memberRepo    repository.WorkspaceMemberRepository
	workspaceRepo repository.WorkspaceRepository
	emailSvc      EmailService
	authSvc       *auth.Service
	baseURL       string
}

// NewInviteService creates a new WorkspaceInviteService.
func NewInviteService(
	inviteRepo repository.WorkspaceInviteRepository,
	userRepo repository.UserRepository,
	memberRepo repository.WorkspaceMemberRepository,
	workspaceRepo repository.WorkspaceRepository,
	emailSvc EmailService,
	authSvc *auth.Service,
	baseURL string,
) WorkspaceInviteService {
	return &inviteService{
		inviteRepo:    inviteRepo,
		userRepo:      userRepo,
		memberRepo:    memberRepo,
		workspaceRepo: workspaceRepo,
		emailSvc:      emailSvc,
		authSvc:       authSvc,
		baseURL:       baseURL,
	}
}

// deliver sends an invitation email and classifies the outcome.
//
// A send failure never propagates as an error to the caller: the invite row is
// already committed and its link works, so failing the whole request would
// throw away a valid invite over a mail problem. The outcome is reported
// instead, and the API hands the link back so the inviter can deliver it.
func (s *inviteService) deliver(ctx context.Context, toEmail, workspaceName, inviteURL string) InviteDelivery {
	d := InviteDelivery{URL: inviteURL}

	err := s.emailSvc.SendInvite(ctx, toEmail, workspaceName, inviteURL)
	switch {
	case err == nil:
		d.Status = InviteDeliverySent
	case errors.Is(err, ErrEmailNotConfigured):
		d.Status = InviteDeliveryNotConfigured
	default:
		d.Status = InviteDeliveryFailed
		d.Error = err.Error()
		// Configured-but-broken mail is the case that used to vanish entirely:
		// the error was assigned to _ and the API still answered 201. Record it
		// where an operator can find it, without the token.
		log.Printf("invite_service: sending invitation email to %s failed: %v", toEmail, err)
	}
	return d
}

func (s *inviteService) CreateInvite(ctx context.Context, input CreateInviteInput) (*CreateInviteResult, error) {
	// Store the invite against the canonical address so that accepting it
	// resolves to the same account a normal login would.
	input.Email = auth.NormalizeEmail(input.Email)
	if input.Email == "" {
		return nil, apierror.ValidationError(map[string]string{"email": "email is required"})
	}
	if !isValidRole(input.Role) {
		input.Role = domain.RoleMember
	}

	ws, err := s.workspaceRepo.GetByID(ctx, input.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("invite_service.CreateInvite: %w", err)
	}
	if ws == nil {
		return nil, apierror.NotFound("Workspace")
	}

	token, err := generateToken()
	if err != nil {
		return nil, fmt.Errorf("invite_service.CreateInvite: failed to generate token: %w", err)
	}

	var invitedByPtr *uuid.UUID
	if input.InvitedBy != uuid.Nil {
		invitedByPtr = &input.InvitedBy
	}

	now := time.Now()
	invite := &domain.WorkspaceInvite{
		ID:          uuid.New(),
		WorkspaceID: input.WorkspaceID,
		Email:       input.Email,
		Role:        input.Role,
		Token:       token,
		InvitedBy:   invitedByPtr,
		ExpiresAt:   now.Add(7 * 24 * time.Hour),
		CreatedAt:   now,
	}

	if err := s.inviteRepo.Create(ctx, invite); err != nil {
		return nil, fmt.Errorf("invite_service.CreateInvite: %w", err)
	}

	inviteURL := fmt.Sprintf("%s/accept-invite/%s", s.baseURL, token)

	return &CreateInviteResult{
		Invite:   invite,
		Delivery: s.deliver(ctx, input.Email, ws.Name, inviteURL),
	}, nil
}

func (s *inviteService) ListInvites(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceInvite, error) {
	return s.inviteRepo.ListByWorkspace(ctx, workspaceID)
}

func (s *inviteService) ResendInvite(ctx context.Context, workspaceID, inviteID uuid.UUID) (InviteDelivery, error) {
	invite, err := s.inviteRepo.GetByID(ctx, inviteID)
	if err != nil {
		return InviteDelivery{}, fmt.Errorf("invite_service.ResendInvite: %w", err)
	}
	if invite == nil || invite.WorkspaceID != workspaceID {
		return InviteDelivery{}, apierror.NotFound("WorkspaceInvite")
	}
	if !invite.IsPending() {
		return InviteDelivery{}, apierror.BadRequest("invite is no longer pending")
	}

	ws, err := s.workspaceRepo.GetByID(ctx, invite.WorkspaceID)
	if err != nil {
		return InviteDelivery{}, fmt.Errorf("invite_service.ResendInvite: %w", err)
	}
	if ws == nil {
		return InviteDelivery{}, apierror.NotFound("Workspace")
	}

	inviteURL := fmt.Sprintf("%s/accept-invite/%s", s.baseURL, invite.Token)
	return s.deliver(ctx, invite.Email, ws.Name, inviteURL), nil
}

// RevokeInvite deletes a pending invite of workspaceID.
//
// The workspace check is the fix for a cross-tenant delete: the route is
// /workspaces/:ws_id/invites/:invite_id, but only :invite_id ever reached this
// code, so an admin of any workspace could pass their own :ws_id with a
// stranger's :invite_id and revoke that tenant's pending invitation — 204, row
// gone, and their new hire's link stopped working. Resend was the same shape with
// an email attached.
func (s *inviteService) RevokeInvite(ctx context.Context, workspaceID, inviteID uuid.UUID) error {
	invite, err := s.inviteRepo.GetByID(ctx, inviteID)
	if err != nil {
		return fmt.Errorf("invite_service.RevokeInvite: %w", err)
	}
	if invite == nil || invite.WorkspaceID != workspaceID {
		return apierror.NotFound("WorkspaceInvite")
	}
	return s.inviteRepo.Delete(ctx, inviteID)
}

func (s *inviteService) GetByToken(ctx context.Context, token string) (*domain.WorkspaceInvite, error) {
	invite, err := s.inviteRepo.GetByToken(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("invite_service.GetByToken: %w", err)
	}
	if invite == nil || !invite.IsPending() {
		return nil, nil
	}
	return invite, nil
}

func (s *inviteService) AcceptInvite(ctx context.Context, input AcceptInviteInput) (accessToken, refreshToken string, err error) {
	invite, err := s.inviteRepo.GetByToken(ctx, input.Token)
	if err != nil {
		return "", "", fmt.Errorf("invite_service.AcceptInvite: %w", err)
	}
	if invite == nil || !invite.IsPending() {
		return "", "", apierror.BadRequest("invite is invalid or has expired")
	}

	if input.Password == "" {
		return "", "", apierror.ValidationError(map[string]string{"password": "password is required"})
	}
	if validateErr := auth.ValidatePassword(input.Password); validateErr != nil {
		return "", "", validateErr
	}

	// Invites created before email normalization landed may still carry a
	// mixed-case or padded address; canonicalize on read so lookup, creation
	// and the closing login all agree on one identity.
	email := auth.NormalizeEmail(invite.Email)

	// Falling back to the address is a last resort, not the normal path. It is
	// how a whole instance ends up with every member displayed as their own
	// address: the accept form never asked, so this line answered for them. The
	// form asks now; this stays only so a client that omits the field still gets
	// a usable account — and it is recorded as not-self-chosen, so a workspace
	// admin can still fill it in afterwards.
	name := strings.TrimSpace(input.Name)
	selfNamed := name != ""
	if !selfNamed {
		name = email
	}
	if len([]rune(name)) > 100 {
		return "", "", apierror.ValidationError(map[string]string{"name": "name must be at most 100 characters"})
	}

	// Get or create user.
	user, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		return "", "", fmt.Errorf("invite_service.AcceptInvite: %w", err)
	}

	if user == nil {
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(input.Password), 10)
		if hashErr != nil {
			return "", "", apierror.InternalError("failed to hash password")
		}
		// users.username is NOT NULL with a slug CHECK constraint, so a new
		// account needs a username derived the same way self-registration
		// derives it. Delegating to auth.Service keeps exactly one
		// implementation of that rule.
		username, unameErr := s.authSvc.DeriveUsername(ctx, email)
		if unameErr != nil {
			log.Printf("invite_service.AcceptInvite: derive username for %s: %v", email, unameErr)
			return "", "", apierror.InternalError("could not allocate a username for this account")
		}
		now := time.Now()
		user = &domain.User{
			ID:                 uuid.New(),
			Email:              email,
			Name:               name,
			Username:           username,
			PasswordHash:       string(hash),
			IsActive:           true,
			DisplayNameSelfSet: selfNamed,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		if createErr := s.userRepo.Create(ctx, user); createErr != nil {
			// Never surface the raw driver/constraint text to the client.
			var pqErr *pq.Error
			if errors.As(createErr, &pqErr) && pqErr.Code == "23505" {
				return "", "", apierror.Conflict("an account with this email or username already exists")
			}
			log.Printf("invite_service.AcceptInvite: create user %s: %v", email, createErr)
			return "", "", apierror.InternalError("could not create an account for this invite")
		}
	}

	// Add to workspace if not already a member.
	existing, err := s.memberRepo.GetByWorkspaceAndUser(ctx, invite.WorkspaceID, user.ID)
	if err != nil {
		return "", "", fmt.Errorf("invite_service.AcceptInvite: %w", err)
	}
	if existing == nil {
		now := time.Now()
		member := &domain.WorkspaceMember{
			ID:          uuid.New(),
			WorkspaceID: invite.WorkspaceID,
			UserID:      user.ID,
			Role:        invite.Role,
			InvitedBy:   invite.InvitedBy,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if createErr := s.memberRepo.Create(ctx, member); createErr != nil {
			return "", "", fmt.Errorf("invite_service.AcceptInvite: %w", createErr)
		}
	}

	// Mark invite accepted.
	if acceptErr := s.inviteRepo.Accept(ctx, invite.ID); acceptErr != nil {
		return "", "", fmt.Errorf("invite_service.AcceptInvite: %w", acceptErr)
	}

	// Issue JWT via login.
	_, tokens, err := s.authSvc.Login(ctx, email, input.Password)
	if err != nil {
		return "", "", fmt.Errorf("invite_service.AcceptInvite: %w", err)
	}
	return tokens.AccessToken, tokens.RefreshToken, nil
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func isValidRole(role string) bool {
	switch role {
	case domain.RoleAdmin, domain.RoleMember, domain.RoleViewer:
		return true
	}
	return false
}

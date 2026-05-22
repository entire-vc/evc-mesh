package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
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

func (s *inviteService) CreateInvite(ctx context.Context, input CreateInviteInput) (*domain.WorkspaceInvite, error) {
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
	_ = s.emailSvc.SendInvite(ctx, input.Email, ws.Name, inviteURL)

	return invite, nil
}

func (s *inviteService) ListInvites(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceInvite, error) {
	return s.inviteRepo.ListByWorkspace(ctx, workspaceID)
}

func (s *inviteService) ResendInvite(ctx context.Context, inviteID uuid.UUID) error {
	invite, err := s.inviteRepo.GetByID(ctx, inviteID)
	if err != nil {
		return fmt.Errorf("invite_service.ResendInvite: %w", err)
	}
	if invite == nil {
		return apierror.NotFound("WorkspaceInvite")
	}
	if !invite.IsPending() {
		return apierror.BadRequest("invite is no longer pending")
	}

	ws, err := s.workspaceRepo.GetByID(ctx, invite.WorkspaceID)
	if err != nil {
		return fmt.Errorf("invite_service.ResendInvite: %w", err)
	}
	if ws == nil {
		return apierror.NotFound("Workspace")
	}

	inviteURL := fmt.Sprintf("%s/accept-invite/%s", s.baseURL, invite.Token)
	return s.emailSvc.SendInvite(ctx, invite.Email, ws.Name, inviteURL)
}

func (s *inviteService) RevokeInvite(ctx context.Context, inviteID uuid.UUID) error {
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

	name := input.Name
	if name == "" {
		name = invite.Email
	}

	// Get or create user.
	user, err := s.userRepo.GetByEmail(ctx, invite.Email)
	if err != nil {
		return "", "", fmt.Errorf("invite_service.AcceptInvite: %w", err)
	}

	if user == nil {
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(input.Password), 10)
		if hashErr != nil {
			return "", "", apierror.InternalError("failed to hash password")
		}
		now := time.Now()
		user = &domain.User{
			ID:           uuid.New(),
			Email:        invite.Email,
			Name:         name,
			PasswordHash: string(hash),
			IsActive:     true,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if createErr := s.userRepo.Create(ctx, user); createErr != nil {
			return "", "", fmt.Errorf("invite_service.AcceptInvite: %w", createErr)
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
	_, tokens, err := s.authSvc.Login(ctx, invite.Email, input.Password)
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

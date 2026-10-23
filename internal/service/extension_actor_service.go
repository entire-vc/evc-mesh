package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ExtensionTokenPrefix marks a bearer token as an extension actor token, the way
// OAuthAccessTokenPrefix marks a mot_ one. The middleware routes on it.
const ExtensionTokenPrefix = "ext_"

// ExtensionTokenRotationGrace is how long the previous token keeps working after
// POST .../rotate-token, so the extension can switch without a gap (spec 03 §4).
const ExtensionTokenRotationGrace = 5 * time.Minute

// ExtensionPrincipal is what a verified extension token resolves to.
type ExtensionPrincipal struct {
	ConnectionID uuid.UUID
	WorkspaceID  uuid.UUID
	ActorID      uuid.UUID
	ExtensionID  string
	DisplayName  string
	// Capabilities by kind, e.g. {"api": ["tasks:read"], "events": ["task.created"]}.
	Capabilities map[string][]string
}

// Principal returns the stable name `ext:<id>@<workspace>`.
func (p *ExtensionPrincipal) Principal() string {
	return "ext:" + p.ExtensionID + "@" + p.WorkspaceID.String()
}

// HasAPI reports whether the consented API capabilities include capability
// (for example "tasks:write").
func (p *ExtensionPrincipal) HasAPI(capability string) bool {
	for _, c := range p.Capabilities["api"] {
		if c == capability {
			return true
		}
	}
	return false
}

// IssuedExtensionToken is the one response that carries the raw token.
type IssuedExtensionToken struct {
	Token    string
	ActorID  uuid.UUID
	Actor    string // ext:<id>@<workspace>
	Grace    time.Duration
	Rotated  bool
	IssuedAt time.Time
}

// ExtensionActorService owns the service principal of extension connections.
type ExtensionActorService interface {
	// Issue creates (or re-arms after a revoke) the actor of a connection with
	// the capabilities consented to, in the flat form of Status.ConsentedCapabilities
	// ("api:tasks:read", "events:task.created", ...). The raw token is returned
	// once.
	Issue(ctx context.Context, workspaceID, connectionID uuid.UUID, extensionID, displayName string, consented []string) (*IssuedExtensionToken, error)
	// Rotate installs a new token; the previous one works for
	// ExtensionTokenRotationGrace more.
	Rotate(ctx context.Context, workspaceID, connectionID uuid.UUID) (*IssuedExtensionToken, error)
	// SelfRotate is the actor rotating its own token (capability
	// ext_token:self_rotate). presented is the raw token of the call: it stays
	// valid for grace (default ExtensionTokenRotationGrace) as the previous token.
	SelfRotate(ctx context.Context, p *ExtensionPrincipal, presented string, grace time.Duration) (*IssuedExtensionToken, error)
	// SelfRevokePrevious ends the transition window early. Only the current
	// token may call it; a repeat is a no-op.
	SelfRevokePrevious(ctx context.Context, p *ExtensionPrincipal, presented string) error
	// Revoke kills every token of the connection. Idempotent.
	Revoke(ctx context.Context, connectionID uuid.UUID) error
	// Authenticate resolves a raw token. Every failure is the same 401: unknown,
	// revoked, expired grace, connection disabled or workspace gone.
	Authenticate(ctx context.Context, rawToken string) (*ExtensionPrincipal, error)
	// RecordViolation counts a call outside the consented capabilities; the
	// count is part of the connection's health.
	RecordViolation(ctx context.Context, connectionID uuid.UUID, what string)
	// Get returns the actor row of a connection (nil when there is none).
	Get(ctx context.Context, connectionID uuid.UUID) (*domain.ExtensionActor, error)
}

type extensionActorService struct {
	actors       repository.ExtensionActorRepository
	integrations repository.IntegrationRepository
	workspaces   repository.WorkspaceRepository
	consents     repository.ExtensionConsentRepository
	now          func() time.Time
}

// WithConsents lets Rotate issue the first token of a connection that has no
// actor yet, from the consent recorded at connect. Without it Rotate needs an
// existing actor.
func WithConsents(s ExtensionActorService, c repository.ExtensionConsentRepository) ExtensionActorService {
	if impl, ok := s.(*extensionActorService); ok {
		impl.consents = c
	}
	return s
}

// NewExtensionActorService wires the service.
func NewExtensionActorService(
	actors repository.ExtensionActorRepository,
	integrations repository.IntegrationRepository,
	workspaces repository.WorkspaceRepository,
) ExtensionActorService {
	return &extensionActorService{actors: actors, integrations: integrations, workspaces: workspaces, now: time.Now}
}

// extensionTokenHash is the stored form of a token. A 256-bit random value has no
// dictionary to stretch, so a plain SHA-256 is the right primitive (and makes the
// lookup a single indexed equality).
func extensionTokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func newExtensionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return ExtensionTokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// CapabilitiesByKind turns the flat consented list into the stored object.
func CapabilitiesByKind(consented []string) map[string][]string {
	out := map[string][]string{}
	for _, c := range consented {
		kind, rest, ok := strings.Cut(c, ":")
		if !ok || kind == "" || rest == "" {
			continue
		}
		out[kind] = append(out[kind], rest)
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

func (s *extensionActorService) Issue(ctx context.Context, workspaceID, connectionID uuid.UUID, extensionID, displayName string, consented []string) (*IssuedExtensionToken, error) {
	conn, err := s.integrations.GetByID(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	if conn == nil || conn.WorkspaceID != workspaceID {
		return nil, apierror.NotFound("Connection")
	}
	raw, err := newExtensionToken()
	if err != nil {
		return nil, apierror.InternalError("failed to generate token")
	}
	caps, err := json.Marshal(CapabilitiesByKind(consented))
	if err != nil {
		return nil, apierror.InternalError("failed to encode capabilities")
	}
	row, err := s.actors.Issue(ctx, &domain.ExtensionActor{
		ConnectionID: connectionID, WorkspaceID: workspaceID, ActorID: uuid.New(),
		ExtensionID: extensionID, DisplayName: displayName, TokenHash: extensionTokenHash(raw), Capabilities: caps,
	})
	if err != nil {
		return nil, err
	}
	return &IssuedExtensionToken{Token: raw, ActorID: row.ActorID, Actor: row.Principal(), IssuedAt: row.IssuedAt}, nil
}

func (s *extensionActorService) Rotate(ctx context.Context, workspaceID, connectionID uuid.UUID) (*IssuedExtensionToken, error) {
	row, err := s.actors.GetByConnection(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	if row == nil && s.consents != nil {
		return s.issueFirst(ctx, workspaceID, connectionID)
	}
	if row == nil || row.WorkspaceID != workspaceID {
		return nil, apierror.NotFound("Extension actor")
	}
	if row.RevokedAt != nil {
		return nil, apierror.Conflict("extension actor is revoked; reconnect the extension to issue a new token")
	}
	raw, err := newExtensionToken()
	if err != nil {
		return nil, apierror.InternalError("failed to generate token")
	}
	ok, err := s.actors.Rotate(ctx, connectionID, extensionTokenHash(raw), s.now().Add(ExtensionTokenRotationGrace))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apierror.Conflict("extension actor is revoked; reconnect the extension to issue a new token")
	}
	return &IssuedExtensionToken{Token: raw, ActorID: row.ActorID, Actor: row.Principal(), Grace: ExtensionTokenRotationGrace, Rotated: true, IssuedAt: s.now()}, nil
}

// issueFirst issues the first token of a connection made before any actor
// existed, with the capabilities consented to at connect.
func (s *extensionActorService) issueFirst(ctx context.Context, workspaceID, connectionID uuid.UUID) (*IssuedExtensionToken, error) {
	conn, err := s.integrations.GetByID(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	if conn == nil || conn.WorkspaceID != workspaceID {
		return nil, apierror.NotFound("Extension actor")
	}
	id := string(conn.Provider)
	consent, err := s.consents.GetConsent(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	if consent == nil {
		return nil, apierror.NotFound("Extension actor")
	}
	return s.Issue(ctx, workspaceID, connectionID, id, id, consent.Capabilities)
}

// ExtensionSelfRotateMaxGrace bounds the transition window a caller may ask for.
const ExtensionSelfRotateMaxGrace = time.Hour

func (s *extensionActorService) SelfRotate(ctx context.Context, p *ExtensionPrincipal, presented string, grace time.Duration) (*IssuedExtensionToken, error) {
	if p == nil || !strings.HasPrefix(presented, ExtensionTokenPrefix) {
		return nil, apierror.Forbidden("only an extension actor can rotate its own token")
	}
	if grace <= 0 {
		grace = ExtensionTokenRotationGrace
	}
	if grace > ExtensionSelfRotateMaxGrace {
		return nil, apierror.BadRequest("grace_seconds must be at most 3600")
	}
	raw, err := newExtensionToken()
	if err != nil {
		return nil, apierror.InternalError("failed to generate token")
	}
	now := s.now()
	ok, err := s.actors.RotateSelf(ctx, p.ConnectionID, extensionTokenHash(presented), extensionTokenHash(raw), now.Add(grace), now)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apierror.Unauthorized("invalid extension token")
	}
	return &IssuedExtensionToken{Token: raw, ActorID: p.ActorID, Actor: p.Principal(), Grace: grace, Rotated: true, IssuedAt: now}, nil
}

func (s *extensionActorService) SelfRevokePrevious(ctx context.Context, p *ExtensionPrincipal, presented string) error {
	if p == nil || !strings.HasPrefix(presented, ExtensionTokenPrefix) {
		return apierror.Forbidden("only an extension actor can revoke its own previous token")
	}
	ok, err := s.actors.DropPrevious(ctx, p.ConnectionID, extensionTokenHash(presented))
	if err != nil {
		return err
	}
	if !ok {
		return apierror.Conflict("call this with the current token, not the previous one")
	}
	return nil
}

func (s *extensionActorService) Revoke(ctx context.Context, connectionID uuid.UUID) error {
	_, err := s.actors.Revoke(ctx, connectionID)
	return err
}

func (s *extensionActorService) Authenticate(ctx context.Context, rawToken string) (*ExtensionPrincipal, error) {
	deny := apierror.Unauthorized("invalid extension token")
	if !strings.HasPrefix(rawToken, ExtensionTokenPrefix) {
		return nil, deny
	}
	row, err := s.actors.GetByTokenHash(ctx, extensionTokenHash(rawToken), s.now())
	if err != nil {
		return nil, err
	}
	if row == nil || row.RevokedAt != nil {
		return nil, deny
	}
	// The connection must still exist, be active and belong to the workspace the
	// token names: disabling a connection is effective even if nothing called
	// Revoke, and a workspace that is gone takes its actors with it.
	conn, err := s.integrations.GetByID(ctx, row.ConnectionID)
	if err != nil {
		return nil, err
	}
	if conn == nil || !conn.IsActive || conn.WorkspaceID != row.WorkspaceID {
		return nil, deny
	}
	ws, err := s.workspaces.GetByID(ctx, row.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if ws == nil {
		return nil, deny
	}
	caps := map[string][]string{}
	if len(row.Capabilities) > 0 {
		if err := json.Unmarshal(row.Capabilities, &caps); err != nil {
			// Unreadable capabilities mean no capabilities, never "all".
			return nil, fmt.Errorf("extension actor %s: bad capabilities: %w", row.ConnectionID, err)
		}
	}
	return &ExtensionPrincipal{
		ConnectionID: row.ConnectionID, WorkspaceID: row.WorkspaceID, ActorID: row.ActorID,
		ExtensionID: row.ExtensionID, DisplayName: row.DisplayName, Capabilities: caps,
	}, nil
}

func (s *extensionActorService) RecordViolation(ctx context.Context, connectionID uuid.UUID, what string) {
	// Counting must never turn a 403 into a 500.
	_ = s.actors.RecordViolation(ctx, connectionID, what)
}

func (s *extensionActorService) Get(ctx context.Context, connectionID uuid.UUID) (*domain.ExtensionActor, error) {
	return s.actors.GetByConnection(ctx, connectionID)
}

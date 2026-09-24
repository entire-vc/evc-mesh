package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/oautherror"
)

const (
	oauthAccessTokenTTL  = 1 * time.Hour
	oauthRefreshTokenTTL = 30 * 24 * time.Hour
	oauthCodeTTL         = 60 * time.Second

	// OAuthAccessTokenPrefix is the prefix internal/middleware/auth.go looks
	// for on the Authorization: Bearer header to route a request through
	// AuthenticateAccessToken instead of JWT validation. Spec-mandated
	// (Mesh Doc oauth-for-remote-mesh-mcp, "Токены").
	OAuthAccessTokenPrefix = "mot_"
	// oauthRefreshTokenPrefix has no protocol meaning (refresh tokens are
	// only ever presented to /oauth/token, never to the auth middleware) —
	// it exists purely so a refresh token pasted into the wrong header is
	// visibly not an access token instead of silently failing a hash lookup.
	oauthRefreshTokenPrefix = "mor_"
	// dcrClientIDPrefix marks a server-generated (DCR) client_id so it can
	// never collide with a CIMD client_id, which is always an https:// URL.
	dcrClientIDPrefix = "mcpc_"

	oauthTokenRandomBytes = 32
	oauthCodeRandomBytes  = 32

	// oauthDefaultScope is the only scope this AS grants today (Mesh Doc:
	// "Один scope mesh (плюс offline_access) — дробить сейчас не нужно").
	oauthDefaultScope = "mesh offline_access"

	// oauthConnectorWorkspaceRole is the workspace_role an OAuth-issued
	// token resolves to — same default a connectionless legacy agent key
	// gets (agentService.legacyGrantRole). The connector agent's actual
	// permissions come from agents.role/RBAC, unchanged; this only answers
	// "which workspace_role does WorkspaceRLS see", the same question
	// agentService.Authenticate answers for agk_ keys.
	oauthConnectorWorkspaceRole = "member"

	// oauthClientCacheTTL is how long a cached CIMD document is trusted
	// before ResolveClient refetches it. Not in the spec as a number — a
	// deliberate middle ground between "never notice a client rotated its
	// redirect_uris" and "fetch the document on every single authorize
	// request".
	oauthClientCacheTTL = 1 * time.Hour

	// Retention before the periodic purge (PurgeExpired) deletes a row. Both
	// are measured from expires_at, not from the moment of use or revocation:
	// a used-but-unexpired code and a revoked-but-unexpired refresh token must
	// survive so a replay is still recognised (and its family revoked) instead
	// of reading as "never existed". The grace after expiry keeps the row a
	// little longer for post-mortems of a stolen-token report.
	oauthCodeRetention  = 24 * time.Hour
	oauthTokenRetention = 7 * 24 * time.Hour

	// oauthInvalidCodeDescription is the one client-facing text for every
	// "this code cannot be redeemed" outcome that is decided before the caller
	// has proven anything: no such code, expired, already used, verifier
	// malformed or not matching. One text means the response cannot be used to
	// probe which of those it was — in particular whether a guessed code exists.
	oauthInvalidCodeDescription = "invalid or expired authorization code"

	oauthCIMDMaxBodyBytes = 64 * 1024
	oauthCIMDFetchTimeout = 10 * time.Second
	oauthCIMDDialTimeout  = 5 * time.Second

	// PKCE (RFC 7636 §4.1): code_verifier is 43-128 chars from the
	// "unreserved" charset. code_challenge for S256 is always exactly 43
	// chars — BASE64URL-NOPAD of a 32-byte SHA-256 digest.
	pkceVerifierMinLen = 43
	pkceVerifierMaxLen = 128
	pkceChallengeLen   = 43

	// oauthConnectorNameRetries bounds getOrCreateGrant's retry loop when the
	// natural "<client_name> — <username>" agent name collides with an
	// existing agent slug in the workspace (two separate DCR registrations
	// of the same client, e.g. per-install DCR, share the same
	// client_name but get different client_ids, so the grant-lookup dedup
	// above never catches this).
	oauthConnectorNameRetries = 3
)

// ssrfDeniedCIDRs extends isPubliclyRoutable's net.IP.Is* checks with ranges
// the standard library has no built-in predicate for: CGNAT, "this network",
// benchmarking, reserved, IETF protocol assignments, and every IPv6 prefix
// that embeds (or is routed to) an IPv4 address — NAT64 (both the well-known
// /96 and the local-use /48), 6to4, and the deprecated IPv4-compatible and
// site-local ranges — so an IPv6-only dial cannot reach a private IPv4 target
// through a translation prefix.
var ssrfDeniedCIDRs = mustParseCIDRs(
	"100.64.0.0/10",
	"0.0.0.0/8",
	"192.0.0.0/24",
	"198.18.0.0/15",
	"240.0.0.0/4",
	"64:ff9b::/96",
	"64:ff9b:1::/48",
	"2002::/16",
	"::/96",
	"fec0::/10",
)

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("oauth: invalid SSRF deny-list CIDR " + c + ": " + err.Error())
		}
		nets = append(nets, n)
	}
	return nets
}

// oauthDefaultGrantTypes is what a freshly registered/discovered client gets
// when it doesn't specify its own grant_types.
var oauthDefaultGrantTypes = []string{"authorization_code", "refresh_token"}

type oauthService struct {
	repo                repository.OAuthRepository
	agentService        AgentService
	userRepo            repository.UserRepository
	workspaceRepo       repository.WorkspaceRepository
	workspaceMemberRepo repository.WorkspaceMemberRepository
	agentGrantRepo      repository.AgentWorkspaceGrantRepository
	httpClient          *http.Client
	authCache           *oauthAuthCache
}

// NewOAuthService constructs the OAuth 2.0 Authorization Server service.
func NewOAuthService(
	repo repository.OAuthRepository,
	agentService AgentService,
	userRepo repository.UserRepository,
	workspaceRepo repository.WorkspaceRepository,
	workspaceMemberRepo repository.WorkspaceMemberRepository,
	agentGrantRepo repository.AgentWorkspaceGrantRepository,
) OAuthService {
	return &oauthService{
		repo:                repo,
		agentService:        agentService,
		userRepo:            userRepo,
		workspaceRepo:       workspaceRepo,
		workspaceMemberRepo: workspaceMemberRepo,
		agentGrantRepo:      agentGrantRepo,
		httpClient:          newCIMDHTTPClient(),
		authCache:           newOAuthAuthCache(oauthAuthCacheTTL),
	}
}

// SetHTTPClientForTesting implements OAuthServiceConfigurable — see its doc
// comment in interfaces.go for why this exists and why it is test-only.
func (s *oauthService) SetHTTPClientForTesting(client *http.Client) {
	s.httpClient = client
}

// SetAuthCacheTTLForTesting implements OAuthServiceConfigurable. A zero TTL
// disables the cache.
func (s *oauthService) SetAuthCacheTTLForTesting(ttl time.Duration) {
	s.authCache = newOAuthAuthCache(ttl)
}

// PurgeExpired deletes authorization codes and tokens that expired more than
// their retention ago. Idempotent; safe to run concurrently with live traffic —
// nothing it removes can still be redeemed or presented.
func (s *oauthService) PurgeExpired(ctx context.Context) (codes, tokens int64, err error) {
	now := timeNow()
	codes, cerr := s.repo.DeleteExpiredCodes(ctx, now.Add(-oauthCodeRetention))
	tokens, terr := s.repo.DeleteExpiredTokens(ctx, now.Add(-oauthTokenRetention))
	return codes, tokens, errors.Join(cerr, terr)
}

// --- Client registration (DCR + CIMD) ---

func (s *oauthService) RegisterClientDCR(ctx context.Context, in DCRRegisterInput) (*domain.OAuthClient, *oautherror.Error) {
	if len(in.RedirectURIs) == 0 {
		return nil, oautherror.InvalidClientMetadata("redirect_uris is required and must not be empty")
	}
	if oerr := checkClientMetadataLimits(in.ClientName, in.RedirectURIs); oerr != nil {
		return nil, oerr
	}
	for _, ru := range in.RedirectURIs {
		if !validRedirectURI(ru) {
			return nil, oautherror.InvalidClientMetadata("invalid redirect_uri (must be https://, or http:// on loopback, with no fragment): " + ru)
		}
	}
	// This AS only ever issues public clients (PKCE S256 is mandatory on
	// every code exchange instead of a client secret) — see the migration's
	// package doc comment. An explicit request for anything else is a
	// registration this AS cannot satisfy, not a silent downgrade.
	if in.TokenEndpointAuthMethod != "" && in.TokenEndpointAuthMethod != "none" {
		return nil, oautherror.InvalidClientMetadata(
			`token_endpoint_auth_method must be "none" — this server only issues public clients`)
	}
	grantTypes := in.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = oauthDefaultGrantTypes
	}

	rawSuffix, err := randomHex(16)
	if err != nil {
		return nil, oautherror.ServerError("failed to generate client_id")
	}
	now := timeNow()
	metadata, _ := json.Marshal(in)
	c := &domain.OAuthClient{
		ID:                      uuid.New(),
		ClientID:                dcrClientIDPrefix + rawSuffix,
		RegistrationType:        "dcr",
		ClientName:              in.ClientName,
		RedirectURIs:            in.RedirectURIs,
		TokenEndpointAuthMethod: "none",
		GrantTypes:              grantTypes,
		Metadata:                metadata,
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := s.repo.CreateClient(ctx, c); err != nil {
		return nil, oautherror.ServerError("failed to register client: " + err.Error())
	}
	return c, nil
}

// cimdDocument is the subset of a Client ID Metadata Document this AS reads.
type cimdDocument struct {
	ClientID     string   `json:"client_id"`
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
	GrantTypes   []string `json:"grant_types"`
}

func (s *oauthService) ResolveClient(ctx context.Context, clientID string) (*domain.OAuthClient, *oautherror.Error) {
	if clientID == "" {
		return nil, oautherror.InvalidRequest("client_id is required")
	}

	existing, err := s.repo.GetClientByClientID(ctx, clientID)
	if err != nil {
		return nil, oautherror.ServerError(err.Error())
	}

	isCIMDShaped := strings.HasPrefix(clientID, "https://")

	if existing != nil {
		if existing.IsCIMD() && s.cimdStale(existing) {
			doc, oerr := s.fetchCIMD(ctx, clientID)
			if oerr != nil {
				// A CIMD client whose document can no longer be verified is
				// not authorized to proceed on cached trust — fail closed,
				// same posture as a client we'd never seen before.
				return nil, oerr
			}
			s.applyCIMDDoc(existing, doc)
			if err := s.repo.UpdateClientMetadata(ctx, existing); err != nil {
				return nil, oautherror.ServerError(err.Error())
			}
		}
		return existing, nil
	}

	if !isCIMDShaped {
		return nil, oautherror.InvalidClient("unknown client_id")
	}

	doc, oerr := s.fetchCIMD(ctx, clientID)
	if oerr != nil {
		return nil, oerr
	}
	now := timeNow()
	rawDoc, _ := json.Marshal(doc)
	grantTypes := doc.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = oauthDefaultGrantTypes
	}
	c := &domain.OAuthClient{
		ID:                      uuid.New(),
		ClientID:                clientID,
		RegistrationType:        "cimd",
		ClientName:              doc.ClientName,
		RedirectURIs:            doc.RedirectURIs,
		TokenEndpointAuthMethod: "none",
		GrantTypes:              grantTypes,
		Metadata:                rawDoc,
		MetadataFetchedAt:       &now,
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	if err := s.repo.CreateClient(ctx, c); err != nil {
		// Likely a race between two concurrent first-sights of the same
		// CIMD URL (unique index on client_id) — the other writer won,
		// serve what it wrote instead of failing this request.
		if again, err2 := s.repo.GetClientByClientID(ctx, clientID); err2 == nil && again != nil {
			return again, nil
		}
		return nil, oautherror.ServerError("failed to register CIMD client: " + err.Error())
	}
	return c, nil
}

func (s *oauthService) cimdStale(c *domain.OAuthClient) bool {
	if c.MetadataFetchedAt == nil {
		return true
	}
	return timeNow().Sub(*c.MetadataFetchedAt) > oauthClientCacheTTL
}

func (s *oauthService) applyCIMDDoc(c *domain.OAuthClient, doc *cimdDocument) {
	now := timeNow()
	c.ClientName = doc.ClientName
	c.RedirectURIs = doc.RedirectURIs
	if len(doc.GrantTypes) > 0 {
		c.GrantTypes = doc.GrantTypes
	}
	rawDoc, _ := json.Marshal(doc)
	c.Metadata = rawDoc
	c.MetadataFetchedAt = &now
	c.UpdatedAt = now
}

// fetchCIMD downloads and parses a Client ID Metadata Document. clientIDURL
// must be an https:// URL (enforced here, not just by isCIMDShaped's prefix
// check, since a caller could reach this via a cache-refresh path too).
func (s *oauthService) fetchCIMD(ctx context.Context, clientIDURL string) (*cimdDocument, *oautherror.Error) {
	u, err := url.Parse(clientIDURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, oautherror.InvalidClientMetadata("client_id must be an https:// URL")
	}

	ctx, cancel := context.WithTimeout(ctx, oauthCIMDFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientIDURL, http.NoBody)
	if err != nil {
		return nil, oautherror.InvalidClientMetadata("could not build request for client_id URL")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		// The transport error names the address the dialer refused or the
		// resolver failed on — to an unauthenticated caller that is an oracle
		// for which internal hosts resolve and to what. Fixed text out, detail
		// to the server log.
		log.Printf("oauth: CIMD fetch for %q failed: %v", clientIDURL, err)
		return nil, oautherror.InvalidClient("failed to fetch client metadata document")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, oautherror.InvalidClient(fmt.Sprintf("client metadata document fetch returned HTTP %d", resp.StatusCode))
	}

	limited := io.LimitReader(resp.Body, oauthCIMDMaxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, oautherror.InvalidClient("failed to read client metadata document")
	}
	if len(body) > oauthCIMDMaxBodyBytes {
		return nil, oautherror.InvalidClientMetadata("client metadata document exceeds size limit")
	}

	var doc cimdDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, oautherror.InvalidClientMetadata("client metadata document is not valid JSON")
	}
	// CIMD requires the document's own client_id to equal the URL it was
	// fetched from — otherwise the URL is not proof of anything, since any
	// https server could vouch for any other client_id. Required, not
	// merely checked-when-present: an absent client_id is just as
	// unverifiable as a mismatched one.
	if doc.ClientID == "" || doc.ClientID != clientIDURL {
		return nil, oautherror.InvalidClientMetadata("client metadata document client_id is missing or does not match its own URL")
	}
	if len(doc.RedirectURIs) == 0 {
		return nil, oautherror.InvalidClientMetadata("client metadata document has no redirect_uris")
	}
	if oerr := checkClientMetadataLimits(doc.ClientName, doc.RedirectURIs); oerr != nil {
		return nil, oerr
	}
	for _, ru := range doc.RedirectURIs {
		if !validRedirectURI(ru) {
			return nil, oautherror.InvalidClientMetadata("client metadata document has an invalid redirect_uri (must be https://, or http:// on loopback, with no fragment)")
		}
	}
	return &doc, nil
}

// newCIMDHTTPClient builds the http.Client used only for outbound CIMD
// fetches — never redirects (a redirect target would need the exact same
// SSRF check re-applied, and refusing is simpler and no less correct for a
// document fetch), and dials only through ssrfSafeDialContext.
func newCIMDHTTPClient() *http.Client {
	return &http.Client{
		Timeout: oauthCIMDFetchTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext:         ssrfSafeDialContext,
			TLSHandshakeTimeout: oauthCIMDDialTimeout,
		},
	}
}

// ssrfSafeDialContext is a net.Dialer.DialContext that refuses to connect to
// any address that is not publicly routable, checked AFTER DNS resolution
// (via net.Dialer.Control, which runs on the actual resolved IP right before
// connect) rather than by pre-resolving the hostname ourselves — a
// pre-resolve check is vulnerable to DNS rebinding (the name can resolve
// differently between the check and the real connection); Control closes
// that gap because it sees the exact address the connection is about to be
// made to.
func ssrfSafeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d := &net.Dialer{
		Timeout: oauthCIMDDialTimeout,
		Control: func(_, address string, c syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("refusing to dial non-IP address %q", host)
			}
			if !isPubliclyRoutable(ip) {
				return fmt.Errorf("refusing to dial non-public address %s", ip)
			}
			return nil
		},
	}
	return d.DialContext(ctx, network, address)
}

// isPubliclyRoutable blocks the standard SSRF target list: loopback
// (127.0.0.0/8, ::1), RFC1918/ULA private ranges, link-local (including the
// 169.254.169.254 cloud metadata address), multicast, and unspecified
// (0.0.0.0, ::).
func isPubliclyRoutable(ip net.IP) bool {
	switch {
	case ip.IsLoopback(),
		ip.IsPrivate(),
		ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(),
		ip.IsUnspecified(),
		ip.IsMulticast():
		return false
	}
	for _, n := range ssrfDeniedCIDRs {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// --- Authorize + consent ---

func (s *oauthService) ValidateAuthorize(ctx context.Context, p AuthorizeParams) *AuthorizeValidation {
	if p.ClientID == "" {
		return &AuthorizeValidation{ClientErr: oautherror.InvalidRequest("client_id is required")}
	}
	client, oerr := s.ResolveClient(ctx, p.ClientID)
	if oerr != nil {
		return &AuthorizeValidation{ClientErr: oerr}
	}
	if p.RedirectURI == "" || !redirectURIAllowed(p.RedirectURI, client.RedirectURIs) {
		return &AuthorizeValidation{ClientErr: oautherror.InvalidRequest("redirect_uri is missing or not registered for this client")}
	}

	v := &AuthorizeValidation{Client: client, RedirectURI: p.RedirectURI}
	if p.ResponseType != "code" {
		v.RequestErr = oautherror.UnsupportedResponseType(`only response_type=code is supported`)
		return v
	}
	if p.CodeChallengeMethod != "S256" || !validPKCEChallenge(p.CodeChallenge) {
		v.RequestErr = oautherror.InvalidRequest("PKCE code_challenge with code_challenge_method=S256 is required")
		return v
	}
	return v
}

func (s *oauthService) ConsentInfo(ctx context.Context, userID uuid.UUID, p AuthorizeParams) (*ConsentInfo, *oautherror.Error) {
	v := s.ValidateAuthorize(ctx, p)
	if v.ClientErr != nil {
		return nil, v.ClientErr
	}
	if v.RequestErr != nil {
		return nil, v.RequestErr
	}

	workspaces, err := s.workspaceRepo.ListForUser(ctx, userID)
	if err != nil {
		return nil, oautherror.ServerError(err.Error())
	}

	ru, _ := url.Parse(v.RedirectURI)
	host := ""
	if ru != nil {
		host = ru.Host
	}

	scope := oauthDefaultScope
	if p.Scope != "" {
		scope = p.Scope
	}

	return &ConsentInfo{
		ClientName:      v.Client.ClientName,
		RedirectURI:     v.RedirectURI,
		RedirectHost:    host,
		LoopbackWarning: allRedirectURIsLoopback(v.Client.RedirectURIs),
		Scope:           scope,
		Workspaces:      workspaces,
	}, nil
}

func (s *oauthService) Decide(ctx context.Context, in ConsentDecisionInput) (string, error) {
	v := s.ValidateAuthorize(ctx, in.AuthorizeParams)
	if v.ClientErr != nil {
		return "", v.ClientErr
	}
	if v.RequestErr != nil {
		return appendRedirectError(v.RedirectURI, v.RequestErr.Code, in.State), nil
	}
	if !in.Allow {
		return appendRedirectError(v.RedirectURI, "access_denied", in.State), nil
	}

	role, isMember, err := s.resolveMemberRole(ctx, in.WorkspaceID, in.UserID)
	if err != nil {
		// Not apierror.Wrap: it copies err.Error() (driver/SQL text) into the
		// response's details field.
		log.Printf("oauth: consent membership lookup for user %s workspace %s failed: %v", in.UserID, in.WorkspaceID, err)
		return "", apierror.InternalError("failed to verify workspace membership")
	}
	if !isMember {
		return "", apierror.Forbidden("you are not a member of that workspace")
	}
	// A viewer holds no write permissions in this workspace at all
	// (permissionMatrix[RoleViewer] is empty) — consenting anyway would
	// create a connector agent that outward-facing middleware then has
	// nothing to clamp it down FROM, since agentPerms (the set a fixed
	// agent identity gets) has always been bigger than viewer's. Refuse at
	// the source instead of relying solely on the per-request clamp.
	if role == domain.RoleViewer {
		return "", apierror.Forbidden("a viewer cannot connect an external application — ask a workspace admin or owner")
	}

	grant, err := s.getOrCreateGrant(ctx, in.UserID, v.Client, in.WorkspaceID)
	if err != nil {
		return "", err
	}

	rawCode, err := randomHex(oauthCodeRandomBytes)
	if err != nil {
		return "", oautherror.ServerError("failed to generate authorization code")
	}
	now := timeNow()
	code := &domain.OAuthAuthorizationCode{
		ID:                  uuid.New(),
		CodeHash:            sha256Hex(rawCode),
		ClientID:            v.Client.ClientID,
		RedirectURI:         v.RedirectURI,
		CodeChallenge:       in.CodeChallenge,
		CodeChallengeMethod: "S256",
		GrantID:             grant.ID,
		ExpiresAt:           now.Add(oauthCodeTTL),
		CreatedAt:           now,
	}
	if err := s.repo.CreateCode(ctx, code); err != nil {
		return "", oautherror.ServerError(err.Error())
	}

	return appendRedirectCode(v.RedirectURI, rawCode, in.State), nil
}

// resolveMemberRole mirrors WorkspaceRepo.ListForUser's own rule (owner OR
// workspace_members row) rather than trusting the members table alone — an
// owner whose auto-membership insert never landed (a known, documented
// possibility, see ListForUser's doc comment) must still be able to consent
// into their own workspace. Returns isMember=false (not an error) when
// userID has no standing in workspaceID at all, or the workspace itself is
// gone — the two states Decide, AuthenticateAccessToken, and
// RefreshTokenGrant all need to treat identically: the connector's access
// no longer has a live human behind it.
func (s *oauthService) resolveMemberRole(ctx context.Context, workspaceID, userID uuid.UUID) (role string, isMember bool, err error) {
	ws, err := s.workspaceRepo.GetByID(ctx, workspaceID)
	if err != nil {
		return "", false, err
	}
	if ws == nil {
		return "", false, nil
	}
	if ws.OwnerID == userID {
		return domain.RoleOwner, true, nil
	}
	member, err := s.workspaceMemberRepo.GetByWorkspaceAndUser(ctx, workspaceID, userID)
	if err != nil {
		return "", false, err
	}
	if member == nil {
		return "", false, nil
	}
	return member.Role, true, nil
}

// getOrCreateGrant finds the user's existing (client, workspace) grant,
// reactivating it if revoked, or creates a brand-new one — registering a
// fresh connector agent ("<client_name> — <username>", SupervisorUserID set
// to the consenting user) the first time this pair is ever consented to.
//
// An existing grant is only reusable while the connector agent behind it is
// still usable (alive AND holding an active workspace connection). If an
// admin deleted the agent or revoked its connection, reusing the grant would
// make every consent "succeed" and every resulting mot_ token 401 — with no
// way out from the API, since the grant row itself is the thing that is
// stuck. In that case the grant is retargeted at a freshly registered agent.
func (s *oauthService) getOrCreateGrant(ctx context.Context, userID uuid.UUID, client *domain.OAuthClient, workspaceID uuid.UUID) (*domain.OAuthGrant, error) {
	existing, err := s.repo.GetGrantByUserClientWorkspace(ctx, userID, client.ClientID, workspaceID)
	if err != nil {
		return nil, oautherror.ServerError(err.Error())
	}

	if existing != nil {
		usable, uerr := s.connectorAgentUsable(ctx, existing)
		if uerr != nil {
			return nil, oautherror.ServerError(uerr.Error())
		}
		if usable {
			if existing.IsRevoked() {
				if reactivateErr := s.repo.ReactivateGrant(ctx, existing.ID); reactivateErr != nil {
					return nil, oautherror.ServerError(reactivateErr.Error())
				}
				existing.RevokedAt = nil
			}
			return existing, nil
		}
	}

	agentName, nerr := s.connectorAgentName(ctx, userID, client)
	if nerr != nil {
		return nil, nerr
	}
	reg, regErr := s.registerConnectorAgent(ctx, workspaceID, agentName, userID)
	if regErr != nil {
		return nil, regErr
	}

	if existing != nil {
		// Tokens minted under the dead agent must not silently start
		// authenticating as the new one once the grant points at it.
		now := timeNow()
		if rerr := s.repo.RevokeTokensByGrant(ctx, existing.ID, now); rerr != nil {
			return nil, oautherror.ServerError(rerr.Error())
		}
		terr := s.repo.RetargetGrant(ctx, existing.ID, reg.Agent.ID)
		s.authCache.evictGrant(existing.ID)
		if terr != nil {
			return nil, oautherror.ServerError(terr.Error())
		}
		existing.AgentID = reg.Agent.ID
		existing.RevokedAt = nil
		return existing, nil
	}

	g := &domain.OAuthGrant{
		ID:          uuid.New(),
		UserID:      userID,
		ClientID:    client.ClientID,
		WorkspaceID: workspaceID,
		AgentID:     reg.Agent.ID,
		Scope:       oauthDefaultScope,
		CreatedAt:   timeNow(),
	}
	if err := s.repo.CreateGrant(ctx, g); err != nil {
		return nil, oautherror.ServerError(err.Error())
	}
	return g, nil
}

// connectorAgentName builds the "<client_name> — <username>" display name.
func (s *oauthService) connectorAgentName(ctx context.Context, userID uuid.UUID, client *domain.OAuthClient) (string, *oautherror.Error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return "", oautherror.ServerError(err.Error())
	}
	if user == nil {
		return "", oautherror.ServerError("consenting user not found")
	}
	displayName := client.ClientName
	if strings.TrimSpace(displayName) == "" {
		displayName = client.ClientID
	}
	return fmt.Sprintf("%s — %s", displayName, user.Username), nil
}

// loadConnectorAgent returns the connector agent a grant authenticates as, or
// (nil, nil) when that agent is not usable right now: deleted, or with its
// workspace connection (agent_workspace_grants row) missing or revoked.
//
// The second condition is the one that matters for admin control: an admin
// revokes a connection through DELETE /workspaces/:ws_id/agent-grants/:id and
// the documented meaning is "this agent no longer authenticates" —
// agentService.Authenticate enforces that for agk_ keys, so a mot_ token must
// honour it too, or revoking a connector's agent-side connection would
// silently do nothing for the OAuth path. A lookup failure is an error, not
// "unusable": callers must not revoke tokens on an outage.
func (s *oauthService) loadConnectorAgent(ctx context.Context, grant *domain.OAuthGrant) (*domain.Agent, error) {
	agent, err := s.agentService.GetByID(ctx, grant.AgentID)
	if err != nil {
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	if agent == nil {
		return nil, nil
	}
	if s.agentGrantRepo != nil {
		home, err := s.agentGrantRepo.GetByAgentAndWorkspace(ctx, grant.AgentID, grant.WorkspaceID)
		if err != nil {
			return nil, err
		}
		if home == nil || home.IsRevoked() {
			return nil, nil
		}
	}
	return agent, nil
}

// connectorAgentUsable is loadConnectorAgent for callers that only need the
// verdict.
func (s *oauthService) connectorAgentUsable(ctx context.Context, grant *domain.OAuthGrant) (bool, error) {
	agent, err := s.loadConnectorAgent(ctx, grant)
	return agent != nil, err
}

// registerConnectorAgent registers a new connector agent for baseName,
// retrying with a disambiguating suffix when the natural
// "<client_name> — <username>" slug collides with an existing agent in the
// workspace (uq_agents_workspace_slug). This happens for real: a DCR client
// that registers a fresh client_id per install (Claude Code's own flow does)
// gives getOrCreateGrant's GetGrantByUserClientWorkspace dedup nothing to
// match against on a second connection of "the same app", since client_id
// differs each time even though client_name does not — without this, the
// second connection surfaced a raw 500 from the unique-constraint violation
// instead of a working connector.
func (s *oauthService) registerConnectorAgent(ctx context.Context, workspaceID uuid.UUID, baseName string, supervisorUserID uuid.UUID) (*RegisterAgentOutput, *oautherror.Error) {
	name := baseName
	for attempt := 0; attempt < oauthConnectorNameRetries; attempt++ {
		uid := supervisorUserID
		reg, err := s.agentService.Register(ctx, RegisterAgentInput{
			WorkspaceID:      workspaceID,
			Name:             name,
			AgentType:        domain.AgentTypeCustom,
			SupervisorUserID: &uid,
		})
		if err == nil {
			return reg, nil
		}
		if !isNameConflict(err) {
			return nil, oautherror.ServerError(err.Error())
		}
		suffix, rerr := randomHex(2)
		if rerr != nil {
			return nil, oautherror.ServerError("failed to disambiguate connector agent name")
		}
		name = fmt.Sprintf("%s (%s)", baseName, suffix)
	}
	return nil, oautherror.ServerError("could not register connector agent: name collision persisted after retries")
}

// isSlugConflict reports whether err is the agents.uq_agents_workspace_slug
// unique-constraint violation.
func isSlugConflict(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505" && pqErr.Constraint == "uq_agents_workspace_slug"
}

// isNameConflict reports whether a Register failure is a name collision worth
// retrying with a different name, as opposed to any other error (bad input,
// DB down): either another agent already has the slug, or Register's 409
// because a workspace member's username equals it — the second is just as
// permanent for this name, so without a retry every consent would 500.
func isNameConflict(err error) bool {
	if isSlugConflict(err) {
		return true
	}
	var apiErr *apierror.Error
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusConflict
}

// --- Token endpoint ---

// buildTokenPair generates a fresh access+refresh pair for a grant. It only
// builds — persisting is the caller's job, inside the same transaction as the
// step that authorizes issuing it (code redemption or refresh rotation), so a
// pair can never exist without the redemption/rotation that justified it.
func (s *oauthService) buildTokenPair(grantID, familyID uuid.UUID, parentRefreshID *uuid.UUID) (*TokenResponse, []*domain.OAuthToken, *oautherror.Error) {
	now := timeNow()

	rawAccessSuffix, err := randomHex(oauthTokenRandomBytes)
	if err != nil {
		return nil, nil, oautherror.ServerError("failed to generate access token")
	}
	rawAccess := OAuthAccessTokenPrefix + rawAccessSuffix
	accessRow := &domain.OAuthToken{
		ID:        uuid.New(),
		GrantID:   grantID,
		TokenType: domain.OAuthTokenTypeAccess,
		TokenHash: sha256Hex(rawAccess),
		FamilyID:  familyID,
		ExpiresAt: now.Add(oauthAccessTokenTTL),
		CreatedAt: now,
	}

	rawRefreshSuffix, err := randomHex(oauthTokenRandomBytes)
	if err != nil {
		return nil, nil, oautherror.ServerError("failed to generate refresh token")
	}
	rawRefresh := oauthRefreshTokenPrefix + rawRefreshSuffix
	refreshRow := &domain.OAuthToken{
		ID:            uuid.New(),
		GrantID:       grantID,
		TokenType:     domain.OAuthTokenTypeRefresh,
		TokenHash:     sha256Hex(rawRefresh),
		FamilyID:      familyID,
		ParentTokenID: parentRefreshID,
		ExpiresAt:     now.Add(oauthRefreshTokenTTL),
		CreatedAt:     now,
	}

	return &TokenResponse{
		AccessToken:  rawAccess,
		TokenType:    "Bearer",
		ExpiresIn:    int(oauthAccessTokenTTL.Seconds()),
		RefreshToken: rawRefresh,
		Scope:        oauthDefaultScope,
	}, []*domain.OAuthToken{accessRow, refreshRow}, nil
}

func (s *oauthService) ExchangeCode(ctx context.Context, clientID, redirectURI, code, codeVerifier string) (*TokenResponse, *oautherror.Error) {
	if clientID == "" || code == "" || codeVerifier == "" {
		return nil, oautherror.InvalidRequest("client_id, code, and code_verifier are required")
	}
	if !validPKCEVerifier(codeVerifier) {
		return nil, oautherror.InvalidGrant(oauthInvalidCodeDescription)
	}

	row, err := s.repo.GetCodeByHash(ctx, sha256Hex(code))
	if err != nil {
		return nil, oautherror.ServerError(err.Error())
	}
	if row == nil {
		return nil, oautherror.InvalidGrant(oauthInvalidCodeDescription)
	}

	now := timeNow()
	if !row.IsUsable(now) {
		// A replay of an already-used code is itself evidence the code
		// leaked — revoke whatever it already produced, not just this
		// attempt (RFC 6749 §10.5).
		if row.UsedAt != nil && row.IssuedFamilyID != nil {
			s.revokeFamilyLogged(ctx, *row.IssuedFamilyID, now)
		}
		return nil, oautherror.InvalidGrant(oauthInvalidCodeDescription)
	}
	if row.ClientID != clientID {
		// Same text as "no such code": a distinct one would confirm to whoever
		// holds a client_id that this exact code exists and is still live.
		return nil, oautherror.InvalidGrant(oauthInvalidCodeDescription)
	}
	if oerr := s.requireClientGrantType(ctx, clientID, oauthGrantTypeAuthorizationCode); oerr != nil {
		return nil, oerr
	}
	if row.RedirectURI != redirectURI {
		return nil, oautherror.InvalidGrant("redirect_uri does not match the one used to obtain this code")
	}
	if row.CodeChallengeMethod != "S256" || !verifyPKCE(codeVerifier, row.CodeChallenge) {
		return nil, oautherror.InvalidGrant(oauthInvalidCodeDescription)
	}

	grant, err := s.repo.GetGrantByID(ctx, row.GrantID)
	if err != nil {
		return nil, oautherror.ServerError(err.Error())
	}
	if grant == nil || grant.IsRevoked() {
		return nil, oautherror.InvalidGrant("access for this authorization has been revoked")
	}

	// The connector must still be usable at redemption time — a code minted
	// before an admin removed the agent or cut its workspace connection must
	// not turn into fresh tokens.
	usable, uerr := s.connectorAgentUsable(ctx, grant)
	if uerr != nil {
		return nil, oautherror.ServerError(uerr.Error())
	}
	if !usable {
		return nil, oautherror.InvalidGrant("access for this authorization has been revoked")
	}

	familyID := uuid.New()
	resp, rows, oerr := s.buildTokenPair(grant.ID, familyID, nil)
	if oerr != nil {
		return nil, oerr
	}
	// Redeem the code and persist the pair in one transaction: the code is
	// only burned if the client actually gets tokens for it, and a concurrent
	// replay cannot slip between "code marked used" and "tokens exist".
	ok, err := s.repo.RedeemCode(ctx, row.ID, now, familyID, rows)
	if err != nil {
		return nil, oautherror.ServerError(err.Error())
	}
	if !ok {
		// Lost a redemption race against a concurrent exchange of the exact
		// same code — same outward response as any other replay. The winner's
		// family is the one to kill: a second redemption attempt is evidence
		// the code leaked.
		if fresh, gerr := s.repo.GetCodeByHash(ctx, sha256Hex(code)); gerr == nil && fresh != nil && fresh.IssuedFamilyID != nil {
			s.revokeFamilyLogged(ctx, *fresh.IssuedFamilyID, now)
		}
		return nil, oautherror.InvalidGrant(oauthInvalidCodeDescription)
	}
	return resp, nil
}

func (s *oauthService) RefreshTokenGrant(ctx context.Context, clientID, refreshToken string) (*TokenResponse, *oautherror.Error) {
	if clientID == "" || refreshToken == "" {
		return nil, oautherror.InvalidRequest("client_id and refresh_token are required")
	}
	if !strings.HasPrefix(refreshToken, oauthRefreshTokenPrefix) {
		return nil, oautherror.InvalidGrant("unknown refresh token")
	}

	row, err := s.repo.GetTokenByHash(ctx, sha256Hex(refreshToken))
	if err != nil {
		return nil, oautherror.ServerError(err.Error())
	}
	if row == nil || row.TokenType != domain.OAuthTokenTypeRefresh {
		return nil, oautherror.InvalidGrant("unknown refresh token")
	}

	now := timeNow()
	if row.RevokedAt != nil {
		// Reuse of an already-rotated-away refresh token: kill the whole
		// chain — RFC 6749's recommended response to detected refresh-token
		// reuse.
		s.revokeFamilyLogged(ctx, row.FamilyID, now)
		return nil, oautherror.InvalidGrant("refresh token has already been used — all tokens for this authorization have been revoked")
	}
	if !now.Before(row.ExpiresAt) {
		return nil, oautherror.InvalidGrant("refresh token expired")
	}

	grant, err := s.repo.GetGrantByID(ctx, row.GrantID)
	if err != nil {
		return nil, oautherror.ServerError(err.Error())
	}
	if grant == nil || grant.IsRevoked() {
		return nil, oautherror.InvalidGrant("access for this authorization has been revoked")
	}
	if grant.ClientID != clientID {
		return nil, oautherror.InvalidGrant("refresh token was not issued to this client")
	}
	if oerr := s.requireClientGrantType(ctx, clientID, oauthGrantTypeRefreshToken); oerr != nil {
		return nil, oerr
	}
	// The user who consented to this grant must still belong to the
	// workspace right now — not just at consent time — otherwise a token
	// issued before their removal keeps working for up to
	// oauthRefreshTokenTTL (30 days) after they lose access.
	// Only a confirmed non-member revokes the family. A failed lookup
	// refuses this one request and leaves the family alone: revoking on an
	// outage would permanently disconnect every user who refreshed during it.
	_, isMember, merr := s.resolveMemberRole(ctx, grant.WorkspaceID, grant.UserID)
	if merr != nil {
		log.Printf("oauth: refresh membership lookup failed: %v", merr)
		return nil, oautherror.ServerError("membership lookup failed")
	}
	if !isMember {
		s.revokeFamilyLogged(ctx, row.FamilyID, now)
		return nil, oautherror.InvalidGrant("access for this authorization has been revoked")
	}

	// The connector agent must still exist and still hold its workspace
	// connection. Same policy as the membership check above: only a confirmed
	// "gone/revoked" kills the family, a failed lookup refuses this request only.
	usable, uerr := s.connectorAgentUsable(ctx, grant)
	if uerr != nil {
		return nil, oautherror.ServerError(uerr.Error())
	}
	if !usable {
		s.revokeFamilyLogged(ctx, row.FamilyID, now)
		return nil, oautherror.InvalidGrant("access for this authorization has been revoked")
	}

	parentID := row.ID
	resp, rows, oerr := s.buildTokenPair(grant.ID, row.FamilyID, &parentID)
	if oerr != nil {
		return nil, oerr
	}
	// Revoke the presented token and persist its replacement in ONE
	// transaction. Two failure modes this closes: (1) a failed insert after a
	// committed revoke strands the client with a dead refresh token, and its
	// retry is then read as reuse and kills the family; (2) two concurrent
	// refreshes both passing the RevokedAt check above cannot both commit —
	// the loser's guarded UPDATE affects no row.
	rotated, err := s.repo.RotateRefreshToken(ctx, row.ID, now, rows)
	if err != nil {
		return nil, oautherror.ServerError(err.Error())
	}
	if !rotated {
		// Lost the race: a concurrent refresh of this exact token already
		// claimed it. Treat exactly like a presented already-revoked token
		// (RFC 6749's reuse-detection response), not like a transient error.
		s.revokeFamilyLogged(ctx, row.FamilyID, now)
		return nil, oautherror.InvalidGrant("refresh token has already been used — all tokens for this authorization have been revoked")
	}
	return resp, nil
}

// revokeFamilyLogged kills a token family on a security-relevant path (reuse
// detected, connector gone). The client-facing answer is invalid_grant either
// way, so a failure here cannot change the response — but it must not vanish:
// a family that survives its own revocation is a live stolen token.
func (s *oauthService) revokeFamilyLogged(ctx context.Context, familyID uuid.UUID, now time.Time) {
	err := s.repo.RevokeFamily(ctx, familyID, now)
	// Evicted even when the revoke failed: at worst the next request re-reads
	// the database, whereas a stale entry would keep serving a family that was
	// just found compromised.
	s.authCache.evictFamily(familyID)
	if err != nil {
		log.Printf("oauth: revoking token family %s failed: %v", familyID, err)
	}
}

const (
	oauthGrantTypeAuthorizationCode = "authorization_code"
	oauthGrantTypeRefreshToken      = "refresh_token"
)

// requireClientGrantType enforces that the /oauth/token grant_type is one the
// client registered for (RFC 7591 grant_types; RFC 6749 §5.2 unauthorized_client).
// Read from the stored client row — no CIMD refetch on the token path. A client
// that registered only "authorization_code" therefore cannot refresh.
func (s *oauthService) requireClientGrantType(ctx context.Context, clientID, grantType string) *oautherror.Error {
	client, err := s.repo.GetClientByClientID(ctx, clientID)
	if err != nil {
		return oautherror.ServerError(err.Error())
	}
	if client == nil {
		return oautherror.InvalidClient("unknown client")
	}
	for _, gt := range client.GrantTypes {
		if gt == grantType {
			return nil
		}
	}
	return oautherror.UnauthorizedClient("this client is not registered for the " + grantType + " grant type")
}

func (s *oauthService) RevokeToken(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	row, err := s.repo.GetTokenByHash(ctx, sha256Hex(token))
	if err != nil {
		return err
	}
	if row == nil {
		// RFC 7009 §2.2: an invalid/unknown token is not an error response.
		return nil
	}
	// RFC 7009 §2.1: revoking a refresh token SHOULD invalidate every token
	// issued alongside/after it — here, every token sharing its FamilyID
	// (the paired access token, and anything a later rotation produced).
	// Revoking a bare access token stays scoped to itself.
	if row.TokenType == domain.OAuthTokenTypeRefresh {
		revokeErr := s.repo.RevokeFamily(ctx, row.FamilyID, timeNow())
		s.authCache.evictFamily(row.FamilyID)
		return revokeErr
	}
	_, err = s.repo.RevokeToken(ctx, row.ID, timeNow())
	s.authCache.evictToken(sha256Hex(token))
	return err
}

// --- mot_ access-token authentication (consumed by the auth middleware) ---

func (s *oauthService) AuthenticateAccessToken(ctx context.Context, rawToken string) (*domain.Agent, error) {
	if !strings.HasPrefix(rawToken, OAuthAccessTokenPrefix) {
		return nil, apierror.Unauthorized("invalid access token")
	}
	tokenHash := sha256Hex(rawToken)
	// Snapshot BEFORE any read below: put refuses to store the answer if a
	// revocation evicted anything while this request was in flight.
	gen := s.authCache.generation()
	if cached, grantID, ok := s.authCache.get(tokenHash, timeNow()); ok {
		// One primary-key lookup instead of the full path: revoking the grant is
		// honoured on the next request, by every replica, however it was revoked.
		// A lookup error falls through to the full path, which reports it.
		if g, gerr := s.repo.GetGrantByID(ctx, grantID); gerr == nil {
			if g == nil || g.IsRevoked() {
				s.authCache.evictToken(tokenHash)
				return nil, apierror.Unauthorized("access for this token has been revoked")
			}
			return cached, nil
		}
	}
	row, err := s.repo.GetTokenByHash(ctx, tokenHash)
	if err != nil {
		return nil, err
	}
	if row == nil || row.TokenType != domain.OAuthTokenTypeAccess || !row.IsUsable(timeNow()) {
		return nil, apierror.Unauthorized("invalid or expired access token")
	}

	grant, err := s.repo.GetGrantByID(ctx, row.GrantID)
	if err != nil {
		return nil, err
	}
	if grant == nil || grant.IsRevoked() {
		return nil, apierror.Unauthorized("access for this token has been revoked")
	}
	// The consenting user must still belong to the workspace RIGHT NOW —
	// checked on every uncached authentication, not just at consent time, so
	// removal from the workspace takes effect within oauthAuthCacheTTL instead
	// of only once the access token itself expires (up to oauthAccessTokenTTL
	// later).
	if _, isMember, merr := s.resolveMemberRole(ctx, grant.WorkspaceID, grant.UserID); merr != nil || !isMember {
		return nil, apierror.Unauthorized("access for this token has been revoked")
	}

	// The agent behind the grant must be alive and hold an ACTIVE workspace
	// connection (agent_workspace_grants) — the same test
	// agentService.Authenticate applies to agk_ keys. A deleted agent is an
	// invalid token (401), not a 404 about some resource the caller never
	// asked for; a revoked connection is exactly what an admin's "cut this
	// agent off" means.
	agent, err := s.loadConnectorAgent(ctx, grant)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, apierror.Unauthorized("access for this token has been revoked")
	}

	// Copy before mutating — same reason agentService.authenticateViaGrant
	// does: the repository's contract makes no promise this pointer isn't
	// shared.
	resolved := *agent
	resolved.WorkspaceID = grant.WorkspaceID
	resolved.WorkspaceRole = oauthConnectorWorkspaceRole
	connectorUserID := grant.UserID
	resolved.OAuthConnectorUserID = &connectorUserID
	s.authCache.put(tokenHash, &resolved, grant.ID, row.FamilyID, row.ExpiresAt, timeNow(), gen)
	return &resolved, nil
}

// --- User-facing "your connected apps" API ---

func (s *oauthService) ListMyGrants(ctx context.Context, userID uuid.UUID) ([]domain.OAuthGrantWithDetails, error) {
	return s.repo.ListGrantsByUser(ctx, userID)
}

func (s *oauthService) RevokeMyGrant(ctx context.Context, userID, grantID uuid.UUID) error {
	g, err := s.repo.GetGrantByID(ctx, grantID)
	if err != nil {
		return err
	}
	if g == nil || g.UserID != userID {
		return apierror.NotFound("Grant")
	}
	now := timeNow()
	// Evict before AND after; the generation counter (see oauthAuthCache.gen)
	// is what makes a request that read the still-valid rows before the revoke
	// unable to re-insert its answer after the eviction.
	s.authCache.evictGrant(grantID)
	defer s.authCache.evictGrant(grantID)
	if err := s.repo.RevokeGrant(ctx, grantID, now); err != nil {
		return err
	}
	return s.repo.RevokeTokensByGrant(ctx, grantID, now)
}

// --- Small stateless helpers ---

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// validPKCEVerifier reports whether a code_verifier has the RFC 7636 §4.1
// shape (43-128 chars, unreserved charset) — checked before the constant-time
// comparison in verifyPKCE so a malformed verifier gets the same
// invalid_grant a mismatched one would, rather than being fed straight into
// a hash.
func validPKCEVerifier(v string) bool {
	if len(v) < pkceVerifierMinLen || len(v) > pkceVerifierMaxLen {
		return false
	}
	for _, r := range v {
		if !isPKCEUnreserved(r) {
			return false
		}
	}
	return true
}

// validPKCEChallenge reports whether a code_challenge has the shape an S256
// challenge must: exactly 43 base64url characters (no padding) — the fixed
// length of a 32-byte SHA-256 digest so encoded.
func validPKCEChallenge(c string) bool {
	if len(c) != pkceChallengeLen {
		return false
	}
	for _, r := range c {
		if !isBase64URLChar(r) {
			return false
		}
	}
	return true
}

// isBase64URLChar reports whether r is in the base64url (RFC 4648 §5)
// alphabet: ALPHA / DIGIT / "-" / "_" — no "+", "/", or padding.
func isBase64URLChar(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
}

// isPKCEUnreserved reports whether r is in RFC 7636 §4.1's code_verifier
// charset: ALPHA / DIGIT / "-" / "." / "_" / "~".
func isPKCEUnreserved(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
		r == '-' || r == '.' || r == '_' || r == '~'
}

// verifyPKCE checks a presented code_verifier against a stored S256
// code_challenge (RFC 7636 §4.6): challenge == BASE64URL-NOPAD(SHA256(verifier)).
func verifyPKCE(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

// Size limits on client-supplied registration metadata. Both registration
// paths are unauthenticated and store what they are given; client_name also
// becomes the connector agent's display name and slug.
const (
	maxClientNameRunes   = 200
	maxRedirectURIs      = 10
	maxRedirectURILength = 2048
)

func checkClientMetadataLimits(clientName string, redirectURIs []string) *oautherror.Error {
	// Control characters (NUL above all: Postgres text cannot store it, so
	// it used to surface as a 500) have no place in a display name or a URI.
	if hasControlChar(clientName) {
		return oautherror.InvalidClientMetadata("client_name contains control characters")
	}
	for _, ru := range redirectURIs {
		if hasControlChar(ru) {
			return oautherror.InvalidClientMetadata("a redirect_uri contains control characters")
		}
	}
	if utf8.RuneCountInString(clientName) > maxClientNameRunes {
		return oautherror.InvalidClientMetadata(fmt.Sprintf("client_name is longer than %d characters", maxClientNameRunes))
	}
	if len(redirectURIs) > maxRedirectURIs {
		return oautherror.InvalidClientMetadata(fmt.Sprintf("at most %d redirect_uris are allowed", maxRedirectURIs))
	}
	for _, ru := range redirectURIs {
		if len(ru) > maxRedirectURILength {
			return oautherror.InvalidClientMetadata(fmt.Sprintf("a redirect_uri is longer than %d bytes", maxRedirectURILength))
		}
	}
	return nil
}

func hasControlChar(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

// validRedirectURI reports whether a URI is acceptable as a registered
// redirect_uri (checked at DCR/CIMD registration time, so an invalid one can
// never reach Decide()'s redirect — RFC 6749 §4.1.2.1 forbids ever
// redirecting to an untrusted target). Requires https on any host, or http
// restricted to loopback (native-app local callback); rejects everything
// else, including a bare custom scheme, javascript:/data:, and any fragment
// (RFC 6749 §3.1.2 forbids one — a fragment set here could smuggle the
// authorization code past whatever the client's own redirect handler reads).
func validRedirectURI(raw string) bool {
	// url.Parse, NOT url.ParseRequestURI — the latter's doc comment says it
	// "assumes url was received in an HTTP request" and therefore "is
	// assumed not to have a #fragment suffix", so it folds a literal "#..."
	// into the path instead of populating u.Fragment, which would make the
	// very check below a no-op against exactly the input it exists to catch.
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return isLoopbackHost(u.Hostname())
	default:
		return false
	}
}

// isLoopbackHost reports whether host names the local machine by "localhost"
// or a loopback IP literal — the spec's port-insensitive redirect_uri
// exception applies only to these.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// redirectURIAllowed reports whether presented exactly matches one of
// registered, OR — when both are loopback URLs with identical
// scheme+path+query — matches ignoring port (spec: "loopback localhost/
// 127.0.0.1 — без учёта порта", the Claude Code local-callback case).
func redirectURIAllowed(presented string, registered []string) bool {
	for _, r := range registered {
		if r == presented {
			return true
		}
		if loopbackRedirectMatch(presented, r) {
			return true
		}
	}
	return false
}

func loopbackRedirectMatch(presented, registered string) bool {
	pu, err := url.Parse(presented)
	if err != nil {
		return false
	}
	ru, err := url.Parse(registered)
	if err != nil {
		return false
	}
	if pu.Scheme != ru.Scheme || pu.Path != ru.Path || pu.RawQuery != ru.RawQuery {
		return false
	}
	return isLoopbackHost(pu.Hostname()) && isLoopbackHost(ru.Hostname())
}

func allRedirectURIsLoopback(uris []string) bool {
	if len(uris) == 0 {
		return false
	}
	for _, u := range uris {
		pu, err := url.Parse(u)
		if err != nil || !isLoopbackHost(pu.Hostname()) {
			return false
		}
	}
	return true
}

func appendQuery(base string, params map[string]string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func appendRedirectCode(redirectURI, code, state string) string {
	return appendQuery(redirectURI, map[string]string{"code": code, "state": state})
}

func appendRedirectError(redirectURI, errCode, state string) string {
	return appendQuery(redirectURI, map[string]string{"error": errCode, "state": state})
}

// Compile-time proof *oauthService satisfies OAuthService.
var _ OAuthService = (*oauthService)(nil)

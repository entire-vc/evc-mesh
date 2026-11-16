package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/oautherror"
)

// ConfigureOIDC implements OIDCConfigurable.
func (s *oauthService) ConfigureOIDC(ctx context.Context, settings OIDCSettings) error {
	allowed := make(map[string]bool, len(settings.Clients))
	now := timeNow()
	for _, sc := range settings.Clients {
		metadata, _ := json.Marshal(sc)
		c := &domain.OAuthClient{
			ID:                      uuid.New(),
			ClientID:                sc.ClientID,
			RegistrationType:        domain.OAuthRegistrationStatic,
			ClientName:              sc.ClientName,
			RedirectURIs:            pq.StringArray(sc.RedirectURIs),
			TokenEndpointAuthMethod: "none",
			GrantTypes:              pq.StringArray{oauthGrantTypeAuthorizationCode},
			Metadata:                metadata,
			CreatedAt:               now,
			UpdatedAt:               now,
		}
		ok, err := s.repo.UpsertStaticClient(ctx, c)
		if err != nil {
			return errors.New("oidc: upsert static client " + sc.ClientID + ": " + err.Error())
		}
		if !ok {
			return errors.New("oidc: client_id " + sc.ClientID + " is already registered by a DCR or CIMD client")
		}
		allowed[sc.ClientID] = true
	}
	s.oidcIssuer = strings.TrimRight(settings.Issuer, "/")
	s.oidcKeys = settings.Keys
	s.oidcClients = allowed
	return nil
}

// OIDCKeys implements OAuthService: the key set, nil while OIDC is off.
func (s *oauthService) OIDCKeys() *OIDCKeySet { return s.oidcKeys }

// staticClientConfigured reports whether a static client is still in the
// current MESH_OIDC_CLIENTS. A row left behind by a client the operator
// removed from the config is not trusted.
func (s *oauthService) staticClientConfigured(clientID string) bool {
	return s.oidcClients[clientID]
}

// classifyScope applies the flow split of an internal ticket to an already
// trusted client + redirect_uri: openid without mesh is the identity flow;
// openid together with mesh, openid while OIDC is off, openid from a DCR/CIMD
// client, openid together with any other scope, and anything but openid from
// a static client are invalid_scope. The identity flow is exactly "openid":
// discovery advertises only openid and the id_token carries no profile or
// email claims, so a request for more is refused rather than silently narrowed.
func (s *oauthService) classifyScope(client *domain.OAuthClient, p AuthorizeParams) (identity bool, oerr *oautherror.Error) {
	var hasOpenID, hasMesh, hasOther bool
	for _, sc := range strings.Fields(p.Scope) {
		switch sc {
		case oidcScope:
			hasOpenID = true
		case "mesh":
			hasMesh = true
		default:
			hasOther = true
		}
	}
	switch {
	case hasOpenID && hasMesh:
		return false, oautherror.InvalidScope("openid cannot be combined with mesh in one request")
	case hasOpenID && hasOther:
		return false, oautherror.InvalidScope("openid cannot be combined with other scopes")
	case hasOpenID && s.oidcKeys == nil:
		return false, oautherror.InvalidScope("openid is not supported by this server")
	case hasOpenID && !client.IsStatic():
		return false, oautherror.InvalidScope("this client may not request openid")
	case hasOpenID:
		if len(p.Nonce) > oidcNonceMaxLen {
			return false, oautherror.InvalidRequest("nonce is too long")
		}
		return true, nil
	case client.IsStatic():
		return false, oautherror.InvalidScope("this client may only request openid")
	}
	return false, nil
}

// identityConsentInfo is ConsentInfo for the identity flow: nothing to choose,
// only who is asking and where the answer goes.
func identityConsentInfo(v *AuthorizeValidation) *ConsentInfo {
	host := ""
	if ru, err := url.Parse(v.RedirectURI); err == nil {
		host = ru.Host
	}
	return &ConsentInfo{
		ClientName:   v.Client.ClientName,
		RedirectURI:  v.RedirectURI,
		RedirectHost: host,
		Scope:        oidcScope,
		Workspaces:   []domain.Workspace{},
		IdentityOnly: true,
	}
}

// decideIdentity issues a one-time identity code after the user allowed it.
// No workspace, grant or agent: the code proves only who the user is.
func (s *oauthService) decideIdentity(ctx context.Context, v *AuthorizeValidation, in ConsentDecisionInput) (string, error) {
	rawSuffix, err := randomHex(oauthCodeRandomBytes)
	if err != nil {
		return "", oautherror.ServerError("failed to generate authorization code")
	}
	rawCode := oidcIdentityCodePrefix + rawSuffix
	now := timeNow()
	var nonce *string
	if in.Nonce != "" {
		n := in.Nonce
		nonce = &n
	}
	code := &domain.OAuthIdentityCode{
		ID:            uuid.New(),
		CodeHash:      sha256Hex(rawCode),
		ClientID:      v.Client.ClientID,
		RedirectURI:   v.RedirectURI,
		CodeChallenge: in.CodeChallenge,
		UserID:        in.UserID,
		Nonce:         nonce,
		Scope:         oidcScope,
		ExpiresAt:     now.Add(oauthCodeTTL),
		CreatedAt:     now,
	}
	if err := s.repo.CreateIdentityCode(ctx, code); err != nil {
		return "", oautherror.ServerError(err.Error())
	}
	return appendRedirectCode(v.RedirectURI, rawCode, in.State), nil
}

// exchangeIdentityCode redeems an identity code for an id_token. Every "this
// code cannot be redeemed" case answers with the same text as the MCP flow, so
// the response does not tell a guessed code from an expired or used one.
func (s *oauthService) exchangeIdentityCode(ctx context.Context, clientID, redirectURI, code, codeVerifier string) (*TokenResponse, *oautherror.Error) {
	if s.oidcKeys == nil {
		return nil, oautherror.InvalidGrant(oauthInvalidCodeDescription)
	}
	row, err := s.repo.GetIdentityCodeByHash(ctx, sha256Hex(code))
	if err != nil {
		return nil, oautherror.ServerError(err.Error())
	}
	now := timeNow()
	if row == nil || !row.IsUsable(now) || row.ClientID != clientID {
		return nil, oautherror.InvalidGrant(oauthInvalidCodeDescription)
	}
	if !s.staticClientConfigured(clientID) {
		return nil, oautherror.InvalidClient("unknown client")
	}
	if oerr := s.requireClientGrantType(ctx, clientID, oauthGrantTypeAuthorizationCode); oerr != nil {
		return nil, oerr
	}
	if row.RedirectURI != redirectURI {
		return nil, oautherror.InvalidGrant("redirect_uri does not match the one used to obtain this code")
	}
	if !verifyPKCE(codeVerifier, row.CodeChallenge) {
		return nil, oautherror.InvalidGrant(oauthInvalidCodeDescription)
	}
	user, err := s.userRepo.GetByID(ctx, row.UserID)
	if err != nil {
		return nil, oautherror.ServerError(err.Error())
	}
	if user == nil || !user.IsActive {
		return nil, oautherror.InvalidGrant("the account behind this authorization is no longer active")
	}

	nonce := ""
	if row.Nonce != nil {
		nonce = *row.Nonce
	}
	idToken, err := s.oidcKeys.signIDToken(s.oidcIssuer, row.UserID.String(), clientID, nonce, now)
	if err != nil {
		return nil, oautherror.ServerError("sign id_token: " + err.Error())
	}
	accessSuffix, err := randomHex(oauthTokenRandomBytes)
	if err != nil {
		return nil, oautherror.ServerError("failed to generate access token")
	}

	// Burn the code last: a failure above leaves it redeemable for the
	// client's retry; a concurrent second redemption loses here.
	ok, err := s.repo.MarkIdentityCodeUsed(ctx, row.ID, now)
	if err != nil {
		return nil, oautherror.ServerError(err.Error())
	}
	if !ok {
		return nil, oautherror.InvalidGrant(oauthInvalidCodeDescription)
	}
	return &TokenResponse{
		AccessToken: OIDCIdentityAccessTokenPrefix + accessSuffix,
		TokenType:   "Bearer",
		ExpiresIn:   int(oidcIDTokenTTL.Seconds()),
		Scope:       oidcScope,
		IDToken:     idToken,
	}, nil
}

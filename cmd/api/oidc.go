package main

import (
	"context"
	"log"
	"time"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// configureOIDC switches on Mesh as an OpenID Connect provider (card
// an internal ticket) when MESH_OIDC_SIGNING_KEY_FILE is set. A configured but unusable
// key or client list stops the start: turning the provider off silently would
// leave every client's sign-in broken with nothing in the log saying why.
func configureOIDC(cfg *config.Config, oauthService service.OAuthService) {
	keys, err := service.LoadOIDCKeySet(cfg.OIDC.SigningKeyFile, cfg.OIDC.PublishedKeyFiles)
	if err != nil {
		log.Fatalf("[oidc] %v", err) //nolint:gocritic // startup: a configured key must be usable
	}
	clients, err := service.ParseOIDCClients(cfg.OIDC.ClientsJSON)
	if err != nil {
		log.Fatalf("[oidc] %v", err)
	}
	configurable, ok := oauthService.(service.OIDCConfigurable)
	if !ok {
		log.Fatal("[oidc] the OAuth service cannot be configured for OpenID Connect")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = configurable.ConfigureOIDC(ctx, service.OIDCSettings{
		Issuer:  cfg.Email.BaseURL,
		Keys:    keys,
		Clients: clients,
	})
	cancel()
	if err != nil {
		log.Fatalf("[oidc] %v", err)
	}
	if keys == nil {
		if len(clients) > 0 {
			log.Printf("[oidc] WARNING: MESH_OIDC_CLIENTS lists %d client(s) but MESH_OIDC_SIGNING_KEY_FILE is not set: OpenID Connect stays off", len(clients))
		}
		return
	}
	log.Printf("[oidc] OpenID Connect on: issuer %s, signing kid %s, %d published key(s), %d static client(s)",
		cfg.Email.BaseURL, keys.ActiveKID(), len(keys.JWKS().Keys), len(clients))
}

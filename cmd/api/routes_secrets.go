package main

import (
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// registerSecretRoutes mounts secret materialization, chat-session token
// control and the write-only secrets CRUD.
func registerSecretRoutes(deps *apiDeps) {
	secretRepo := deps.secretRepo
	agentService := deps.agentService
	chatSessionService := deps.chatSessionService
	secretMaterializeHandler := deps.secretMaterializeHandler
	secretHandler := deps.secretHandler
	e := deps.e
	api := deps.api
	rbac := deps.rbac

	// Secret materialization — deliberately OUTSIDE the `api` group. DualAuth
	// there accepts a user JWT or ANY agent's API key; this route must accept
	// neither, since either would let something wielding an ordinary agent
	// identity read secret values (served from Keep). Gated by mw.SpawnerAuth() — an
	// allowlisted spawner's own agent key; see its doc comment for the trust
	// model — behind mw.DirectOnly(),
	// which 404s anything that came through a reverse proxy, so the route's
	// closure no longer rests on the edge Caddyfile alone.
	e.POST("/internal/secrets/materialize", secretMaterializeHandler.Materialize, mw.DirectOnly(), mw.SpawnerAuth(agentService))

	// Chat-session token control (E4.4): issue, renew, revoke. Runner-only, like
	// materialization: outside the `api` group so no user JWT or agent key reaches
	// it, gated by the chat controller credential behind DirectOnly.
	chatSessionHandler := handler.NewChatSessionHandler(chatSessionService)
	e.POST("/internal/chat-sessions/tokens", chatSessionHandler.Issue, mw.DirectOnly(), mw.ChatControllerAuth())
	e.POST("/internal/chat-sessions/:session_id/renew", chatSessionHandler.Renew, mw.DirectOnly(), mw.ChatControllerAuth())
	e.POST("/internal/chat-sessions/:session_id/revoke", chatSessionHandler.Revoke, mw.DirectOnly(), mw.ChatControllerAuth())

	// Write-only secrets CRUD (an internal ticket, S3). Every route is gated by
	// PermManageSecrets, which no agent key holds — see its declaration in
	// rbac.go. That includes the masked LIST: it carries a fingerprint, and
	// the point of this store is that an agent identity learns nothing about
	// a value it did not supply. Create and rotate write the value to Keep
	// Mesh keeps only the name, the Keep ref and the masked view.
	api.POST("/workspaces/:ws_id/secrets", secretHandler.Create, rbac(mw.PermManageSecrets))
	api.GET("/workspaces/:ws_id/secrets", secretHandler.List, rbac(mw.PermManageSecrets))
	api.POST("/secrets/:secret_id/rotate", secretHandler.Rotate, rbac(mw.PermManageSecrets))
	api.DELETE("/secrets/:secret_id", secretHandler.Delete, rbac(mw.PermManageSecrets))
	// Keep metadata probe (protocol mesh-keep-metadata-v1): version + length of a
	// workspace secret's current Keep value, never the value. A human session with
	// PermManageSecrets only: RequireUserAuth refuses agent keys, extensions,
	// connectors and chat tokens before the role is read. Its errors use the
	// route's closed envelope (mw.KeepMetadataEnvelope, first in the group).
	keepMetadataHandler := handler.NewKeepMetadataHandler(secretRepo, keepMetadataReader(deps.secretRouter))
	api.GET("/workspaces/:ws_id/secrets/:secret_id/keep-metadata", keepMetadataHandler.Get, mw.RequireUserAuth(), rbac(mw.PermManageSecrets))
	api.HEAD("/workspaces/:ws_id/secrets/:secret_id/keep-metadata", keepMetadataHandler.Head, mw.RequireUserAuth(), rbac(mw.PermManageSecrets))
}

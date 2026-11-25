package main

import (
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// registerLeadAcceptRoutes wires the lead-accept context (contract CH→Mesh
// lead-accept-context/v1, docs/lead-accept-context.md):
//
//	GET  /api/v1/lead-accept-context                 reader (mlr_) only
//	POST /api/v1/lead-accept-registry/attestations   publisher (mlp_) only
//
// The guard is Echo-level (e.Use) and is registered before the gate-intake
// guard (see modules.go), so on these two routes it answers every credential
// itself, an mgi_ token included, and an mlr_/mlp_ token is a 401 or 403 on
// every other route without reaching DualAuth. The routes are on v1 outside
// the protected group: their only credential is the lead-accept token.
func registerLeadAcceptRoutes(deps *apiDeps) {
	var objects service.LeadAcceptObjects
	if deps.s3Client != nil { // a typed-nil *S3Client must not become a non-nil interface
		objects = deps.s3Client
	}
	svc := service.NewLeadAcceptService(postgres.NewLeadAcceptRepo(deps.db), objects, nil)
	h := handler.NewLeadAcceptHandler(svc)
	e := deps.e
	v1 := deps.v1
	e.Use(mw.LeadAcceptGuard(svc))
	v1.GET("/lead-accept-context", h.Context, mw.RequireLeadAcceptPrincipal(domain.LeadAcceptKindReader))
	v1.POST("/lead-accept-registry/attestations", h.Attest, mw.RequireLeadAcceptPrincipal(domain.LeadAcceptKindPublisher))
}

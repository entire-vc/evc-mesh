package main

import (
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// registerGateIntakeRoutes wires gate intake (contract CH→Mesh gate-create/v1,
// docs/gate-intake.md): POST /api/v1/gate-intake for gate-intake principals.
//
// The guard is Echo-level (e.Use): it sees every request before any group's
// auth, so an mgi_ token is a 401 (invalid) or a 403 (valid, wrong route) on
// every other route and never reaches DualAuth. The route itself is on v1,
// outside the protected group: its only credential is the gate-intake token,
// required by RequireGateIntakePrincipal. No `idempotent` middleware: the
// approval receipt written with the card is the idempotency, without a TTL.
func registerGateIntakeRoutes(deps *apiDeps) {
	svc := service.NewGateIntakeService(postgres.NewGateIntakeRepo(deps.db), deps.taskService, deps.taskStatusRepo)
	gateIntakeHandler := handler.NewGateIntakeHandler(svc)
	e := deps.e
	v1 := deps.v1
	e.Use(mw.GateIntakeGuard(svc))
	v1.POST("/gate-intake", gateIntakeHandler.Create, mw.RequireGateIntakePrincipal())
}

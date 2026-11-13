package main

import (
	"github.com/entire-vc/evc-mesh/internal/handler"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// registerMemoryRoutes mounts memory, canonical updates and Spark catalog routes.
func registerMemoryRoutes(deps *apiDeps) {
	workspaceMemberRepo := deps.workspaceMemberRepo
	agentService := deps.agentService
	sparkIntegrationResolver := deps.sparkIntegrationResolver
	memoryHandler := deps.memoryHandler
	canonicalUpdatesHandler := deps.canonicalUpdatesHandler
	api := deps.api
	rbac := deps.rbac
	connectorRBAC := deps.connectorRBAC
	projAccess := deps.projAccess

	// Memory routes.
	// NOTE: fixed-path routes (/memories/search, /memories/export, /memories/import,
	// /memories/reindex, /memories/recall_graph) MUST be registered before /memories/:id
	// to avoid the literal path segments being parsed as UUID parameters.
	api.POST("/memories", memoryHandler.Remember, connectorRBAC(mw.PermWriteMemory))
	api.GET("/memories", memoryHandler.List)
	api.GET("/memories/search", memoryHandler.Search)
	api.GET("/memories/recall-final/v1", memoryHandler.FinalRecall)
	api.GET("/memories/recall_graph", memoryHandler.RecallGraph)
	api.GET("/memories/export", memoryHandler.ExportMemories)
	api.POST("/memories/import", memoryHandler.ImportMemories, connectorRBAC(mw.PermWriteMemory))
	api.POST("/memories/reindex", memoryHandler.Reindex, rbac(mw.PermMemoryIndex))
	api.POST("/memories/backfill-chunks", memoryHandler.BackfillChunks, rbac(mw.PermMemoryIndex))
	api.POST("/memories/rechunk-stale", memoryHandler.RechunkStale, rbac(mw.PermMemoryIndex))
	api.POST("/memories/backfill-doc-index", memoryHandler.BackfillDocIndex, rbac(mw.PermMemoryIndex))
	api.GET("/memories/doc-index-status", memoryHandler.DocIndexStatus)
	api.GET("/memories/by-key", memoryHandler.ByKey)
	api.GET("/memories/:id", memoryHandler.GetByID)
	api.POST("/memories/:id/archive", memoryHandler.Archive, connectorRBAC(mw.PermWriteMemory))
	api.POST("/memories/:id/unarchive", memoryHandler.Unarchive, connectorRBAC(mw.PermWriteMemory))
	api.GET("/memories/:id/related", memoryHandler.FindRelated)
	api.GET("/memories/:id/revisions", memoryHandler.Revisions)
	api.DELETE("/memories/:id", memoryHandler.Delete, connectorRBAC(mw.PermWriteMemory))
	api.GET("/projects/:proj_id/knowledge", memoryHandler.GetProjectKnowledge, projAccess)
	api.POST("/projects/:proj_id/knowledge", memoryHandler.SetProjectKnowledge, projAccess, connectorRBAC(mw.PermWriteMemory))

	// C1 canonical updates feed — returns privacy:public canonical-decision memories since a cursor.
	api.GET("/canonical_updates", canonicalUpdatesHandler.GetCanonicalUpdates)

	// Spark catalog routes — always registered now (C1: is_active must live on
	// the request path, not on whether the route exists at all). Each request
	// resolves its own base URL via sparkIntegrationResolver and refuses with a
	// named reason when Spark is disabled for the resolved workspace, rather
	// than 404ing the whole route the old cfg.Spark.Enabled gate produced.
	sparkHandler := handler.NewSparkHandler(sparkIntegrationResolver, agentService, workspaceMemberRepo)
	api.GET("/spark/agents", sparkHandler.Search)
	api.GET("/spark/agents/popular", sparkHandler.Popular)
	api.GET("/spark/agents/:agent_id", sparkHandler.GetByID)
	api.POST("/spark/agents/:agent_id/install", sparkHandler.Install)
}

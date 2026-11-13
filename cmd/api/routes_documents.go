package main

import (
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// registerDocumentRoutes mounts artifact and document routes.
func registerDocumentRoutes(deps *apiDeps) {
	cfg := deps.cfg
	artifactHandler := deps.artifactHandler
	documentHandler := deps.documentHandler
	documentExportHandler := deps.documentExportHandler
	documentAttachmentHandler := deps.documentAttachmentHandler
	documentCommentHandler := deps.documentCommentHandler
	documentWatchHandler := deps.documentWatchHandler
	api := deps.api
	rbac := deps.rbac
	wsAccess := deps.wsAccess
	idempotencyRepo := deps.idempotencyRepo
	projAccess := deps.projAccess

	// Artifact routes.
	api.GET("/tasks/:task_id/artifacts", artifactHandler.List, wsAccess)
	api.POST("/tasks/:task_id/artifacts", artifactHandler.Upload, wsAccess, rbac(mw.PermUploadArtifact),
		mw.Idempotency(idempotencyRepo, artifactIdempotencyBodyLimit(cfg.ArtifactMaxBytes)))
	api.GET("/artifacts/:artifact_id", artifactHandler.GetByID, wsAccess)
	api.GET("/artifacts/:artifact_id/download", artifactHandler.Download, wsAccess)
	api.GET("/tasks/:task_id/artifacts/:artifact_id/download", artifactHandler.Download, wsAccess)
	api.DELETE("/artifacts/:artifact_id", artifactHandler.Delete, wsAccess, rbac(mw.PermUploadArtifact))

	// Document routes. The project-scoped pair takes projAccess; the object routes
	// take wsAccess, because RequireProjectMember answers 500 on a path with no
	// :proj_id in it.
	//
	// PermUploadArtifact is the closest existing write permission: a document is
	// project content whose body is written into object storage, which is exactly
	// what that permission already covers, and it is held by the same roles as
	// create_task (owner/admin/member and agents), so nothing gains or loses reach.
	api.GET("/projects/:proj_id/documents", documentHandler.List, projAccess)
	// Static segment before the parameterised sibling routes, and under
	// /projects/:proj_id so the tenant is named by the path rather than by a
	// query parameter.
	api.GET("/projects/:proj_id/documents/search", documentHandler.Search, projAccess)
	api.POST("/projects/:proj_id/documents", documentHandler.Create, projAccess, rbac(mw.PermUploadArtifact))
	api.GET("/documents/:doc_id", documentHandler.GetByID, wsAccess)
	api.PATCH("/documents/:doc_id", documentHandler.Update, wsAccess, rbac(mw.PermUploadArtifact))
	api.DELETE("/documents/:doc_id", documentHandler.Delete, wsAccess, rbac(mw.PermUploadArtifact))

	// Reading a document in pieces, for callers that should not have to fetch the
	// whole page to work with one part of it — an agent answering a question about
	// one section otherwise pays for every section.
	//
	// All reads, so wsAccess and no rbac: whoever may GET the document may ask for
	// its outline, for a section of it, or for where a sentence in it sits.
	//
	// resolve-anchor is a POST that changes nothing. It has to be a POST — its
	// input is a quotation of up to 2000 bytes, which does not belong in a query
	// string — and it is deliberately not behind a write permission, because it
	// writes nothing. It exists because an agent has no text selection to compute a
	// comment anchor from, and computing byte offsets from text is exactly where it
	// gets them wrong: a Cyrillic quote at byte 853 is at character 475, and an
	// anchor off by that much points at a different sentence with total confidence.
	api.GET("/documents/:doc_id/outline", documentHandler.Outline, wsAccess)
	api.GET("/documents/:doc_id/section", documentHandler.Section, wsAccess)
	api.POST("/documents/:doc_id/resolve-anchor", documentHandler.ResolveAnchor, wsAccess)
	api.GET("/documents/:doc_id/export", documentExportHandler.Export, wsAccess)

	// Subscribing to a document's changes. No RBAC beyond workspace access on
	// purpose: asking to be told about a page you can already read grants
	// nothing you did not already have, and requiring a write permission to
	// follow a document would lock read-only members out of the one feature that
	// exists for people who are not editing.
	api.GET("/documents/:doc_id/watch", documentWatchHandler.Get, wsAccess)
	api.PUT("/documents/:doc_id/watch", documentWatchHandler.Watch, wsAccess)
	api.DELETE("/documents/:doc_id/watch", documentWatchHandler.Unwatch, wsAccess)

	// Addressing a document by its slug path instead of its uuid, because agents
	// think in paths and making them resolve an id first adds a call to every
	// single access.
	//
	// projAccess, like the rest of the project-scoped pair: the only id on the
	// route is :proj_id, and the segments after by-path are slugs inside the
	// project the guard has already checked the caller against — they can name
	// nothing outside it.
	//
	// A trailing wildcard rather than a query parameter, so the URL reads the way
	// the path is written. It cannot collide with the collection route above: Echo
	// matches the literal `documents` segment first, and `by-path` is a literal
	// segment no document slug lookup starts from by accident.
	api.GET("/projects/:proj_id/documents/by-path/*", documentHandler.GetByPath, projAccess)

	// Document attachment routes. The upload/list pair hangs off :doc_id, which
	// already resolves a tenant; the object routes name the attachment directly and
	// are guarded by the :att_id resolver.
	//
	// :att_id appears under exactly one collection segment — document-attachments —
	// and must stay that way: resolvers are keyed on the parameter name alone, so
	// mounting the same id under /documents/:doc_id/attachments/:att_id as well
	// would make the name ambiguous (TestScopedParamNamesAreUnambiguous).
	//
	// PermUploadArtifact for the writes, same reasoning as the document routes: an
	// attachment is project content whose bytes go to object storage, which is
	// literally what that permission covers.
	api.GET("/documents/:doc_id/attachments", documentAttachmentHandler.List, wsAccess)
	api.POST("/documents/:doc_id/attachments", documentAttachmentHandler.Upload, wsAccess, rbac(mw.PermUploadArtifact))
	api.GET("/document-attachments/:att_id/download", documentAttachmentHandler.Download, wsAccess)
	api.DELETE("/document-attachments/:att_id", documentAttachmentHandler.Delete, wsAccess, rbac(mw.PermUploadArtifact))

	// Document comment routes — Confluence-style inline comments on document text.
	//
	// The list/create pair hangs off :doc_id, which already resolves a tenant; the
	// object routes name the comment directly and are guarded by the :dcom_id
	// resolver. wsAccess throughout, not projAccess: none of these paths carry a
	// :proj_id, and RequireProjectMember answers 500 on a path without one.
	//
	// :dcom_id appears under exactly one collection segment — document-comments —
	// and must stay that way: resolvers are keyed on the parameter name alone, so
	// mounting the same id under /documents/:doc_id/comments/:dcom_id as well
	// would make the name ambiguous (TestScopedParamNamesAreUnambiguous). It is
	// spelled :dcom_id rather than :comment_id for the same reason — that name is
	// taken by task comments, whose resolver reads the unrelated `comments` table.
	//
	// PermAddComment for the writes: commenting on a document is the same act the
	// permission already names, held by the same roles (owner/admin/member and
	// agents), so nothing gains or loses reach. Resolve/unresolve sit under it too
	// — putting a thread away is part of taking part in it, not an admin power.
	api.GET("/documents/:doc_id/comments", documentCommentHandler.List, wsAccess)
	api.POST("/documents/:doc_id/comments", documentCommentHandler.Create, wsAccess, rbac(mw.PermAddComment))
	api.PATCH("/document-comments/:dcom_id", documentCommentHandler.Update, wsAccess, rbac(mw.PermAddComment))
	api.POST("/document-comments/:dcom_id/resolve", documentCommentHandler.Resolve, wsAccess, rbac(mw.PermAddComment))
	api.POST("/document-comments/:dcom_id/unresolve", documentCommentHandler.Unresolve, wsAccess, rbac(mw.PermAddComment))
	api.DELETE("/document-comments/:dcom_id", documentCommentHandler.Delete, wsAccess, rbac(mw.PermAddComment))
}

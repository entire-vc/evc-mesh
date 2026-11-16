package handler

import (
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// authorFeedViewer says who a /me list (GET /me/comments, GET /me/mentions and
// its unseen count) is served to. These routes carry no workspace role, so a request workspace role (if any) must not widen it: a human
// is passed as a UserID viewer and the repository checks owner/admin per row
// against each row's own workspace; an agent sees only its explicit project
// memberships; an extension the projects its connection may reach.
func authorFeedViewer(c echo.Context, actorID uuid.UUID, actorType domain.ActorType) domain.ProjectViewer {
	switch actorType {
	case domain.ActorTypeUser:
		return domain.ProjectViewer{UserID: &actorID}
	case domain.ActorTypeAgent:
		return domain.ProjectViewer{AgentID: &actorID}
	case domain.ActorTypeExtension:
		if mw.GetExtensionPrincipal(c) != nil {
			return projectViewerFromContext(c)
		}
	}
	return domain.ProjectViewer{}
}

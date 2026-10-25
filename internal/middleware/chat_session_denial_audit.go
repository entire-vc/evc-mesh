package middleware

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ChatSessionDenialAudit records every 403 a chat-session (cst_) request gets
// into the activity log: the agent as actor, the user it acted for, the chat
// thread and session, and the route. Allowed calls are already attributed by
// the entity's own activity row; a refusal changes nothing, so without this it
// would leave no trace at all. The bearer never reaches the row. Inert for any
// other principal. Registered right after DualAuth so it sees the answer of
// every guard behind it (role clamp, agent ceiling, project scope).
func ChatSessionDenialAudit(repo repository.ActivityLogRepository) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)
			p := GetChatSession(c)
			if p == nil || p.Token == nil || repo == nil || answeredStatus(c, err) != http.StatusForbidden {
				return err
			}
			changes, _ := json.Marshal(map[string]any{
				"method":               c.Request().Method,
				"route":                c.Path(),
				"status":               http.StatusForbidden,
				"initiated_by_user_id": p.Token.UserID,
				"chat_thread_id":       p.Token.ChatThreadID,
				"chat_session_id":      p.Token.ChatSessionID,
			})
			entry := &domain.ActivityLog{
				ID: uuid.New(), WorkspaceID: p.Token.WorkspaceID, EntityType: "chat_session", EntityID: p.Token.ChatSessionID,
				Action: "chat_session.denied", ActorID: p.Token.AgentID, ActorType: domain.ActorTypeAgent,
				Changes: changes, CreatedAt: time.Now().UTC(),
			}
			if cerr := repo.Create(c.Request().Context(), entry); cerr != nil {
				slog.Error("chat session denial audit failed", "error", cerr, "session", p.Token.ChatSessionID)
			}
			return err
		}
	}
}

// answeredStatus is the status the client gets: the one already written, or the
// one carried by the error the error handler is about to render.
func answeredStatus(c echo.Context, err error) int {
	if err != nil {
		var he *echo.HTTPError
		if errors.As(err, &he) {
			return he.Code
		}
		var ae *apierror.Error
		if errors.As(err, &ae) {
			return ae.StatusCode()
		}
	}
	return c.Response().Status
}

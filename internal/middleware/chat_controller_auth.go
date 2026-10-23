package middleware

import (
	"crypto/subtle"
	"os"

	"github.com/labstack/echo/v4"
)

// EnvChatControllerToken holds the shared secret of the chat runner's scoped
// controller credential. It is deliberately NOT an agent key and not a user JWT:
// the model's session never holds it, only the runner that leases chat turns, so
// the ability to mint a chat-session token stays out of reach of every agent
// identity (contract agent-chat-session-token §3.2).
const EnvChatControllerToken = "MESH_CHAT_CONTROLLER_TOKEN"

// ChatControllerTokenHeader carries the controller secret on issue/renew/revoke.
const ChatControllerTokenHeader = "X-Chat-Controller-Token"

// ChatControllerAuth gates the chat-session token control routes behind
// EnvChatControllerToken, compared in constant time and read fresh on every
// request (rotatable without a redeploy). Unconfigured means fail CLOSED: an
// empty or unset secret refuses every request, including one with an empty header.
func ChatControllerAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			want := os.Getenv(EnvChatControllerToken)
			if want == "" {
				return unauthorizedJSON(c, "chat controller credential is not configured")
			}
			got := c.Request().Header.Get(ChatControllerTokenHeader)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				return unauthorizedJSON(c, "invalid chat controller credential")
			}
			return next(c)
		}
	}
}

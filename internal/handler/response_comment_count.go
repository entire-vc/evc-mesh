package handler

import (
	"github.com/labstack/echo/v4"

	mw "github.com/entire-vc/evc-mesh/internal/middleware"
)

// setResponseCommentCount records how many comments a response carried so the
// tool-call tracker can count get_task+comments separately. It lives in its own
// file so comment_handler.go does not need the middleware import for it: the
// extension-authorship RED control reverts that import with its fix commit.
func setResponseCommentCount(c echo.Context, n int) { mw.SetResponseCommentCount(c, n) }

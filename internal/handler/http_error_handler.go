package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// NewHTTPErrorHandler wraps Echo's own error handler so that a handler
// returning *apierror.Error gets the status code and body that type actually
// carries, instead of the 500 Echo gives anything that isn't *echo.HTTPError.
//
// Every handler in this codebase returns *apierror.Error for the ordinary,
// expected failures — bad input, missing auth, not found — and nothing ever
// taught Echo what that type means: DefaultHTTPErrorHandler type-switches on
// *echo.HTTPError only, so *apierror.Error fell through to its generic
// fallback, which is a bare 500. A caller sending a malformed request saw
// "the server is broken" instead of "your request is wrong" — reproduced
// live on GET /me/mentions and /me/document-mentions with no workspace_id
// (#fe3ae257), and pre-existing on /me/tasks the same way; this fixes it for
// every route at once rather than one handler at a time.
//
// fallback is Echo's own DefaultHTTPErrorHandler (captured by the caller
// before installing this one), so anything that is not *apierror.Error keeps
// behaving exactly as it did.
func NewHTTPErrorHandler(fallback echo.HTTPErrorHandler) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		var apiErr *apierror.Error
		if !errors.As(err, &apiErr) {
			// Echo's own errors (no route, wrong method, a body that cannot be
			// bound) would otherwise answer a bare {"message": ...} with no stable
			// code; give them the generic code of their status like every other
			// error response.
			var httpErr *echo.HTTPError
			switch {
			case !errors.As(err, &httpErr):
				// Any other error a handler returns unclassified: Echo would answer
				// 500 with a bare message. Same status, same generic text (the
				// error's own text is never sent), plus the stable code.
				apiErr = apierror.InternalError("")
				// The client never sees the cause, so the log keeps its class, not its
				// text: error text can carry credentials (a DSN, a value from a
				// database error), and a digest of it can be brute-forced offline. The
				// type chain locates the failing path; the handler that should have
				// classified the error is where its text gets fixed.
				c.Logger().Errorf("unhandled handler error: %s %s: types=%s",
					c.Request().Method, strconv.Quote(c.Path()), errorTypeChain(err))
			case httpErr.Code < http.StatusBadRequest:
				fallback(err, c)
				return
			default:
				msg, ok := httpErr.Message.(string)
				if !ok || msg == "" {
					msg = http.StatusText(httpErr.Code)
				}
				apiErr = &apierror.Error{Code: httpErr.Code, Message: msg}
			}
		}

		if c.Response().Committed {
			return
		}

		var writeErr error
		if c.Request().Method == http.MethodHead {
			writeErr = c.NoContent(apiErr.StatusCode())
		} else {
			writeErr = c.JSON(apiErr.StatusCode(), apiErr)
		}
		if writeErr != nil {
			c.Logger().Error(writeErr)
		}
	}
}

// errorTypeChain renders the dynamic types along err's Unwrap chain, outermost
// first. Joined errors contribute their first branch only; the chain is a
// locator, not a full dump.
func errorTypeChain(err error) string {
	var types []string
	for err != nil && len(types) < 8 {
		types = append(types, fmt.Sprintf("%T", err))
		switch u := err.(type) { //nolint:errorlint // walking the chain by hand on purpose
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		case interface{ Unwrap() []error }:
			if inner := u.Unwrap(); len(inner) > 0 {
				err = inner[0]
			} else {
				err = nil
			}
		default:
			err = nil
		}
	}
	return strings.Join(types, " -> ")
}

package middleware

import (
	"log"

	"github.com/labstack/echo/v4"
)

// MultipartCleanup removes the temp files of a parsed multipart form once
// the request is done.
//
// net/http does this itself in finishRequest, but only for the *http.Request
// it handed to the handler. The auth middlewares replace the request with
// c.Request().WithContext(...), a shallow copy, so ParseMultipartForm stores
// the form on the copy and the server never sees it: every upload above the
// 32 MB in-memory limit left a /tmp/multipart-NNN file of its full size
// behind. This reads c.Request() after the chain returns, i.e. the copy the
// handler actually parsed.
func MultipartCleanup() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			defer func() {
				if form := c.Request().MultipartForm; form != nil {
					if err := form.RemoveAll(); err != nil {
						log.Printf("[multipart-cleanup] remove temp files: %v", err)
					}
				}
			}()
			return next(c)
		}
	}
}

package handler

import (
	"github.com/labstack/echo/v4"
)

// DeprecatedAlias marks a route that stays for one release after its
// replacement shipped (docs/extensions/api.md, "Aliases"). The response body is
// untouched; the headers tell a client where to move.
func DeprecatedAlias(successor string) echo.MiddlewareFunc {
	link := "<" + successor + `>; rel="successor-version"`
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Response().Header()
			h.Set("Deprecation", "true")
			h.Set("Link", link)
			return next(c)
		}
	}
}

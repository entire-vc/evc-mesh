package main

import (
	"context"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// newVersionHandler serves /api/version, /api/v1/version and
// /api/v1/healthz/version. See the comment at the registration site in main.go
// for why spark_enabled/spark_url ride along. sparkInstance reports the Spark
// instance level (nil: Spark is not wired, reported as off).
func newVersionHandler(sparkInstance func(ctx context.Context) (string, bool)) echo.HandlerFunc {
	return func(c echo.Context) error {
		sparkURL, sparkEnabled := "", false
		if sparkInstance != nil {
			sparkURL, sparkEnabled = sparkInstance(c.Request().Context())
		}
		return c.JSON(200, map[string]any{
			"commit":         BuildSHA,
			"build_time":     BuildTime,
			"version":        BuildVersion,
			"environment":    BuildEnv,
			"service":        "evc-mesh-api",
			"spark_enabled":  sparkEnabled,
			"spark_url":      sparkURL,
			"extensions_api": extensions.ExtensionsAPIVersion,
		})
	}
}

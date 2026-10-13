package main

import (
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// newVersionHandler serves /api/version, /api/v1/version and
// /api/v1/healthz/version. See the comment at the registration site in main.go
// for why spark_enabled/spark_url ride along.
func newVersionHandler(cfg *config.Config) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(200, map[string]any{
			"commit":         BuildSHA,
			"build_time":     BuildTime,
			"version":        BuildVersion,
			"environment":    BuildEnv,
			"service":        "evc-mesh-api",
			"spark_enabled":  cfg.Spark.Enabled,
			"spark_url":      cfg.Spark.URL,
			"extensions_api": extensions.ExtensionsAPIVersion,
		})
	}
}

package handler

import (
	"bytes"
	"encoding/json"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// extensionJSON writes v with manifest "<key>_i18n" values resolved for the
// request's Accept-Language (manifest title/description are English by
// default). A value that cannot round-trip through JSON is sent unlocalized.
func extensionJSON(c echo.Context, status int, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return c.JSON(status, v)
	}
	lang := extensions.LocaleFromAcceptLanguage(c.Request().Header.Get("Accept-Language"))
	if lang == extensions.DefaultLocale && !bytes.Contains(raw, []byte("_i18n\"")) {
		return c.JSON(status, v)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return c.JSON(status, v)
	}
	return c.JSON(status, extensions.LocalizeJSON(generic, lang))
}

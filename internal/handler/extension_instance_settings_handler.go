package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/themes"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// secretMask is returned instead of a stored secret and means "keep as is" on PUT.
const secretMask = "********"

// InstanceAdminChecker decides who may edit instance-level extension settings.
type InstanceAdminChecker interface {
	IsInstanceAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
}

// ExtensionInstanceSettingsHandler serves GET/PUT /extensions/:ext_id/instance-settings.
type ExtensionInstanceSettingsHandler struct {
	repo     repository.InstanceExtensionSettingRepository
	admins   InstanceAdminChecker
	registry *extensions.Registry
	sources  []extensions.EnvSource
	journal  repository.InstanceExtensionPolicyRepository
}

// WithJournal makes Put record who changed which keys (never values).
func (h *ExtensionInstanceSettingsHandler) WithJournal(j repository.InstanceExtensionPolicyRepository) *ExtensionInstanceSettingsHandler {
	h.journal = j
	return h
}

func NewExtensionInstanceSettingsHandler(repo repository.InstanceExtensionSettingRepository, admins InstanceAdminChecker, registry *extensions.Registry, sources []extensions.EnvSource) *ExtensionInstanceSettingsHandler {
	return &ExtensionInstanceSettingsHandler{repo: repo, admins: admins, registry: registry, sources: sources}
}

type instanceSettingsBody struct {
	Settings map[string]any `json:"settings"`
	Enabled  bool           `json:"enabled"`
}

type instanceSettingsResponse struct {
	ExtensionID string         `json:"extension_id"`
	Settings    map[string]any `json:"settings"`
	Enabled     bool           `json:"enabled"`
	Source      string         `json:"source"`
	UpdatedBy   *uuid.UUID     `json:"updated_by,omitempty"`
	UpdatedAt   string         `json:"updated_at,omitempty"`
}

// known reports whether extID is an extension this instance knows and its
// secret setting keys. The keep extension is always known: its instance row
// carries the secret provider switch, which Mesh reads itself. A theme is
// never known here: its instance default is set in the instance Themes tab.
func (h *ExtensionInstanceSettingsHandler) known(extID string) (secretKeys []string, ok bool) {
	ok = extID == secretstore.KeepExtensionID
	for _, s := range h.sources {
		if s.ExtensionID == extID {
			secretKeys, ok = s.SecretKeys, true
		}
	}
	if h.registry != nil {
		if p, reg := h.registry.Get(extID); reg {
			if p.Manifest().Kind == themes.KindTheme {
				return nil, false
			}
			ok = true
			for _, k := range p.Manifest().InstanceSecretKeys() {
				if !slices.Contains(secretKeys, k) {
					secretKeys = append(secretKeys, k)
				}
			}
		}
	}
	return secretKeys, ok
}

// authorize writes the denial response itself and reports denied=true.
func (h *ExtensionInstanceSettingsHandler) authorize(c echo.Context) (denied bool, err error) {
	if middleware.IsAgent(c) {
		return true, c.JSON(http.StatusForbidden, apierror.Forbidden("instance settings require a human instance administrator"))
	}
	uid, err := middleware.GetUserID(c)
	if err != nil {
		return true, c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
	}
	ok, err := h.admins.IsInstanceAdmin(c.Request().Context(), uid)
	if err != nil {
		return true, c.JSON(http.StatusInternalServerError, apierror.InternalError("admin check failed"))
	}
	if !ok {
		return true, c.JSON(http.StatusForbidden, apierror.Forbidden("only an instance administrator can change instance extension settings"))
	}
	return false, nil
}

func maskInstanceSecrets(settings map[string]any, secretKeys []string) map[string]any {
	out := make(map[string]any, len(settings))
	for k, v := range settings {
		out[k] = v
	}
	for _, k := range secretKeys {
		if s, ok := out[k].(string); ok && s != "" {
			out[k] = secretMask
		}
	}
	return out
}

func (h *ExtensionInstanceSettingsHandler) Get(c echo.Context) error {
	if denied, err := h.authorize(c); denied {
		return err
	}
	extID := c.Param("ext_id")
	secretKeys, ok := h.known(extID)
	if !ok {
		return c.JSON(http.StatusNotFound, apierror.NotFound("extension not found"))
	}
	row, err := h.repo.Get(c.Request().Context(), extID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierror.InternalError("failed to read instance settings"))
	}
	if row == nil {
		return c.JSON(http.StatusOK, instanceSettingsResponse{ExtensionID: extID, Settings: map[string]any{}, Source: "none"})
	}
	settings := map[string]any{}
	_ = json.Unmarshal(row.Settings, &settings)
	return c.JSON(http.StatusOK, instanceSettingsResponse{
		ExtensionID: extID,
		Settings:    maskInstanceSecrets(settings, secretKeys),
		Enabled:     row.Enabled,
		Source:      string(row.Source),
		UpdatedBy:   row.UpdatedBy,
		UpdatedAt:   row.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

func (h *ExtensionInstanceSettingsHandler) Put(c echo.Context) error {
	if denied, err := h.authorize(c); denied {
		return err
	}
	ctx := c.Request().Context()
	extID := c.Param("ext_id")
	secretKeys, ok := h.known(extID)
	if !ok {
		return c.JSON(http.StatusNotFound, apierror.NotFound("extension not found"))
	}
	var body instanceSettingsBody
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	if body.Settings == nil {
		body.Settings = map[string]any{}
	}

	// A masked or omitted secret keeps the stored value; with nothing stored
	// the placeholder is dropped instead of being saved as a credential.
	prev, err := h.repo.Get(ctx, extID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierror.InternalError("failed to read instance settings"))
	}
	old := map[string]any{}
	if prev != nil {
		_ = json.Unmarshal(prev.Settings, &old)
	}
	for _, k := range secretKeys {
		if v, present := body.Settings[k]; !present || v == secretMask {
			if ov, had := old[k]; had {
				body.Settings[k] = ov
			} else {
				delete(body.Settings, k)
			}
		}
	}

	// The provider switch is Mesh's own setting: checked here, and kept out
	// of the extension's validation, which knows nothing of it.
	toValidate := body.Settings
	if extID == secretstore.KeepExtensionID {
		if v, present := body.Settings[secretstore.SettingSecretProvider]; present {
			str, isStr := v.(string)
			if _, perr := secretstore.ParseProvider(str); !isStr || perr != nil {
				return c.JSON(http.StatusUnprocessableEntity, apierror.BadRequest(secretstore.SettingSecretProvider+" must be "+secretstore.ProviderBuiltin+" or "+secretstore.ProviderKeep))
			}
			toValidate = make(map[string]any, len(body.Settings))
			for k, v := range body.Settings {
				if k != secretstore.SettingSecretProvider {
					toValidate[k] = v
				}
			}
		}
	}
	if h.registry != nil {
		if p, reg := h.registry.Get(extID); reg {
			// An external extension is checked even with nothing to save: its
			// manifest schema may require fields, and one with no instance
			// schema takes no instance row. Builtins keep their enable-only PUT.
			ext, isExt := p.(interface{ IsExternal() bool })
			if len(toValidate) > 0 || (isExt && ext.IsExternal()) {
				if verr := p.Validate(extensions.LevelInstance, toValidate); verr != nil {
					return c.JSON(http.StatusUnprocessableEntity, apierror.BadRequest(verr.Error()))
				}
			}
		}
	}

	blob, err := json.Marshal(body.Settings)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid settings"))
	}
	uid, _ := middleware.GetUserID(c)
	if err := h.repo.Upsert(ctx, &domain.InstanceExtensionSetting{
		ExtensionID: extID,
		Settings:    blob,
		Enabled:     body.Enabled,
		Source:      domain.InstanceSettingSourceAdmin,
		UpdatedBy:   &uid,
	}, secretKeys); err != nil {
		return c.JSON(http.StatusInternalServerError, apierror.InternalError("failed to save instance settings"))
	}
	if h.journal != nil {
		// Only the keys whose value differs from what was stored: a form posts
		// the whole object, and unchanged fields are not part of the change.
		keys := make([]string, 0, len(body.Settings))
		for k, v := range body.Settings {
			if ov, had := old[k]; !had || !reflect.DeepEqual(ov, v) {
				keys = append(keys, k)
			}
		}
		for k := range old {
			if _, still := body.Settings[k]; !still {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		enabledChanged := prev != nil && prev.Enabled != body.Enabled || prev == nil && body.Enabled
		// A save that changes nothing is not an event worth recording.
		if len(keys) > 0 || enabledChanged {
			details := map[string]any{"keys": keys}
			if enabledChanged {
				details["enabled"] = body.Enabled
			}
			appendJournal(c, h.journal, uid, extID, domain.InstanceJournalSettingsChanged, details)
		}
	}
	return c.JSON(http.StatusOK, instanceSettingsResponse{
		ExtensionID: extID,
		Settings:    maskInstanceSecrets(body.Settings, secretKeys),
		Enabled:     body.Enabled,
		Source:      string(domain.InstanceSettingSourceAdmin),
		UpdatedBy:   &uid,
	})
}

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// teamRelayExtensionID is the manifest id of the extension whose project
// settings are typed (share, mount, sync) and kept in project_integrations by
// the Team Relay service; every other extension's project level is generic.
const teamRelayExtensionID = "team-relay"

// maskedSecret is what a secret setting reads back as once it is stored.
const maskedSecret = "***"

// agentKeySetting is the one secret a project binding can hold: the row has a
// single encrypted credential column, named after the Team Relay key it was made for.
const agentKeySetting = "agent_key"

// extensionKeyExpiringSoonWindow is how far ahead of key_expires_at the
// key_expiring_soon flag turns on (the same window as the old Team Relay block).
const extensionKeyExpiringSoonWindow = teamRelayKeyExpiringSoonWindow

// extensionLookup is the part of the registry the project routes need.
type extensionLookup interface {
	Get(id string) (extensions.Provider, bool)
}

// ProjectAction is an operation a provider offers at the project level, called
// as /projects/:proj_id/extensions/:ext_id/actions/:name so the interface never
// learns the paths of the system behind the extension. Method GET is a read
// (project access); anything else is a write (manage_webhooks).
type ProjectAction struct {
	Name   string
	Method string
	Title  string
	Run    echo.HandlerFunc
}

// checkAction is the action every project-level extension has.
const checkAction = "check"

// ExtensionProjectHandler serves /projects/:proj_id/extensions...
// (docs/extensions/api.md, "Project routes"): the project level of every
// extension whose manifest has levels.project.
type ExtensionProjectHandler struct {
	registry extensionLookup
	pi       service.ProjectIntegrationService
	projects service.ProjectService
	actions  map[string][]ProjectAction
}

func NewExtensionProjectHandler(registry extensionLookup, pi service.ProjectIntegrationService) *ExtensionProjectHandler {
	return &ExtensionProjectHandler{registry: registry, pi: pi, actions: map[string][]ProjectAction{}}
}

// WithProjects gives the handler the project lookup the check action needs to
// find the workspace whose connection a binding relies on.
func (h *ExtensionProjectHandler) WithProjects(projects service.ProjectService) *ExtensionProjectHandler {
	h.projects = projects
	return h
}

// WithActions registers the provider-specific actions of an extension.
func (h *ExtensionProjectHandler) WithActions(extensionID string, actions ...ProjectAction) *ExtensionProjectHandler {
	h.actions[extensionID] = append(h.actions[extensionID], actions...)
	return h
}

// projectExtensionResponse is the ProjectExtension type of the API document.
type projectExtensionResponse struct {
	ExtensionID string         `json:"extension_id"`
	Enabled     bool           `json:"enabled"`
	Settings    map[string]any `json:"settings"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`

	// KeyExpiresAt/KeyExpirySource say when the stored credential stops working
	// and how that is known ("source": asked of the system itself; "manual").
	// KeyExpiringSoon is false, not absent, when the expiry is unknown.
	KeyExpiresAt    *time.Time `json:"key_expires_at,omitempty"`
	KeyExpirySource *string    `json:"key_expiry_source,omitempty"`
	KeyExpiringSoon bool       `json:"key_expiring_soon"`

	// LastSync* record the latest attempt to reach the source of the binding.
	LastSyncCheckedAt *time.Time `json:"last_sync_checked_at,omitempty"`
	LastSyncStatus    *string    `json:"last_sync_status,omitempty"`
	LastSyncError     *string    `json:"last_sync_error,omitempty"`

	// ProjectView is the project.view tab of the extension; List sets it only
	// while the extension is enabled in the project.
	ProjectView *projectViewResponse `json:"project_view,omitempty"`
	// UnmetRequires names the extensions from the manifest's requires that are
	// not enabled in this project. The extension still works; the interface warns.
	UnmetRequires []string `json:"unmet_requires,omitempty"`
}

// projectViewResponse is the project.view slot as the interface consumes it.
type projectViewResponse struct {
	Icon      string         `json:"icon"`
	Title     string         `json:"title"`
	After     string         `json:"after,omitempty"`
	Route     string         `json:"route"`
	VisibleIf map[string]any `json:"visible_if,omitempty"`
}

func toProjectExtensionResponse(extID string, pi *domain.ProjectIntegration) projectExtensionResponse {
	settings := map[string]any{}
	_ = json.Unmarshal(pi.Settings, &settings)
	if settings == nil {
		settings = map[string]any{}
	}
	// The agent key is a secret setting: reported as set, never as a value.
	if pi.AgentKey != "" {
		settings[agentKeySetting] = maskedSecret
	}
	expiringSoon := pi.KeyExpiresAt != nil && time.Until(*pi.KeyExpiresAt) <= extensionKeyExpiringSoonWindow
	return projectExtensionResponse{
		ExtensionID:       extID,
		Enabled:           pi.Enabled,
		Settings:          settings,
		CreatedAt:         pi.CreatedAt,
		UpdatedAt:         pi.UpdatedAt,
		KeyExpiresAt:      pi.KeyExpiresAt,
		KeyExpirySource:   pi.KeyExpirySource,
		KeyExpiringSoon:   expiringSoon,
		LastSyncCheckedAt: pi.LastSyncCheckedAt,
		LastSyncStatus:    pi.LastSyncStatus,
		LastSyncError:     pi.LastSyncError,
	}
}

// target resolves the path to a project and a registered extension, and
// refuses with 404 when the extension is unknown or its manifest has no project level.
func (h *ExtensionProjectHandler) target(c echo.Context) (uuid.UUID, string, extensions.Provider, error) {
	projectID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return uuid.Nil, "", nil, apierror.BadRequest("invalid proj_id")
	}
	extID := c.Param("ext_id")
	p, ok := h.registry.Get(extID)
	if !ok || !p.Manifest().Levels.Project {
		return uuid.Nil, "", nil, apierror.NotFound("Extension")
	}
	return projectID, extID, p, nil
}

// List handles GET /projects/:proj_id/extensions: the bindings the project has,
// for the extensions that still have a project level.
func (h *ExtensionProjectHandler) List(c echo.Context) error {
	projectID, err := uuid.Parse(c.Param("proj_id"))
	if err != nil {
		return handleError(c, apierror.BadRequest("invalid proj_id"))
	}
	rows, err := h.pi.List(c.Request().Context(), projectID)
	if err != nil {
		return handleError(c, err)
	}
	out := make([]projectExtensionResponse, 0, len(rows))
	enabled := map[string]bool{}
	for i := range rows {
		if rows[i].Enabled {
			enabled[bindingExtensionID(&rows[i])] = true
		}
	}
	for i := range rows {
		extID := bindingExtensionID(&rows[i])
		if p, ok := h.registry.Get(extID); ok && p.Manifest().Levels.Project {
			resp := toProjectExtensionResponse(extID, &rows[i])
			if rows[i].Enabled {
				m := p.Manifest()
				resp.ProjectView = projectViewOf(extID, m.ProjectView)
				for _, req := range m.Requires {
					if !enabled[req] {
						resp.UnmetRequires = append(resp.UnmetRequires, req)
					}
				}
			}
			out = append(out, resp)
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"extensions": out})
}

func bindingExtensionID(pi *domain.ProjectIntegration) string {
	if pi.ExtensionID != "" {
		return pi.ExtensionID
	}
	return domain.ProjectIntegrationExtensionID(pi.Type)
}

func projectViewOf(extID string, v *extensions.ProjectView) *projectViewResponse {
	if v == nil {
		return nil
	}
	return &projectViewResponse{Icon: v.Icon, Title: v.Title, After: v.After, VisibleIf: v.VisibleIf, Route: "/w/:ws/p/:proj/" + extID + "/*"}
}

// Get handles GET /projects/:proj_id/extensions/:ext_id.
func (h *ExtensionProjectHandler) Get(c echo.Context) error {
	projectID, extID, _, err := h.target(c)
	if err != nil {
		return handleError(c, err)
	}
	pi, err := h.binding(c, projectID, extID)
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, toProjectExtensionResponse(extID, pi))
}

func (h *ExtensionProjectHandler) binding(c echo.Context, projectID uuid.UUID, extID string) (*domain.ProjectIntegration, error) {
	if extID == teamRelayExtensionID {
		return h.pi.GetTeamRelay(c.Request().Context(), projectID)
	}
	return h.pi.GetBinding(c.Request().Context(), projectID, extID)
}

// teamRelayProjectSettings are the keys the project level of Team Relay accepts.
// The manifest's project schema lists exactly these (plus the read-only
// key_expires_at); a test keeps the two from drifting apart.
type teamRelayProjectSettings struct {
	ShareID             string   `json:"share_id"`
	ShareSlug           string   `json:"share_slug"`
	AgentKey            string   `json:"agent_key"`
	Subfolder           string   `json:"subfolder"`
	IncludeProjectSlug  bool     `json:"include_project_slug"`
	DocsMountPath       string   `json:"docs_mount_path"`
	ExcludedPaths       []string `json:"excluded_paths"`
	SyncIntervalMinutes *int     `json:"sync_interval_minutes"`
}

// Put handles PUT /projects/:proj_id/extensions/:ext_id: the binding is
// created or replaced. A secret left out or sent back as "***" keeps the
// stored value; a key outside the manifest's project schema is a 400.
func (h *ExtensionProjectHandler) Put(c echo.Context) error {
	projectID, extID, provider, err := h.target(c)
	if err != nil {
		return handleError(c, err)
	}
	var req struct {
		Enabled  bool            `json:"enabled"`
		Settings json.RawMessage `json:"settings"`
	}
	if bindErr := c.Bind(&req); bindErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	var createdBy *uuid.UUID
	if uid, ok := currentUserID(c); ok {
		createdBy = &uid
	}
	var pi *domain.ProjectIntegration
	if extID == teamRelayExtensionID {
		pi, err = h.putTeamRelay(c, projectID, req.Enabled, req.Settings, createdBy)
	} else {
		pi, err = h.putGeneric(c, projectID, extID, provider, req.Enabled, req.Settings, createdBy)
	}
	if err != nil {
		return handleError(c, err)
	}
	return c.JSON(http.StatusOK, toProjectExtensionResponse(extID, pi))
}

func (h *ExtensionProjectHandler) putTeamRelay(c echo.Context, projectID uuid.UUID, enabled bool, raw json.RawMessage, createdBy *uuid.UUID) (*domain.ProjectIntegration, error) {
	var settings teamRelayProjectSettings
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if decErr := dec.Decode(&settings); decErr != nil {
			return nil, apierror.ValidationError(map[string]string{"settings": decErr.Error()})
		}
	}
	if settings.AgentKey == maskedSecret {
		settings.AgentKey = ""
	}
	return h.pi.UpsertTeamRelay(c.Request().Context(), projectID, service.UpsertProjectIntegrationInput{
		Enabled:             enabled,
		ShareID:             settings.ShareID,
		ShareSlug:           settings.ShareSlug,
		AgentKey:            settings.AgentKey,
		Subfolder:           settings.Subfolder,
		IncludeProjectSlug:  settings.IncludeProjectSlug,
		DocsMountPath:       settings.DocsMountPath,
		ExcludedPaths:       settings.ExcludedPaths,
		SyncIntervalMinutes: settings.SyncIntervalMinutes,
		CreatedBy:           createdBy,
	})
}

// putGeneric validates the settings against the manifest's project level (every
// key declared, none read-only), then against the provider's own rules, and
// stores them. The only secret a binding can hold is agent_key (its own
// encrypted column); a manifest that declares another one cannot be stored yet,
// so it is refused rather than written in the clear.
func (h *ExtensionProjectHandler) putGeneric(c echo.Context, projectID uuid.UUID, extID string, provider extensions.Provider, enabled bool, raw json.RawMessage, createdBy *uuid.UUID) (*domain.ProjectIntegration, error) {
	settings := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if decErr := json.Unmarshal(raw, &settings); decErr != nil {
			return nil, apierror.ValidationError(map[string]string{"settings": "must be a JSON object"})
		}
	}
	fields := provider.Manifest().SettingFields(string(extensions.LevelProject))
	problems := map[string]string{}
	for k := range settings {
		f, declared := fields[k]
		switch {
		case !declared:
			problems[k] = "not in the project settings schema of " + extID
		case f.ReadOnly:
			problems[k] = "read-only"
		case f.Secret && k != agentKeySetting:
			problems[k] = "secret settings other than agent_key are not supported at the project level"
		}
	}
	if len(problems) > 0 {
		return nil, apierror.ValidationError(problems)
	}
	agentKey, _ := settings[agentKeySetting].(string)
	if agentKey == maskedSecret {
		agentKey = ""
	}
	delete(settings, agentKeySetting)
	if err := provider.Validate(extensions.LevelProject, settings); err != nil {
		return nil, apierror.ValidationError(map[string]string{"settings": err.Error()})
	}
	return h.pi.UpsertBinding(c.Request().Context(), projectID, extID, service.UpsertProjectBindingInput{
		Enabled:   enabled,
		Settings:  settings,
		AgentKey:  agentKey,
		CreatedBy: createdBy,
	})
}

// Delete handles DELETE /projects/:proj_id/extensions/:ext_id.
func (h *ExtensionProjectHandler) Delete(c echo.Context) error {
	projectID, extID, _, err := h.target(c)
	if err != nil {
		return handleError(c, err)
	}
	if extID == teamRelayExtensionID {
		err = h.pi.DeleteTeamRelay(c.Request().Context(), projectID)
	} else {
		err = h.pi.DeleteBinding(c.Request().Context(), projectID, extID)
	}
	if err != nil {
		return handleError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// ListActions handles GET /projects/:proj_id/extensions/:ext_id/actions: what
// the interface may call for this extension, without knowing any path but this one.
func (h *ExtensionProjectHandler) ListActions(c echo.Context) error {
	_, extID, _, err := h.target(c)
	if err != nil {
		return handleError(c, err)
	}
	type item struct {
		Name   string `json:"name"`
		Method string `json:"method"`
		Title  string `json:"title"`
	}
	out := []item{{Name: checkAction, Method: http.MethodPost, Title: "Check the connection this binding relies on"}}
	for _, a := range h.actions[extID] {
		out = append(out, item{Name: a.Name, Method: a.Method, Title: a.Title})
	}
	return c.JSON(http.StatusOK, map[string]any{"actions": out})
}

// Action handles GET|POST /projects/:proj_id/extensions/:ext_id/actions/:action.
// The route's method decides the guard (read: project access, write:
// manage_webhooks), so an action answers only on the method it declares: a
// write action called with GET is a 404, never an unguarded run.
func (h *ExtensionProjectHandler) Action(c echo.Context) error {
	projectID, extID, provider, err := h.target(c)
	if err != nil {
		return handleError(c, err)
	}
	name, method := c.Param("action"), c.Request().Method
	if name == checkAction && method == http.MethodPost {
		return h.check(c, projectID, extID, provider)
	}
	for _, a := range h.actions[extID] {
		if a.Name == name && a.Method == method {
			return a.Run(c)
		}
	}
	return handleError(c, apierror.NotFound("Action"))
}

// check runs the provider's health check against the connection governing the
// project's workspace, for a project that has the binding. It writes nothing:
// "could not ask" must not read as "asked, and the binding is bad".
func (h *ExtensionProjectHandler) check(c echo.Context, projectID uuid.UUID, extID string, provider extensions.Provider) error {
	ctx := c.Request().Context()
	pi, err := h.binding(c, projectID, extID)
	if err != nil {
		return handleError(c, err)
	}
	if h.projects == nil {
		return handleError(c, apierror.InternalError("project lookup is not configured"))
	}
	project, err := h.projects.GetByID(ctx, projectID)
	if err != nil {
		return handleError(c, err)
	}
	res, err := provider.Resolve(ctx, project.WorkspaceID)
	if err != nil {
		return handleError(c, err)
	}
	report := extensions.HealthReport{Message: "extension is not enabled for this workspace"}
	if res.Enabled {
		report = provider.Health(ctx, res)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"ok":         report.OK,
		"message":    report.Message,
		"enabled":    pi.Enabled,
		"source":     res.Source,
		"checked_at": time.Now().UTC(),
	})
}

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/themes"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ExtensionInstanceAdminHandler serves the instance administration of
// extensions: the allow-list, the auto-update flag and the journal. Every
// route is for the instance administrator only.
type ExtensionInstanceAdminHandler struct {
	policy    repository.InstanceExtensionPolicyRepository
	settings  repository.InstanceExtensionSettingRepository
	installed repository.InstalledExtensionRepository
	admins    InstanceAdminChecker
	usage     ExtensionUsageCounter
}

// ExtensionUsageCounter counts, per extension id, the workspaces where it is on.
type ExtensionUsageCounter interface {
	CountActiveConnections(ctx context.Context) (map[string]int, error)
}

// journalSearcher is the filtered journal read; repositories without it get
// the newest entries unfiltered.
type journalSearcher interface {
	SearchJournal(ctx context.Context, f domain.InstanceJournalFilter) ([]domain.InstanceExtensionJournalEntry, error)
	JournalFacets(ctx context.Context) (*domain.InstanceJournalFacets, error)
}

// WithUsage makes Overview report "on in N workspaces".
func (h *ExtensionInstanceAdminHandler) WithUsage(u ExtensionUsageCounter) *ExtensionInstanceAdminHandler {
	h.usage = u
	return h
}

func NewExtensionInstanceAdminHandler(policy repository.InstanceExtensionPolicyRepository, settings repository.InstanceExtensionSettingRepository, installed repository.InstalledExtensionRepository, admins InstanceAdminChecker) *ExtensionInstanceAdminHandler {
	return &ExtensionInstanceAdminHandler{policy: policy, settings: settings, installed: installed, admins: admins}
}

// authorizeInstanceAdmin writes the denial response itself and returns the
// acting user; denied=true means the response is already sent.
func authorizeInstanceAdmin(c echo.Context, admins InstanceAdminChecker) (uid uuid.UUID, denied bool, err error) {
	if middleware.IsAgent(c) {
		return uid, true, c.JSON(http.StatusForbidden, apierror.Forbidden("instance settings require a human instance administrator"))
	}
	uid, uerr := middleware.GetUserID(c)
	if uerr != nil {
		return uid, true, c.JSON(http.StatusUnauthorized, apierror.Unauthorized("authentication required"))
	}
	ok, aerr := admins.IsInstanceAdmin(c.Request().Context(), uid)
	if aerr != nil {
		return uid, true, c.JSON(http.StatusInternalServerError, apierror.InternalError("admin check failed"))
	}
	if !ok {
		return uid, true, c.JSON(http.StatusForbidden, apierror.Forbidden("only an instance administrator can change instance extension settings"))
	}
	return uid, false, nil
}

type instanceOverviewItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Source     string `json:"source"`
	Version    string `json:"version"`
	TrustLevel string `json:"trust_level"`
	// Blocked is the catalog's block of the installed row; it beats the allow-list.
	Blocked         bool   `json:"blocked"`
	BlockedReason   string `json:"blocked_reason,omitempty"`
	Allowed         bool   `json:"allowed"`
	AllowExplicit   bool   `json:"allow_explicit"`
	HasInstanceForm bool   `json:"has_instance_form"`
	Enabled         bool   `json:"enabled"`
	SettingsSource  string `json:"settings_source"`
	SettingsAt      string `json:"settings_updated_at,omitempty"`
	// Workspaces is how many workspaces have it on; nil when not counted.
	Workspaces *int `json:"enabled_workspaces,omitempty"`
}

type instanceOverviewResponse struct {
	AutoUpdate bool                   `json:"auto_update"`
	Items      []instanceOverviewItem `json:"items"`
}

// Overview handles GET /extensions/instance/overview.
func (h *ExtensionInstanceAdminHandler) Overview(c echo.Context) error {
	if _, denied, err := authorizeInstanceAdmin(c, h.admins); denied {
		return err
	}
	ctx := c.Request().Context()
	rows, err := h.installed.List(ctx)
	if err != nil {
		return handleError(c, err)
	}
	policies, err := h.policy.ListPolicies(ctx)
	if err != nil {
		return handleError(c, err)
	}
	cfg, err := h.policy.GetConfig(ctx)
	if err != nil {
		return handleError(c, err)
	}
	var usage map[string]int
	if h.usage != nil {
		if usage, err = h.usage.CountActiveConnections(ctx); err != nil {
			return handleError(c, err)
		}
	}
	out := instanceOverviewResponse{AutoUpdate: cfg.AutoUpdate, Items: []instanceOverviewItem{}}
	for i := range rows {
		m, perr := extensions.ParseManifest(rows[i].Manifest)
		if perr != nil || extensions.IsSystemPart(rows[i].ExtensionID) {
			continue
		}
		it := instanceOverviewItem{
			ID: rows[i].ExtensionID, Name: m.Name, Kind: m.Kind, Source: string(rows[i].Source), Version: rows[i].Version,
			TrustLevel: string(rows[i].TrustLevel), Blocked: rows[i].Blocked, BlockedReason: rows[i].BlockedReason,
			Allowed: domain.DefaultExtensionAllowed(string(rows[i].TrustLevel)), HasInstanceForm: m.Kind != themes.KindTheme && m.Levels.Instance && m.HasInstanceFields(),
			SettingsSource: "none",
		}
		if usage != nil {
			n := usage[it.ID]
			it.Workspaces = &n
		}
		if p, ok := policies[it.ID]; ok {
			it.Allowed, it.AllowExplicit = p.Allowed, true
		}
		row, serr := h.settings.Get(ctx, it.ID)
		if serr != nil {
			return handleError(c, serr)
		}
		if row != nil {
			it.Enabled, it.SettingsSource = row.Enabled, string(row.Source)
			it.SettingsAt = row.UpdatedAt.UTC().Format(time.RFC3339)
		}
		out.Items = append(out.Items, it)
	}
	return c.JSON(http.StatusOK, out)
}

type allowedBody struct {
	Allowed bool `json:"allowed"`
}

// SetAllowed handles PUT /extensions/:ext_id/allowed.
func (h *ExtensionInstanceAdminHandler) SetAllowed(c echo.Context) error {
	uid, denied, err := authorizeInstanceAdmin(c, h.admins)
	if denied {
		return err
	}
	ctx := c.Request().Context()
	extID := c.Param("ext_id")
	if row, gerr := h.installed.Get(ctx, extID); gerr != nil {
		return handleError(c, gerr)
	} else if row == nil {
		return c.JSON(http.StatusNotFound, apierror.NotFound("extension not found"))
	}
	var body allowedBody
	if berr := c.Bind(&body); berr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	if serr := h.policy.SetAllowed(ctx, extID, body.Allowed, &uid); serr != nil {
		return handleError(c, serr)
	}
	appendJournal(c, h.policy, uid, extID, domain.InstanceJournalAllowChanged, map[string]any{"allowed": body.Allowed})
	return c.JSON(http.StatusOK, map[string]any{"extension_id": extID, "allowed": body.Allowed, "allow_explicit": true})
}

type autoUpdateBody struct {
	AutoUpdate bool `json:"auto_update"`
}

// SetAutoUpdate handles PUT /extensions/instance/auto-update.
func (h *ExtensionInstanceAdminHandler) SetAutoUpdate(c echo.Context) error {
	uid, denied, err := authorizeInstanceAdmin(c, h.admins)
	if denied {
		return err
	}
	var body autoUpdateBody
	if berr := c.Bind(&body); berr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}
	ctx := c.Request().Context()
	if serr := h.policy.SetAutoUpdate(ctx, body.AutoUpdate, &uid); serr != nil {
		return handleError(c, serr)
	}
	appendJournal(c, h.policy, uid, "", domain.InstanceJournalAutoUpdateChanged, map[string]any{"auto_update": body.AutoUpdate})
	return c.JSON(http.StatusOK, body)
}

type journalEntryResponse struct {
	ID          uuid.UUID       `json:"id"`
	ActorID     *uuid.UUID      `json:"actor_id,omitempty"`
	ActorName   string          `json:"actor_name,omitempty"`
	ExtensionID string          `json:"extension_id"`
	Action      string          `json:"action"`
	Details     json.RawMessage `json:"details"`
	CreatedAt   string          `json:"created_at"`
}

// Journal handles GET /extensions/instance/journal?limit=N (newest first, max 200).
// Optional filters: extension, action, actor (user id), since, until (RFC3339 or
// YYYY-MM-DD; until is exclusive). With facets=1 the response also lists the
// values those filters can take.
func (h *ExtensionInstanceAdminHandler) Journal(c echo.Context) error {
	if _, denied, err := authorizeInstanceAdmin(c, h.admins); denied {
		return err
	}
	limit := 50
	if v, perr := strconv.Atoi(c.QueryParam("limit")); perr == nil && v > 0 {
		limit = min(v, 200)
	}
	f := domain.InstanceJournalFilter{Limit: limit, ExtensionID: c.QueryParam("extension"), Action: c.QueryParam("action")}
	if v := c.QueryParam("actor"); v != "" {
		id, perr := uuid.Parse(v)
		if perr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest("actor must be a user id"))
		}
		f.ActorID = &id
	}
	for _, p := range []struct {
		name string
		dst  **time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		v := c.QueryParam(p.name)
		if v == "" {
			continue
		}
		t, perr := parseJournalTime(v)
		if perr != nil {
			return c.JSON(http.StatusBadRequest, apierror.BadRequest(p.name+" must be RFC3339 or YYYY-MM-DD"))
		}
		*p.dst = &t
	}
	ctx := c.Request().Context()
	search, canSearch := h.policy.(journalSearcher)
	var (
		rows []domain.InstanceExtensionJournalEntry
		err  error
	)
	if canSearch {
		rows, err = search.SearchJournal(ctx, f)
	} else {
		rows, err = h.policy.ListJournal(ctx, limit)
	}
	if err != nil {
		return handleError(c, err)
	}
	out := make([]journalEntryResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, journalEntryResponse{
			ID: r.ID, ActorID: r.ActorID, ActorName: r.ActorName, ExtensionID: r.ExtensionID, Action: r.Action,
			Details: r.Details, CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	resp := map[string]any{"entries": out}
	if c.QueryParam("facets") == "1" && canSearch {
		facets, ferr := search.JournalFacets(ctx)
		if ferr != nil {
			return handleError(c, ferr)
		}
		resp["facets"] = facets
	}
	return c.JSON(http.StatusOK, resp)
}

func parseJournalTime(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Parse(time.DateOnly, v)
}

// appendJournal records an admin action. The action already happened, so a
// failed journal write is logged and does not fail the request.
func appendJournal(c echo.Context, repo repository.InstanceExtensionPolicyRepository, actor uuid.UUID, extID, action string, details map[string]any) {
	raw, _ := json.Marshal(details)
	if err := repo.AppendJournal(c.Request().Context(), &domain.InstanceExtensionJournalEntry{ActorID: &actor, ExtensionID: extID, Action: action, Details: raw}); err != nil {
		c.Logger().Errorf("instance extension journal %s %s: %v", action, extID, err)
	}
}

package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// WorkspaceEventFeed reads the workspace-wide event feed.
type WorkspaceEventFeed interface {
	List(ctx context.Context, f postgres.WorkspaceEventFeedFilter) ([]postgres.WorkspaceFeedEvent, bool, error)
}

// WorkspaceEventHandler serves GET /workspaces/:ws_id/events: the events of
// every project of the workspace the viewer may see, filtered on the server.
// It replaces the Events page's client-side merge of the first five projects.
type WorkspaceEventHandler struct {
	feed WorkspaceEventFeed
}

func NewWorkspaceEventHandler(feed WorkspaceEventFeed) *WorkspaceEventHandler {
	return &WorkspaceEventHandler{feed: feed}
}

// WorkspaceEventPage is the response of GET /workspaces/:ws_id/events.
type WorkspaceEventPage struct {
	Items      []postgres.WorkspaceFeedEvent `json:"items"`
	HasMore    bool                          `json:"has_more"`
	NextCursor *string                       `json:"next_cursor"`
}

var knownEventTypes = map[domain.EventType]bool{
	domain.EventTypeSummary:            true,
	domain.EventTypeStatusChange:       true,
	domain.EventTypeContextUpdate:      true,
	domain.EventTypeError:              true,
	domain.EventTypeDependencyResolved: true,
	domain.EventTypeCustom:             true,
}

// splitMulti accepts both repeated parameters (?event_type=a&event_type=b),
// the bracket form (event_type[]=a) and comma lists (event_type=a,b).
func splitMulti(c echo.Context, name string) []string {
	q := c.QueryParams()
	var out []string
	for _, key := range []string{name, name + "[]"} {
		for _, v := range q[key] {
			for _, part := range strings.Split(v, ",") {
				if part = strings.TrimSpace(part); part != "" {
					out = append(out, part)
				}
			}
		}
	}
	return out
}

func encodeEventCursor(cur postgres.WorkspaceEventCursor) string {
	raw := cur.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + cur.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeEventCursor(s string) (*postgres.WorkspaceEventCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	ts, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return nil, errors.New("malformed cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil, err
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return nil, err
	}
	return &postgres.WorkspaceEventCursor{CreatedAt: t, ID: u}, nil
}

// List handles GET /workspaces/:ws_id/events.
//
// Query: event_type (repeatable or comma list), project_id, actor_type
// (agent|user|system), agent_id, task_id, tags (any of, comma list),
// date_from, date_to (RFC3339), cursor, limit (1..200, default 50).
// Scope: a workspace owner or admin sees every live project, anyone else and
// every agent only the projects they are a member of.
func (h *WorkspaceEventHandler) List(c echo.Context) error {
	wsID, err := uuid.Parse(c.Param("ws_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid workspace_id"))
	}
	f := postgres.WorkspaceEventFeedFilter{WorkspaceID: wsID, Limit: 50}

	// Scope first: fail closed when the actor cannot be named.
	switch {
	case mw.IsExtension(c):
		return c.JSON(http.StatusForbidden, apierror.Forbidden("this route is not available to extensions"))
	case mw.IsAgent(c):
		agentID, agentErr := mw.GetAgentID(c)
		if agentErr != nil {
			return c.JSON(http.StatusForbidden, apierror.Forbidden("agent identity unavailable"))
		}
		f.MemberAgentID = &agentID
	default:
		userID, userErr := mw.GetUserID(c)
		if userErr != nil {
			return c.JSON(http.StatusForbidden, apierror.Forbidden("user identity unavailable"))
		}
		role, _ := c.Get(mw.ContextKeyWorkspaceRole).(string)
		if role == domain.RoleOwner || role == domain.RoleAdmin {
			f.AllProjects = true
		} else {
			f.MemberUserID = &userID
		}
	}

	invalid := map[string]string{}
	for _, t := range splitMulti(c, "event_type") {
		et := domain.EventType(t)
		if !knownEventTypes[et] {
			invalid["event_type"] = "unknown event type " + strconv.Quote(t)
			break
		}
		f.EventTypes = append(f.EventTypes, et)
	}
	parseID := func(name string) *uuid.UUID {
		v := c.QueryParam(name)
		if v == "" {
			return nil
		}
		id, perr := uuid.Parse(v)
		if perr != nil {
			invalid[name] = "must be a UUID"
			return nil
		}
		return &id
	}
	parseTime := func(name string) *time.Time {
		v := c.QueryParam(name)
		if v == "" {
			return nil
		}
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			invalid[name] = "must be an RFC3339 timestamp"
			return nil
		}
		return &t
	}
	f.ProjectID = parseID("project_id")
	f.AgentID = parseID("agent_id")
	f.TaskID = parseID("task_id")
	f.From = parseTime("date_from")
	f.To = parseTime("date_to")
	f.Tags = splitMulti(c, "tags")
	switch a := c.QueryParam("actor_type"); a {
	case "", "all":
	case postgres.EventActorAgent, postgres.EventActorUser, postgres.EventActorSystem:
		f.ActorType = a
	default:
		invalid["actor_type"] = "must be agent, user or system"
	}
	if v := c.QueryParam("limit"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || n > 200 {
			invalid["limit"] = "must be 1-200"
		} else {
			f.Limit = n
		}
	}
	if v := c.QueryParam("cursor"); v != "" {
		cur, cerr := decodeEventCursor(v)
		if cerr != nil {
			invalid["cursor"] = "malformed cursor"
		} else {
			f.Cursor = cur
		}
	}
	if len(invalid) > 0 {
		return c.JSON(http.StatusBadRequest, apierror.ValidationError(invalid))
	}

	items, hasMore, err := h.feed.List(c.Request().Context(), f)
	if err != nil {
		return handleError(c, err)
	}
	if items == nil {
		items = []postgres.WorkspaceFeedEvent{}
	}
	page := WorkspaceEventPage{Items: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		cur := encodeEventCursor(postgres.WorkspaceEventCursor{CreatedAt: last.CreatedAt, ID: last.ID})
		page.NextCursor = &cur
	}
	return c.JSON(http.StatusOK, page)
}

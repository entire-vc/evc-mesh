package middleware

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

// errNoProject means a route parameter names nothing that resolves to exactly
// one live project: a missing or deleted row, a malformed id, an ambiguous
// short id.
var errNoProject = errors.New("request names no single live project")

// requestProjectParams maps a route parameter to the query that returns the
// project of the row it names. :task_id and :short also accept a 6–12 hex short
// id prefix, exactly as the task handlers do (resolveTaskID).
var requestProjectParams = []struct {
	param string
	query string
	short bool
}{
	{"proj_id", `SELECT id FROM projects WHERE id = $1 AND deleted_at IS NULL`, false},
	{"task_id", `SELECT project_id FROM tasks WHERE id = $1 AND deleted_at IS NULL`, true},
	{"short", ``, true},
	{"doc_id", `SELECT project_id FROM documents WHERE id = $1 AND deleted_at IS NULL`, false},
	{"artifact_id", `SELECT t.project_id FROM artifacts a JOIN tasks t ON t.id = a.task_id AND t.deleted_at IS NULL WHERE a.id = $1`, false},
	{"event_id", `SELECT project_id FROM event_bus_messages WHERE id = $1`, false},
	{"link_id", `SELECT t.project_id FROM vcs_links l JOIN tasks t ON t.id = l.task_id AND t.deleted_at IS NULL WHERE l.id = $1`, false},
}

const taskProjectByShortID = `SELECT project_id FROM tasks WHERE id::text LIKE $1 AND deleted_at IS NULL LIMIT 2`

// RequestProjects returns the project of every project-addressing parameter of
// the matched route (:proj_id, :task_id, :short, :doc_id, :artifact_id,
// :event_id, :link_id). found=false when the route has none of them. A
// parameter that resolves to no single live project is errNoProject: callers
// guarding access treat it as a refusal, never as "nothing to check".
func RequestProjects(ctx context.Context, db *sqlx.DB, c echo.Context) (projects []uuid.UUID, found bool, err error) {
	for _, rp := range requestProjectParams {
		v := c.Param(rp.param)
		if v == "" {
			continue
		}
		found = true
		pid, rerr := resolveProjectParam(ctx, db, v, rp.query, rp.short)
		if rerr != nil {
			return nil, true, rerr
		}
		projects = append(projects, pid)
	}
	return projects, found, nil
}

func resolveProjectParam(ctx context.Context, db *sqlx.DB, v, query string, short bool) (uuid.UUID, error) {
	var ids []uuid.UUID
	if id, perr := uuid.Parse(v); perr == nil && query != "" {
		if err := db.SelectContext(ctx, &ids, query, id); err != nil {
			return uuid.Nil, err
		}
	} else if short && isShortID(v) {
		if err := db.SelectContext(ctx, &ids, taskProjectByShortID, strings.ToLower(v)+"%"); err != nil {
			return uuid.Nil, err
		}
	}
	if len(ids) != 1 {
		return uuid.Nil, errNoProject
	}
	return ids[0], nil
}

// isShortID mirrors the handlers' isHexPrefix: 6–12 hex characters.
func isShortID(s string) bool {
	if len(s) < 6 || len(s) > 12 {
		return false
	}
	for _, ch := range s {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

// isNoProject reports whether err is the "names no single live project" refusal.
func isNoProject(err error) bool {
	return errors.Is(err, errNoProject) || errors.Is(err, sql.ErrNoRows)
}

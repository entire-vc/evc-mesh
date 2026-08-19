package postgres

import (
	"fmt"

	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// allowedSortColumns defines a set of columns that can be used for sorting.
// This prevents SQL injection through sort parameters.
type allowedSortColumns map[string]string

// orderClause builds a safe ORDER BY clause from pagination params.
// If sortBy is not in the allowed set, the defaultCol is used.
func orderClause(pg pagination.Params, allowed allowedSortColumns, defaultCol string) string {
	col := defaultCol
	if pg.SortBy != "" {
		if mapped, ok := allowed[pg.SortBy]; ok {
			col = mapped
		}
	}
	dir := "ASC"
	if pg.SortDir == "desc" {
		dir = "DESC"
	}
	return fmt.Sprintf("ORDER BY %s %s", col, dir)
}

// paginationClause builds the LIMIT / OFFSET fragment.
func paginationClause(pg pagination.Params) string {
	return fmt.Sprintf("LIMIT %d OFFSET %d", pg.Limit(), pg.Offset())
}

// actorNameExpr builds the correlated subquery that turns an (id, actor_type)
// pair into a display name, aliased as alias.
//
// The pattern is commentEnrichedSelect's, generalised: the same two-branch CASE
// was already written out by hand for comments.author_id and again for the
// activity feed, and documents needs it twice on one row (created_by and
// updated_by). Resolving at read time rather than storing a copy of the name is
// the point — a stored copy freezes the name a person had when they wrote, so a
// rename leaves their old name scattered across history. This heals it.
//
// It yields NULL, not an empty string, when nothing resolves: a 'system' actor,
// a deleted agent, or an unstamped legacy row. The domain field is a *string for
// that reason, so "no name" and "an empty name" stay different things.
//
// idCol and typeCol are SQL identifiers written by this package, never caller
// input — nothing here is escapable and nothing here comes off the wire.
func actorNameExpr(idCol, typeCol, alias string) string {
	return fmt.Sprintf(`CASE
		WHEN %[2]s = 'agent' THEN
			(SELECT name FROM agents WHERE id = %[1]s AND deleted_at IS NULL)
		WHEN %[2]s = 'user' THEN
			(SELECT COALESCE(NULLIF(u.display_name, ''), SPLIT_PART(u.email, '@', 1)) FROM users u WHERE u.id = %[1]s)
		ELSE NULL
	END AS %[3]s`, idCol, typeCol, alias)
}

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

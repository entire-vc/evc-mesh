package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// notFound checks if err is sql.ErrNoRows and returns an apierror.NotFound.
// Returns nil if err is nil, the original error otherwise.
func notFound(err error, entity string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return apierror.NotFound(entity)
	}
	return err
}

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

// filterBuilder helps construct dynamic WHERE clauses.
type filterBuilder struct {
	conditions []string
	args       []interface{}
	argIdx     int
}

func newFilterBuilder(startIdx int) *filterBuilder {
	return &filterBuilder{argIdx: startIdx}
}

// add appends a condition and its arguments.
func (fb *filterBuilder) add(condition string, arg interface{}) {
	fb.conditions = append(fb.conditions, fmt.Sprintf(condition, fb.argIdx))
	fb.args = append(fb.args, arg)
	fb.argIdx++
}

// addRaw appends a condition without placeholder substitution.
func (fb *filterBuilder) addRaw(condition string) {
	fb.conditions = append(fb.conditions, condition)
}

// where returns the WHERE clause string (including "WHERE") or empty string if no conditions.
func (fb *filterBuilder) where() string {
	if len(fb.conditions) == 0 {
		return ""
	}
	return "WHERE " + strings.Join(fb.conditions, " AND ")
}

// and returns the conditions joined by AND (without "WHERE" prefix), or empty string.
func (fb *filterBuilder) and() string {
	if len(fb.conditions) == 0 {
		return ""
	}
	return strings.Join(fb.conditions, " AND ")
}

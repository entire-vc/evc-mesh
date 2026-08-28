package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// rejectBadSortDir refuses a sort direction that is neither "asc" nor "desc",
// instead of letting pagination.Normalize quietly rewrite it to "asc".
//
// Why this cannot live in Normalize: Normalize has no way to report anything —
// it returns nothing and runs on every paginated endpoint, including ones whose
// callers legitimately pass no direction at all. So the refusal has to happen at
// the boundary that still owns an HTTP response.
//
// Why it is a shared function rather than the copy it started as: the identical
// block already existed in CommentHandler.List, and the task list needed the
// same rule. Two hand-kept copies of one gate is how a gate drifts out of step
// with itself while both halves keep looking correct.
//
// Returns refused=false when there is nothing to refuse — an empty direction is
// the caller declining to specify one, which Normalize is entitled to default.
//
// The two return values are not decoration. An echo handler signals "I already
// wrote the response" by returning the result of c.JSON, and that result is nil
// on success — so a single-error signature makes the natural call site
//
//	if err := rejectBadSortDir(c, pg); err != nil { return err }
//
// fall straight through a refusal it just wrote, and the handler carries on as
// if the input were valid. That is not hypothetical: it is exactly what this
// function did on its first draft, caught by the comment handler's existing
// garbage-input test. Returning the decision separately from the transport error
// makes the mistake unavailable rather than merely documented.
func rejectBadSortDir(c echo.Context, pg pagination.Params) (refused bool, err error) {
	param, value := "sort_dir", pg.SortDir
	if value == "" {
		param, value = "order", pg.Order
	}
	if value == "" || value == "asc" || value == "desc" {
		return false, nil
	}
	return true, c.JSON(http.StatusBadRequest, apierror.ValidationError(map[string]string{
		param: "must be \"asc\" or \"desc\"",
	}))
}

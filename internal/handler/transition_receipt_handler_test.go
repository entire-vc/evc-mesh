package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// A task id that is neither a UUID nor a short hex id is refused by every
// task-scoped receipt route before any service is called.
func TestTransitionReceiptHandler_InvalidTaskID(t *testing.T) {
	h := NewTransitionReceiptHandler(nil, nil)
	routes := map[string]echo.HandlerFunc{
		"product-done":  h.ProductDone,
		"list receipts": h.ListReceipts,
		"record intent": h.RecordAcceptance,
		"list intents":  h.ListAcceptances,
		"revoke intent": h.RevokeAcceptance,
	}
	for name, handle := range routes {
		t.Run(name, func(t *testing.T) {
			e := echo.New()
			e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("task_id")
			c.SetParamValues("not-a-task-id!")
			require.NoError(t, handle(c))
			require.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

// The database guard's refusal of a direct write to done is mapped to a stable
// machine code instead of a 500.
func TestHandleError_ProductDoneGuard(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/", http.NoBody), rec)
	require.NoError(t, handleError(c, &pq.Error{Message: "product_done_receipt_required: use the receipt route"}))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"product_done_receipt_required"`)
}

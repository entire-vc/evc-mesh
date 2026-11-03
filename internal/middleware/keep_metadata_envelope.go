package middleware

import (
	"bytes"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// The Keep metadata probe (GET /workspaces/:ws_id/secrets/:secret_id/keep-metadata,
// protocol mesh-keep-metadata-v1) answers in its own
// closed error envelope, not in the generic apierror one:
//
//	{"error_class":"<class>","message":"<constant>","request_id":"<Mesh ID>"}
//
// Every answer of the route, success included, is application/json and
// Cache-Control: no-store.

// KeepMetadataRouteSuffix identifies the route inside the protected group.
const KeepMetadataRouteSuffix = "/workspaces/:ws_id/secrets/:secret_id/keep-metadata"

const contextKeyKeepMetadataOwn = "keep_metadata_own_answer"

// Error classes and their constant messages. The messages never carry an id, a
// ref, upstream text or the reason: foreign and absent secrets must be
// indistinguishable.
const (
	keepMetaUnauthenticated = "authentication failed"
	keepMetaNotFound        = "not found"
	keepMetaInvalid         = "invalid metadata request"
	keepMetaRateLimited     = "request rate limited"
	keepMetaUnavailable     = "metadata unavailable"
)

// KeepMetadataFailure writes the closed error envelope for status and marks the
// answer as the route's own.
func KeepMetadataFailure(c echo.Context, status int) error {
	class, msg := "not_ready", keepMetaUnavailable
	switch status {
	case http.StatusUnauthorized:
		class, msg = "unauthenticated", keepMetaUnauthenticated
	case http.StatusNotFound:
		class, msg = "not_found", keepMetaNotFound
	case http.StatusBadRequest, http.StatusMethodNotAllowed:
		class, msg = "invalid_argument", keepMetaInvalid
	case http.StatusTooManyRequests:
		class, msg = "rate_limited", keepMetaRateLimited
	default:
		status = http.StatusServiceUnavailable
	}
	return keepMetadataJSON(c, status, map[string]string{
		"error_class": class, "message": msg, "request_id": uuid.NewString(),
	})
}

// KeepMetadataSuccess writes the closed success DTO.
func KeepMetadataSuccess(c echo.Context, version string, length int) error {
	return keepMetadataJSON(c, http.StatusOK, struct {
		Version   string `json:"version"`
		Length    int    `json:"length"`
		RequestID string `json:"request_id"`
	}{version, length, uuid.NewString()})
}

func keepMetadataJSON(c echo.Context, status int, body any) error {
	c.Set(contextKeyKeepMetadataOwn, true)
	h := c.Response().Header()
	// Nothing a previous middleware set may survive onto this answer.
	for k := range h {
		delete(h, k)
	}
	h.Set("Cache-Control", "no-store")
	return c.JSON(status, body)
}

// bufferedWriter holds what the rest of the chain wrote until the envelope
// decides whether it may go out.
type bufferedWriter struct {
	h      http.Header
	status int
	buf    bytes.Buffer
}

func (b *bufferedWriter) Header() http.Header         { return b.h }
func (b *bufferedWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufferedWriter) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

// KeepMetadataEnvelope is registered FIRST on the protected group. For the
// probe route only, it turns every answer the group's own guards produce
// (authentication, WorkspaceRLS, membership, RBAC, rate limit, method) into the
// route's closed envelope:
//
//   - 401 stays 401/unauthenticated;
//   - 403 and 404, i.e. any RLS/RBAC/membership denial, become the same
//     404/not_found a foreign or absent secret gets (the checks still ran; only
//     what they say is flattened);
//   - 429 stays 429; 400/405/413 become 400/405 invalid_argument;
//   - anything else is 503/not_ready.
//
// Answers the handler wrote itself pass through untouched. Every other route
// is passed straight to next with no buffering.
func KeepMetadataEnvelope() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !strings.HasSuffix(c.Path(), KeepMetadataRouteSuffix) {
				return next(c)
			}
			// Request shape is judged before any guard, so a malformed request is
			// the same 400 for a foreign, an absent and an own secret: if the
			// existence checks ran first, a 400 would tell the caller the
			// secret is theirs.
			if c.Request().Method == http.MethodGet && !keepMetadataShapeOK(c) {
				return KeepMetadataFailure(c, http.StatusBadRequest)
			}
			res := c.Response()
			orig := res.Writer
			bw := &bufferedWriter{h: http.Header{}}
			res.Writer = bw
			err := next(c)
			res.Writer = orig

			status := bw.status
			var he *echo.HTTPError
			switch {
			case err != nil && errors.As(err, &he):
				status = he.Code
			case err != nil:
				status = http.StatusServiceUnavailable
			case status == 0:
				status = http.StatusOK
			}
			// The buffered writes marked the response committed; the real
			// answer has not been sent yet.
			res.Committed, res.Size, res.Status = false, 0, 0

			if own, _ := c.Get(contextKeyKeepMetadataOwn).(bool); own && err == nil {
				for k, v := range bw.h {
					orig.Header()[k] = v
				}
				res.WriteHeader(status)
				_, _ = res.Write(bw.buf.Bytes())
				return nil
			}
			switch status {
			case http.StatusForbidden:
				status = http.StatusNotFound
			case http.StatusRequestEntityTooLarge:
				status = http.StatusBadRequest
			}
			return KeepMetadataFailure(c, status)
		}
	}
}

// keepMetadataShapeOK: canonical lowercase UUIDs in the path, no query, no
// fragment, no body.
func keepMetadataShapeOK(c echo.Context) bool {
	req := c.Request()
	if req.URL.RawQuery != "" || req.URL.ForceQuery || req.URL.Fragment != "" ||
		req.ContentLength != 0 || len(req.TransferEncoding) > 0 {
		return false
	}
	for _, p := range []string{"ws_id", "secret_id"} {
		raw := c.Param(p)
		id, err := uuid.Parse(raw)
		if err != nil || id.String() != raw {
			return false
		}
	}
	return true
}

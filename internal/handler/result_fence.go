package handler

import (
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Request headers of the result-write precondition (
// docs/extensions/agents-me-contract.md "Fencing a stale worker"). Headers, not
// body fields, so a multipart artifact upload carries them the same way as a
// JSON comment, move or finish, and the Idempotency-Key fingerprint (the body)
// is unchanged.
const (
	HeaderCheckoutToken         = "X-Mesh-Checkout-Token"
	HeaderCheckoutSessionID     = "X-Mesh-Checkout-Session-Id"
	HeaderCheckoutGeneration    = "X-Mesh-Checkout-Generation"
	HeaderExpectedInputRevision = "X-Mesh-Expected-Input-Revision"
)

// resultFenceFromRequest reads the optional precondition of a result write.
// No header: nil, the route behaves as before. A lock is named by the token,
// or by session id + generation (both, as for extend and release); the
// session form binds to the calling actor.
func resultFenceFromRequest(c echo.Context, taskID uuid.UUID) (*domain.ResultFence, error) {
	h := c.Request().Header
	tok := strings.TrimSpace(h.Get(HeaderCheckoutToken))
	sess := strings.TrimSpace(h.Get(HeaderCheckoutSessionID))
	gen := strings.TrimSpace(h.Get(HeaderCheckoutGeneration))
	rev := strings.TrimSpace(h.Get(HeaderExpectedInputRevision))
	if tok == "" && sess == "" && gen == "" && rev == "" {
		return nil, nil
	}
	bad := func(msg string) error { return apierror.BadRequest(msg) }
	f := &domain.ResultFence{TaskID: taskID}
	f.Holder, _ = actorctx.FromContext(c.Request().Context())
	switch {
	case tok != "":
		if sess != "" || gen != "" {
			return nil, bad(HeaderCheckoutToken + " names the lock alone; do not combine it with session id and generation")
		}
		t, err := uuid.Parse(tok)
		if err != nil || t == uuid.Nil {
			return nil, bad("invalid " + HeaderCheckoutToken)
		}
		f.Token = &t
	case sess != "" || gen != "":
		if sess == "" || gen == "" {
			return nil, bad(HeaderCheckoutSessionID + " and " + HeaderCheckoutGeneration + " go together")
		}
		s, err := uuid.Parse(sess)
		if err != nil || s == uuid.Nil {
			return nil, bad("invalid " + HeaderCheckoutSessionID)
		}
		g, err := strconv.ParseInt(gen, 10, 64)
		if err != nil || g <= 0 {
			return nil, bad(HeaderCheckoutGeneration + " must be a positive integer")
		}
		if f.Holder == uuid.Nil {
			return nil, bad("a session-scoped checkout precondition needs an authenticated actor")
		}
		f.SessionID, f.Generation = &s, g
	}
	if rev != "" {
		r, err := strconv.ParseInt(rev, 10, 64)
		if err != nil || r <= 0 {
			return nil, bad(HeaderExpectedInputRevision + " must be a positive integer")
		}
		f.ExpectedInputRevision = &r
	}
	return f, nil
}

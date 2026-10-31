package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Idempotency-Key headers and refusal codes (docs/idempotency.md).
const (
	HeaderIdempotencyKey      = "Idempotency-Key"
	HeaderIdempotentReplayed  = "Idempotent-Replayed"
	CodeIdempotencyKeyReused  = "idempotency_key_reused"
	CodeIdempotencyInProgress = "idempotency_request_in_progress"

	idemMaxKeyLen = 255
	// idemMemBody is how much of a request body is held in memory; a larger
	// one (an artifact upload) is spooled to a temp file while it is hashed.
	idemMemBody = 1 << 20
	// idemMaxStoredResponse bounds a stored response. A larger one is not
	// replayable and its key is released.
	idemMaxStoredResponse = 1 << 20
)

// IdempotencyStore keeps Idempotency-Key records; *postgres.IdempotencyRepo.
// Claim returns a non-nil token when the caller now holds the key, otherwise
// the record that holds it; Complete and Release act only under that token.
type IdempotencyStore interface {
	Claim(ctx context.Context, s domain.IdempotencyScope, hash string) (*domain.IdempotencyRecord, uuid.UUID, error)
	Complete(ctx context.Context, s domain.IdempotencyScope, token uuid.UUID, status int, contentType string, body []byte) error
	Release(ctx context.Context, s domain.IdempotencyScope, token uuid.UUID) error
}

// Idempotency makes a create route safe to retry. A request with an
// Idempotency-Key header claims the key in the scope actor + method + path:
//
//   - first request: runs, and its answer (status below 500) is stored;
//   - repeat with the same body: gets the stored answer, header
//     Idempotent-Replayed: true, and nothing is written again;
//   - repeat with another body: 409 idempotency_key_reused;
//   - repeat while the first is still running: 409
//     idempotency_request_in_progress with Retry-After.
//
// An error returned up the chain is rendered by Echo's error handler and kept
// like a written answer; a 5xx releases the key so a retry runs again. Without the header the route behaves as before. maxBody caps the
// body read for the fingerprint (the handler applies its own, tighter limits).
// Mount it after authentication: the scope needs the actor.
func Idempotency(store IdempotencyStore, maxBody int64) echo.MiddlewareFunc {
	maxBody = max(maxBody, idemMemBody)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			key := c.Request().Header.Get(HeaderIdempotencyKey)
			if key == "" {
				return next(c)
			}
			if !validIdempotencyKey(key) {
				return c.JSON(http.StatusBadRequest, apierror.BadRequest(
					"Idempotency-Key must be 1-255 printable ASCII characters without spaces"))
			}
			req := c.Request()
			actorID, actorType := actorctx.FromContext(req.Context())
			if actorID == uuid.Nil {
				return next(c)
			}

			hash, cleanup, err := fingerprintRequest(req, maxBody)
			defer cleanup()
			if errors.Is(err, errIdemBodyTooLarge) {
				return c.JSON(http.StatusRequestEntityTooLarge, apierror.BadRequest("request body is too large"))
			}
			if err != nil {
				return c.JSON(http.StatusBadRequest, apierror.BadRequest("could not read request body"))
			}

			scope := domain.IdempotencyScope{
				ActorType: actorType, ActorID: actorID,
				Method: req.Method, Path: req.URL.Path, Key: key,
			}
			ctx := context.WithoutCancel(req.Context())
			rec, token, err := store.Claim(ctx, scope, hash)
			if err != nil {
				log.Printf("[idempotency] claim %s %s: %v", req.Method, req.URL.Path, err)
				return c.JSON(http.StatusServiceUnavailable, apierror.ServiceUnavailable("idempotency store unavailable, retry"))
			}
			if token == uuid.Nil {
				return answerHeldKey(c, rec, hash)
			}

			release := func() {
				if err := store.Release(ctx, scope, token); err != nil {
					log.Printf("[idempotency] release %s %s: %v", req.Method, req.URL.Path, err)
				}
			}
			rw := &idemRecorder{ResponseWriter: c.Response().Writer}
			c.Response().Writer = rw
			// A panic answers 500 further up (Recover): free the key for the
			// retry now instead of leaving it in_flight until it turns stale.
			defer func() {
				if p := recover(); p != nil {
					c.Response().Writer = rw.ResponseWriter
					release()
					panic(p)
				}
			}()
			// An error returned up the chain is rendered here, inside the
			// recorder, so a 4xx from it is kept and replayed like any other
			// answer; Echo's error handler then sees a committed response.
			if herr := next(c); herr != nil {
				c.Error(herr)
			}
			c.Response().Writer = rw.ResponseWriter

			status := c.Response().Status
			if status >= http.StatusInternalServerError || rw.overflow {
				release()
				return nil
			}
			if err := store.Complete(ctx, scope, token, status, c.Response().Header().Get(echo.HeaderContentType), rw.buf.Bytes()); err != nil {
				// The write happened; only the replay is lost. Leave the claim:
				// it turns stale and a retry then runs again.
				log.Printf("[idempotency] complete %s %s: %v", req.Method, req.URL.Path, err)
			}
			return nil
		}
	}
}

// answerHeldKey answers a request whose key is already held.
func answerHeldKey(c echo.Context, rec *domain.IdempotencyRecord, hash string) error {
	if rec.RequestHash != hash {
		return c.JSON(http.StatusConflict, &apierror.Error{
			Code: http.StatusConflict, ErrorCode: CodeIdempotencyKeyReused,
			Message: "this Idempotency-Key was already used with a different request body",
		})
	}
	if rec.State != domain.IdempotencyDone || rec.ResponseStatus == nil {
		c.Response().Header().Set("Retry-After", "1")
		return c.JSON(http.StatusConflict, &apierror.Error{
			Code: http.StatusConflict, ErrorCode: CodeIdempotencyInProgress,
			Message: "a request with this Idempotency-Key is still being processed, retry shortly",
		})
	}
	c.Response().Header().Set(HeaderIdempotentReplayed, "true")
	ct := echo.MIMEApplicationJSON
	if rec.ResponseType != nil && *rec.ResponseType != "" {
		ct = *rec.ResponseType
	}
	return c.Blob(*rec.ResponseStatus, ct, rec.ResponseBody)
}

// validIdempotencyKey accepts 1-255 visible ASCII characters (UUIDs, ULIDs and
// the like), so a key never smuggles whitespace or control bytes into storage.
func validIdempotencyKey(k string) bool {
	if k == "" || len(k) > idemMaxKeyLen {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

var errIdemBodyTooLarge = errors.New("body too large")

// fingerprintRequest hashes the request body and puts an unread copy back on
// the request for the handler. A multipart body is hashed part by part (see
// hashMultipart), since clients pick a fresh random boundary on every retry of
// the same upload. cleanup removes the spool file, if any; it is safe to call
// whatever the error.
func fingerprintRequest(req *http.Request, maxBody int64) (hash string, cleanup func(), err error) {
	cleanup = func() {}
	ct := req.Header.Get(echo.HeaderContentType)
	mediaType, params, _ := mime.ParseMediaType(ct)
	if req.Body == nil || req.Body == http.NoBody {
		hash, err = hashBody(mediaType, params, bytes.NewReader(nil))
		return hash, cleanup, err
	}

	var mem bytes.Buffer
	n, err := io.Copy(&mem, io.LimitReader(req.Body, idemMemBody))
	if err != nil {
		return "", cleanup, err
	}
	if n < idemMemBody {
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(mem.Bytes()))
		hash, err = hashBody(mediaType, params, bytes.NewReader(mem.Bytes()))
		return hash, cleanup, err
	}

	f, err := os.CreateTemp("", "mesh-idem-*")
	if err != nil {
		return "", cleanup, err
	}
	cleanup = func() { _ = f.Close(); _ = os.Remove(f.Name()) }
	if _, err = f.Write(mem.Bytes()); err != nil {
		return "", cleanup, err
	}
	rest, err := io.Copy(f, io.LimitReader(req.Body, maxBody-n+1))
	if err != nil {
		return "", cleanup, err
	}
	if n+rest > maxBody {
		return "", cleanup, errIdemBodyTooLarge
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return "", cleanup, err
	}
	if hash, err = hashBody(mediaType, params, f); err != nil {
		return "", cleanup, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return "", cleanup, err
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(f)
	return hash, cleanup, nil
}

// hashBody fingerprints a body of the given media type.
func hashBody(mediaType string, params map[string]string, body io.ReadSeeker) (string, error) {
	h := sha256.New()
	h.Write([]byte(mediaType + "\n"))
	if b := params["boundary"]; strings.HasPrefix(mediaType, "multipart/") && b != "" {
		if sum, ok := hashMultipart(body, b); ok {
			h.Write([]byte("parts\n"))
			h.Write(sum)
			return hex.EncodeToString(h.Sum(nil)), nil
		}
		// Not parseable as multipart: the handler rejects it anyway; hash
		// the raw bytes so the same bytes still match.
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
	}
	h.Write([]byte("raw\n"))
	if _, err := io.Copy(h, body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashMultipart hashes the parts of a multipart body, leaving the boundary
// out: each part's headers and raw content get a digest of their own, and the
// result is the hash of those digests in order. File bytes are never rewritten,
// so content that happens to contain the boundary string stays distinct.
func hashMultipart(body io.Reader, boundary string) ([]byte, bool) {
	outer := sha256.New()
	mr := multipart.NewReader(body, boundary)
	for {
		part, err := mr.NextRawPart()
		if errors.Is(err, io.EOF) {
			return outer.Sum(nil), true
		}
		if err != nil {
			return nil, false
		}
		ph := sha256.New()
		keys := make([]string, 0, len(part.Header))
		for k := range part.Header {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, v := range part.Header[k] {
				writeLenPrefixed(ph, k)
				writeLenPrefixed(ph, v)
			}
		}
		ph.Write([]byte{0})
		if _, err := io.Copy(ph, part); err != nil {
			return nil, false
		}
		outer.Write(ph.Sum(nil))
	}
}

func writeLenPrefixed(w io.Writer, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	_, _ = w.Write(n[:])
	_, _ = io.WriteString(w, s)
}

// idemRecorder passes the response through and keeps a copy of its body for
// the replay; past idemMaxStoredResponse it stops copying and marks overflow.
type idemRecorder struct {
	http.ResponseWriter
	buf      bytes.Buffer
	overflow bool
}

func (r *idemRecorder) Write(p []byte) (int, error) {
	if !r.overflow {
		if r.buf.Len()+len(p) > idemMaxStoredResponse {
			r.overflow = true
			r.buf.Reset()
		} else {
			r.buf.Write(p)
		}
	}
	return r.ResponseWriter.Write(p)
}

func (r *idemRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

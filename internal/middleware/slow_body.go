package middleware

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

// Slow-body instrumentation.
//
// From 2026-10-25 12:32Z about 0.25% of POSTs with a JSON body hung for
// 20–114 s and ended in 400 (c.Bind failing once the client had given up) or
// 500 57014, while every other request in the same seconds took tens of ms.
// The access log only says "-> 400 35000ms": it cannot tell whether the
// seconds were spent waiting for the body to arrive (network / client) or in
// the handler after the body was read. SlowBody answers that for the next case,
// and BindErrorLogger records why a Bind failed (the handlers answer a bare
// "invalid request body" and drop the error).

// DefaultSlowBodyThreshold is the request duration past which SlowBody logs.
const DefaultSlowBodyThreshold = 2 * time.Second

// bindErrContextKey is where BindErrorLogger leaves the last Bind error so
// SlowBody can put it on the same line as the body timings.
const bindErrContextKey = "mw.bind_err"

// timedBodyContextKey holds the *timedBody SlowBody installed, so the Bind log
// can report bytes_read even after another middleware replaced req.Body.
const timedBodyContextKey = "mw.timed_body"

// SlowBodyConfig configures SlowBody.
type SlowBodyConfig struct {
	// Threshold is the total request duration past which a request with a
	// body is a candidate for logging. Zero means DefaultSlowBodyThreshold.
	Threshold time.Duration
	// Redact rewrites the request path before it is logged (invite tokens
	// live in the path). Nil logs the path as is.
	Redact func(string) string
	// Logf writes the line. Nil means log.Printf.
	Logf func(format string, args ...any)
	// Now is the clock. Nil means time.Now; tests override it.
	Now func() time.Time
}

// SlowBody wraps the request body with a reader that records how many bytes
// were read and when the first and last of them arrived, and logs one
// `slow-body` line for a request with a body that took longer than the
// threshold and either failed (status >= 400, a read or Bind error) or spent
// longer than the threshold reading the body. Successful long-polls whose
// time is all in the handler (runner chat claim) stay out of the log.
//
// Times are relative to the moment the middleware ran, i.e. after the
// headers were parsed:
//   - first_byte_ms: until the first body byte reached the handler
//   - body_ms:       until the last body byte (EOF, read error, or last read)
//   - handler_ms:    from the last body byte to the end of the handler
//
// The path is logged percent-escaped so a client cannot forge log lines with
// an encoded newline.
//
// The response and API behaviour are unchanged: the wrapper passes reads
// through untouched.
func SlowBody(cfg SlowBodyConfig) echo.MiddlewareFunc {
	threshold := cfg.Threshold
	if threshold <= 0 {
		threshold = DefaultSlowBodyThreshold
	}
	redact := cfg.Redact
	if redact == nil {
		redact = func(s string) string { return s }
	}
	logf := cfg.Logf
	if logf == nil {
		logf = log.Printf
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()
			if req.Body == nil || req.Body == http.NoBody {
				return next(c)
			}

			start := now()
			tb := &timedBody{rc: req.Body, now: now}
			req.Body = tb
			c.Set(timedBodyContextKey, tb)

			err := next(c)

			end := now()
			total := end.Sub(start)
			if total <= threshold {
				return err
			}

			st := tb.snapshot()
			bodyEnd := st.last
			if bodyEnd.IsZero() {
				bodyEnd = start
			}
			bodyDur := bodyEnd.Sub(start)

			status := c.Response().Status
			if err != nil {
				var he *echo.HTTPError
				if errors.As(err, &he) {
					status = he.Code
				} else if !c.Response().Committed {
					status = http.StatusInternalServerError
				}
			}

			bindErr, _ := c.Get(bindErrContextKey).(error)
			failed := status >= 400 || st.err != nil || bindErr != nil
			if !failed && bodyDur <= threshold {
				return err
			}

			firstByte := int64(-1)
			if !st.first.IsZero() {
				firstByte = st.first.Sub(start).Milliseconds()
			}

			logf("WARN slow-body: %s %s route=%s status=%d content_length=%d bytes_read=%d eof=%t "+
				"first_byte_ms=%d body_ms=%d handler_ms=%d total_ms=%d read_err=%q bind_err=%q",
				req.Method, redact(req.URL.EscapedPath()), c.Path(), status, req.ContentLength, st.n, st.eof,
				firstByte, bodyDur.Milliseconds(), end.Sub(bodyEnd).Milliseconds(), total.Milliseconds(),
				errString(st.err), errString(bindErr))
			return err
		}
	}
}

// BindErrorLogger wraps an echo.Binder so every failed Bind is logged with
// its cause and left on the context for SlowBody. Handlers keep answering
// their own 400; only the log changes.
type BindErrorLogger struct {
	Inner  echo.Binder
	Redact func(string) string
	Logf   func(format string, args ...any)
}

// Bind implements echo.Binder.
func (b *BindErrorLogger) Bind(i any, c echo.Context) error {
	inner := b.Inner
	if inner == nil {
		inner = &echo.DefaultBinder{}
	}
	err := inner.Bind(i, c)
	if err == nil {
		return nil
	}
	c.Set(bindErrContextKey, err)

	logf := b.Logf
	if logf == nil {
		logf = log.Printf
	}
	// Escaped, not decoded: a %0A in the path must not start a new log line.
	path := c.Request().URL.EscapedPath()
	if b.Redact != nil {
		path = b.Redact(path)
	}
	var bytesRead int64 = -1
	if tb, ok := c.Get(timedBodyContextKey).(*timedBody); ok {
		bytesRead = tb.snapshot().n
	}
	logf("WARN bind-error: %s %s route=%s content_length=%d bytes_read=%d err=%q",
		c.Request().Method, path, c.Path(), c.Request().ContentLength, bytesRead, errString(bindErrCause(err)))
	return err
}

// bindErrCause unwraps echo's HTTPError so the log carries the decoder's
// message ("unexpected EOF", "i/o timeout") rather than just "Bad Request".
func bindErrCause(err error) error {
	var he *echo.HTTPError
	if errors.As(err, &he) && he.Internal != nil {
		return fmt.Errorf("%v: %w", he.Message, he.Internal)
	}
	return err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// timedBody is an io.ReadCloser that records byte count and arrival times.
type timedBody struct {
	rc  io.ReadCloser
	now func() time.Time

	mu    sync.Mutex
	n     int64
	first time.Time
	last  time.Time
	eof   bool
	err   error
}

type timedBodyState struct {
	n           int64
	first, last time.Time
	eof         bool
	err         error
}

func (t *timedBody) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	at := t.now()

	t.mu.Lock()
	defer t.mu.Unlock()
	if n > 0 {
		t.n += int64(n)
		if t.first.IsZero() {
			t.first = at
		}
		t.last = at
	}
	switch {
	case err == io.EOF:
		t.eof = true
		t.last = at
	case err != nil:
		if t.err == nil {
			t.err = err
		}
		t.last = at
	}
	return n, err
}

func (t *timedBody) Close() error { return t.rc.Close() }

func (t *timedBody) snapshot() timedBodyState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return timedBodyState{n: t.n, first: t.first, last: t.last, eof: t.eof, err: t.err}
}

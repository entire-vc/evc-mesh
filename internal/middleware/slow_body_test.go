package middleware

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// pausingReader hands out head, sleeps for pause, then hands out tail —
// the shape of a body whose second half arrives late over the network.
type pausingReader struct {
	head, tail []byte
	pause      time.Duration
	stage      int
}

func (r *pausingReader) Read(p []byte) (int, error) {
	switch r.stage {
	case 0:
		r.stage = 1
		return copy(p, r.head), nil
	case 1:
		time.Sleep(r.pause)
		r.stage = 2
		return copy(p, r.tail), nil
	default:
		return 0, io.EOF
	}
}

// stalledReader hands out head and then fails the way a body cut off by the
// client does.
type stalledReader struct {
	head  []byte
	pause time.Duration
	done  bool
}

func (r *stalledReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(p, r.head), nil
	}
	time.Sleep(r.pause)
	return 0, io.ErrUnexpectedEOF
}

type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *logSink) logf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, fmt.Sprintf(format, args...))
}

func (s *logSink) find(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lines {
		if strings.Contains(l, prefix) {
			return l
		}
	}
	return ""
}

func field(t *testing.T, line, name string) string {
	t.Helper()
	m := regexp.MustCompile(` ` + name + `=("[^"]*"|\S+)`).FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("field %s missing in %q", name, line)
	}
	return strings.Trim(m[1], `"`)
}

func intField(t *testing.T, line, name string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(field(t, line, name), 10, 64)
	if err != nil {
		t.Fatalf("field %s=%q: %v", name, field(t, line, name), err)
	}
	return v
}

type commentReq struct {
	Body string `json:"body"`
}

// newSlowBodyEcho wires SlowBody and BindErrorLogger the way cmd/api does
// and registers a handler shaped like the comment create handler.
func newSlowBodyEcho(sink *logSink, threshold, handlerWork time.Duration) *echo.Echo {
	e := echo.New()
	e.Binder = &BindErrorLogger{Logf: sink.logf}
	e.Use(SlowBody(SlowBodyConfig{Threshold: threshold, Logf: sink.logf}))
	e.POST("/tasks/:id/comments", func(c echo.Context) error {
		var req commentReq
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		}
		time.Sleep(handlerWork)
		return c.JSON(http.StatusCreated, req)
	})
	return e
}

func serveSlowBody(e *echo.Echo, body io.Reader, contentLength int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/tasks/abc/comments", io.NopCloser(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.ContentLength = contentLength
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestSlowBody_LateBodyAttributedToBody(t *testing.T) {
	sink := &logSink{}
	e := newSlowBodyEcho(sink, 50*time.Millisecond, 0)
	full := `{"body":"hello"}`
	rec := serveSlowBody(e, &pausingReader{head: []byte(full[:5]), tail: []byte(full[5:]), pause: 150 * time.Millisecond}, int64(len(full)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s: the wrapper must not change API behaviour", rec.Code, rec.Body)
	}
	line := sink.find("slow-body")
	if line == "" {
		t.Fatalf("no slow-body line for a body that arrived 150ms late; log: %v", sink.lines)
	}
	if got := intField(t, line, "bytes_read"); got != int64(len(full)) {
		t.Errorf("bytes_read=%d, want %d", got, len(full))
	}
	if got := intField(t, line, "content_length"); got != int64(len(full)) {
		t.Errorf("content_length=%d, want %d", got, len(full))
	}
	// eof stays false here: json.Decoder stops at the end of the value and
	// never asks for EOF, so bytes_read == content_length is the signal that
	// the whole body arrived.
	if body := intField(t, line, "body_ms"); body < 140 {
		t.Errorf("body_ms=%d, want >=140: the seconds were spent waiting for the body", body)
	}
	if h := intField(t, line, "handler_ms"); h > 100 {
		t.Errorf("handler_ms=%d, want small: the handler itself was fast", h)
	}
	if fb := intField(t, line, "first_byte_ms"); fb < 0 || fb > 100 {
		t.Errorf("first_byte_ms=%d, want 0..100", fb)
	}
	if field(t, line, "route") != "/tasks/:id/comments" {
		t.Errorf("route=%s", field(t, line, "route"))
	}
}

func TestSlowBody_TruncatedBodyLogsBindCause(t *testing.T) {
	sink := &logSink{}
	e := newSlowBodyEcho(sink, 50*time.Millisecond, 0)
	full := `{"body":"hello"}`
	rec := serveSlowBody(e, &stalledReader{head: []byte(full[:5]), pause: 120 * time.Millisecond}, int64(len(full)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 from the handler's own Bind check", rec.Code)
	}
	bind := sink.find("bind-error")
	if bind == "" {
		t.Fatalf("no bind-error line; log: %v", sink.lines)
	}
	if !strings.Contains(field(t, bind, "err"), "unexpected EOF") {
		t.Errorf("bind-error err=%q, want the decoder cause (unexpected EOF)", field(t, bind, "err"))
	}
	if got := intField(t, bind, "bytes_read"); got != 5 {
		t.Errorf("bind-error bytes_read=%d, want 5", got)
	}

	line := sink.find("slow-body")
	if line == "" {
		t.Fatalf("no slow-body line for a 400 after 120ms; log: %v", sink.lines)
	}
	if got := intField(t, line, "status"); got != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", got)
	}
	if got := intField(t, line, "bytes_read"); got != 5 {
		t.Errorf("bytes_read=%d, want 5 of %d", got, len(full))
	}
	if field(t, line, "eof") != "false" {
		t.Errorf("eof=%s, want false", field(t, line, "eof"))
	}
	if !strings.Contains(field(t, line, "read_err"), "unexpected EOF") {
		t.Errorf("read_err=%q, want unexpected EOF", field(t, line, "read_err"))
	}
	if !strings.Contains(field(t, line, "bind_err"), "unexpected EOF") {
		t.Errorf("bind_err=%q, want unexpected EOF", field(t, line, "bind_err"))
	}
}

func TestSlowBody_SlowHandlerAttributedToHandler(t *testing.T) {
	sink := &logSink{}
	e := echo.New()
	e.Binder = &BindErrorLogger{Logf: sink.logf}
	e.Use(SlowBody(SlowBodyConfig{Threshold: 50 * time.Millisecond, Logf: sink.logf}))
	e.POST("/tasks/:id/comments", func(c echo.Context) error {
		var req commentReq
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, nil)
		}
		time.Sleep(120 * time.Millisecond)
		return c.JSON(http.StatusInternalServerError, nil)
	})
	full := `{"body":"hello"}`
	serveSlowBody(e, strings.NewReader(full), int64(len(full)))

	line := sink.find("slow-body")
	if line == "" {
		t.Fatalf("no slow-body line for a slow 500; log: %v", sink.lines)
	}
	if b := intField(t, line, "body_ms"); b > 40 {
		t.Errorf("body_ms=%d, want small", b)
	}
	if h := intField(t, line, "handler_ms"); h < 110 {
		t.Errorf("handler_ms=%d, want >=110: the seconds were in the handler", h)
	}
	if sink.find("bind-error") != "" {
		t.Errorf("bind-error logged for a valid body: %v", sink.lines)
	}
}

func TestSlowBody_QuietForFastAndForSuccessfulLongPoll(t *testing.T) {
	sink := &logSink{}
	full := `{"body":"hello"}`

	fast := newSlowBodyEcho(sink, 50*time.Millisecond, 0)
	if rec := serveSlowBody(fast, strings.NewReader(full), int64(len(full))); rec.Code != http.StatusCreated {
		t.Fatalf("fast status %d", rec.Code)
	}
	// A long-poll that holds the request in the handler and succeeds is
	// expected to be slow; it must not flood the log.
	longPoll := newSlowBodyEcho(sink, 50*time.Millisecond, 120*time.Millisecond)
	if rec := serveSlowBody(longPoll, strings.NewReader(full), int64(len(full))); rec.Code != http.StatusCreated {
		t.Fatalf("long-poll status %d", rec.Code)
	}
	if len(sink.lines) != 0 {
		t.Fatalf("expected no log lines, got %v", sink.lines)
	}
}

func TestSlowBody_RedactsPath(t *testing.T) {
	sink := &logSink{}
	e := echo.New()
	e.Use(SlowBody(SlowBodyConfig{
		Threshold: 10 * time.Millisecond,
		Logf:      sink.logf,
		Redact:    func(p string) string { return strings.Replace(p, "secret-token", "<redacted>", 1) },
	}))
	e.POST("/invites/:token", func(c echo.Context) error {
		_, _ = io.ReadAll(c.Request().Body)
		time.Sleep(30 * time.Millisecond)
		return c.NoContent(http.StatusBadRequest)
	})
	req := httptest.NewRequest(http.MethodPost, "/invites/secret-token", strings.NewReader("{}"))
	e.ServeHTTP(httptest.NewRecorder(), req)

	line := sink.find("slow-body")
	if line == "" || strings.Contains(line, "secret-token") || !strings.Contains(line, "<redacted>") {
		t.Fatalf("path not redacted: %q", line)
	}
}

func TestSlowBody_EscapesPathAgainstLogForging(t *testing.T) {
	sink := &logSink{}
	e := echo.New()
	e.Binder = &BindErrorLogger{Logf: sink.logf}
	e.Use(SlowBody(SlowBodyConfig{Threshold: 10 * time.Millisecond, Logf: sink.logf}))
	e.POST("/x/:id", func(c echo.Context) error {
		var req commentReq
		_ = c.Bind(&req)
		time.Sleep(30 * time.Millisecond)
		return c.NoContent(http.StatusBadRequest)
	})
	req := httptest.NewRequest(http.MethodPost, "/x/a%0AWARN%20forged", strings.NewReader("{"))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	e.ServeHTTP(httptest.NewRecorder(), req)

	for _, prefix := range []string{"slow-body", "bind-error"} {
		line := sink.find(prefix)
		if line == "" {
			t.Fatalf("no %s line; log: %v", prefix, sink.lines)
		}
		if strings.Contains(line, "\n") {
			t.Errorf("%s line carries a raw newline from the path: %q", prefix, line)
		}
		if !strings.Contains(line, "/x/a%0AWARN%20forged") {
			t.Errorf("%s line lacks the escaped path: %q", prefix, line)
		}
	}
}

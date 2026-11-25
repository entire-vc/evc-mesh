package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Headers the MCP server sends with every request (unit U2). Missing or
// invalid values are recorded as absent; caller_kind then falls back to ui for
// a user's browser session and unknown otherwise.
const (
	HeaderMeshCaller    = "X-Mesh-Caller"
	HeaderMeshSessionID = "X-Mesh-Session-Id"
	HeaderMeshTaskID    = "X-Mesh-Task-Id"
	// HeaderMeshTool names the MCP tool a request serves when one tool makes
	// several requests: get_task(include_comments=true) is GET /tasks/:id plus
	// GET /tasks/:id/comments. The primary request carries get_task+comments
	// (skip counting); the comments request carries get_task and books the
	// single call according to the comments actually returned.
	HeaderMeshTool = "X-Mesh-Tool"
)

const (
	memoryReadFlushInterval = 15 * time.Second
	memoryReadPurgeInterval = time.Hour
	// memoryReadQueueSize bounds the hand-off from the request path. A full
	// queue drops the event (counted, logged on flush) instead of blocking the
	// request: the journal is observability, not a record of truth.
	memoryReadQueueSize = 1024
	// memoryReadCaptureLimit caps the bytes of a response kept for parsing; a
	// larger body is recorded with its size and an error, not its rows.
	memoryReadCaptureLimit = 4 << 20
	memoryReadTextLimit    = 500
	// MemoryReadRawRetention and MemoryReadQueryTextRetention are the journal's
	// retention: raw rows 90 days, query_text cleared after 30.
	MemoryReadRawRetention       = 90 * 24 * time.Hour
	MemoryReadQueryTextRetention = 30 * 24 * time.Hour
)

// memoryReadOps maps "METHOD route-pattern" of every memory read to its op.
// get_memory covers both addressings (by key and by id); route tells them
// apart in the journal.
var memoryReadOps = map[string]string{
	"GET /api/v1/memories/search":                  "recall",
	"GET /api/v1/memories/recall-final/v1":         "recall-final",
	"GET /api/v1/memories/recall_graph":            "recall_graph",
	"GET /api/v1/memories/by-key":                  "get_memory",
	"GET /api/v1/memories/:id":                     "get_memory",
	"GET /api/v1/projects/:proj_id/knowledge":      "get_project_knowledge",
	"GET /api/v1/projects/:proj_id/updates":        "get_project_updates",
	"GET /api/v1/projects/:proj_id/updates/latest": "get_project_updates",
}

// MemoryReadEventSink persists journal rows and applies the retention.
type MemoryReadEventSink interface {
	InsertMemoryReadEvents(ctx context.Context, events []domain.MemoryReadEvent) error
	PurgeMemoryReadEvents(ctx context.Context, rawBefore, queryTextBefore time.Time) (deleted, cleared int64, err error)
}

// rawMemoryRead is what the request path hands to the writer: request facts
// and the response bytes. Parsing happens in the writer goroutine.
type rawMemoryRead struct {
	event     domain.MemoryReadEvent
	body      []byte
	truncated bool
}

// MemoryReadLog journals memory reads into memory_read_events. The middleware
// tees the response of the routes in memoryReadOps and enqueues it without
// waiting; Run parses the queued responses and writes them in batches every
// 15 s, the same shape as ToolBreakdownTracker. A nil or disabled MemoryReadLog
// is inert.
type MemoryReadLog struct {
	enabled bool
	sink    MemoryReadEventSink
	queue   chan rawMemoryRead
	dropped atomic.Int64

	mu      sync.Mutex
	pending []domain.MemoryReadEvent
	now     func() time.Time
}

// NewMemoryReadLog creates the journal writer. enabled=false (MEMORY_READ_LOG=
// false) makes the middleware a pass-through and Run a no-op.
func NewMemoryReadLog(sink MemoryReadEventSink, enabled bool) *MemoryReadLog {
	return &MemoryReadLog{
		enabled: enabled && sink != nil,
		sink:    sink,
		queue:   make(chan rawMemoryRead, memoryReadQueueSize),
		now:     time.Now,
	}
}

// Enabled reports whether the journal is written.
func (l *MemoryReadLog) Enabled() bool { return l != nil && l.enabled }

// Middleware records one journal row per memory read. Register it after the
// auth middleware (agent/user identity in context).
func (l *MemoryReadLog) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !l.Enabled() {
				return next(c)
			}
			route := c.Request().Method + " " + c.Path()
			op, ok := memoryReadOps[route]
			if !ok {
				return next(c)
			}
			start := l.now()
			res := c.Response()
			tee := &teeResponseWriter{ResponseWriter: res.Writer, limit: memoryReadCaptureLimit}
			res.Writer = tee
			err := next(c)
			res.Writer = tee.ResponseWriter

			ev := memoryReadRequestFacts(c, op, route)
			ev.TS = start
			ev.DurationMS = int(l.now().Sub(start).Milliseconds())
			ev.StatusCode = res.Status
			if err != nil {
				var he *echo.HTTPError
				ev.StatusCode = http.StatusInternalServerError
				if errors.As(err, &he) {
					ev.StatusCode = he.Code
				}
				msg := truncateText(err.Error(), memoryReadTextLimit)
				ev.Error = &msg
			}
			ev.ResponseChars = tee.total
			l.enqueue(rawMemoryRead{event: ev, body: tee.buf.Bytes(), truncated: tee.truncated})
			return err
		}
	}
}

func (l *MemoryReadLog) enqueue(r rawMemoryRead) {
	select {
	case l.queue <- r:
	default:
		l.dropped.Add(1)
	}
}

// Run parses queued reads, flushes them every 15 s and applies the retention
// hourly. Blocks until ctx is cancelled.
func (l *MemoryReadLog) Run(ctx context.Context) {
	if !l.Enabled() {
		return
	}
	flush := time.NewTicker(memoryReadFlushInterval)
	defer flush.Stop()
	purge := time.NewTicker(memoryReadPurgeInterval)
	defer purge.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-l.queue:
			l.accept(r)
		case <-flush.C:
			l.flush(ctx)
		case <-purge.C:
			l.purge(ctx)
		}
	}
}

// Flush drains the queue and writes everything pending. Call it on graceful
// shutdown and in tests.
func (l *MemoryReadLog) Flush(ctx context.Context) {
	if !l.Enabled() {
		return
	}
	for {
		select {
		case r := <-l.queue:
			l.accept(r)
		default:
			l.flush(ctx)
			return
		}
	}
}

// Purge applies the retention now.
func (l *MemoryReadLog) Purge(ctx context.Context) {
	if l.Enabled() {
		l.purge(ctx)
	}
}

func (l *MemoryReadLog) accept(r rawMemoryRead) {
	ev := r.event
	parseMemoryReadResponse(&ev, r.body, r.truncated)
	l.mu.Lock()
	if len(l.pending) < memoryReadQueueSize {
		l.pending = append(l.pending, ev)
	} else {
		l.dropped.Add(1)
	}
	l.mu.Unlock()
}

func (l *MemoryReadLog) flush(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	l.mu.Lock()
	batch := l.pending
	l.pending = nil
	l.mu.Unlock()
	if n := l.dropped.Swap(0); n > 0 {
		log.Printf("[memory-read-log] queue full: %d reads not journaled", n)
	}
	if len(batch) == 0 {
		return
	}
	if err := l.sink.InsertMemoryReadEvents(ctx, batch); err != nil {
		log.Printf("[memory-read-log] flush of %d reads failed: %v", len(batch), err)
	}
}

func (l *MemoryReadLog) purge(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := l.now()
	deleted, cleared, err := l.sink.PurgeMemoryReadEvents(ctx, now.Add(-MemoryReadRawRetention), now.Add(-MemoryReadQueryTextRetention))
	if err != nil {
		log.Printf("[memory-read-log] retention purge failed: %v", err)
		return
	}
	if deleted > 0 || cleared > 0 {
		log.Printf("[memory-read-log] retention: %d rows deleted, %d query texts cleared", deleted, cleared)
	}
}

// CallerKindOf classifies the caller of a request: a valid X-Mesh-Caller wins,
// else a user session is ui, else unknown.
func CallerKindOf(c echo.Context) string {
	if v := strings.ToLower(strings.TrimSpace(c.Request().Header.Get(HeaderMeshCaller))); v != "" && domain.ValidCallerKind(v) {
		return v
	}
	if t, _ := c.Get(ContextKeyAuthType).(string); t == AuthTypeUser {
		return domain.CallerKindUI
	}
	return domain.CallerKindUnknown
}

func memoryReadRequestFacts(c echo.Context, op, route string) domain.MemoryReadEvent {
	req := c.Request()
	ev := domain.MemoryReadEvent{
		CallerKind: CallerKindOf(c),
		Route:      route,
		UserAgent:  truncateText(req.UserAgent(), memoryReadTextLimit),
		Op:         op,
	}
	if id, err := GetWorkspaceID(c); err == nil && id != uuid.Nil {
		ev.WorkspaceID = &id
	} else if id, perr := uuid.Parse(c.QueryParam("workspace_id")); perr == nil {
		ev.WorkspaceID = &id
	}
	if id, err := uuid.Parse(c.Param("proj_id")); err == nil {
		ev.ProjectID = &id
	} else if id, perr := uuid.Parse(c.QueryParam("project_id")); perr == nil {
		ev.ProjectID = &id
	}
	if IsAgent(c) {
		if id, err := GetAgentID(c); err == nil {
			ev.AgentID = &id
		}
	} else if id, err := GetUserID(c); err == nil {
		ev.UserID = &id
	}
	if s := strings.TrimSpace(req.Header.Get(HeaderMeshSessionID)); s != "" {
		s = truncateText(s, 200)
		ev.SessionID = &s
	}
	if id, err := uuid.Parse(strings.TrimSpace(req.Header.Get(HeaderMeshTaskID))); err == nil {
		ev.TaskID = &id
	}
	var query string
	switch op {
	case "recall", "recall-final":
		query = c.QueryParam("q")
	case "recall_graph":
		query = c.QueryParam("query")
	case "get_memory":
		if query = c.QueryParam("key"); query == "" {
			query = c.Param("id")
		}
	}
	if query = strings.TrimSpace(query); query != "" {
		sum := sha256.Sum256([]byte(strings.ToLower(query)))
		h := hex.EncodeToString(sum[:])
		text := truncateText(query, 2000)
		ev.QueryHash, ev.QueryText = &h, &text
	}
	return ev
}

// memoryReadRow is the subset of a served row the journal needs. It decodes
// memory rows (recall, final recall, by key, by id, knowledge), graph rows and
// project updates alike; absent fields stay zero.
type memoryReadRow struct {
	ID             uuid.UUID `json:"id"`
	Key            string    `json:"key"`
	Scope          string    `json:"scope"`
	SourceType     string    `json:"source_type"`
	Source         string    `json:"source"`
	Tags           []string  `json:"tags"`
	Status         string    `json:"status"`
	Score          *float64  `json:"score"`
	CompositeScore *float64  `json:"composite_score"`
}

// parseMemoryReadResponse fills the result fields of ev from the response body.
// A non-2xx answer has no rows; an unreadable one is recorded with an error.
func parseMemoryReadResponse(ev *domain.MemoryReadEvent, body []byte, truncated bool) {
	ev.Results = []domain.MemoryReadResult{}
	if ev.StatusCode < 200 || ev.StatusCode > 299 {
		if ev.Error == nil {
			msg := truncateText(strings.TrimSpace(string(body)), memoryReadTextLimit)
			ev.Error = &msg
		}
		return
	}
	if truncated {
		msg := "response over capture limit: rows not journaled"
		ev.Error = &msg
		return
	}
	rows, mode, err := decodeMemoryReadRows(ev.Op, ev.Route, body)
	if err != nil {
		msg := truncateText("unreadable response: "+err.Error(), memoryReadTextLimit)
		ev.Error = &msg
		return
	}
	if mode != "" {
		ev.SearchMode = &mode
	}
	for i, r := range rows {
		source := r.Source
		if source == "" {
			if ev.Op == "get_project_updates" {
				source = domain.ReadSourceProjectUpdate
			} else {
				source = domain.ReadSourceOf(domain.MemorySourceType(r.SourceType), domain.MemoryScope(r.Scope))
			}
		}
		score := r.Score
		if score == nil {
			score = r.CompositeScore
		}
		superseded := r.Status == string(domain.MemoryStatusSuperseded)
		ev.Results = append(ev.Results, domain.MemoryReadResult{
			ID: r.ID, Key: r.Key, Source: source, Rank: i + 1, Score: score, Superseded: superseded,
		})
		if superseded {
			ev.SupersededCount++
		}
		for _, t := range r.Tags {
			if t == domain.KindPinned {
				ev.PinnedCount++
				break
			}
		}
		if score != nil && (ev.TopScore == nil || *score > *ev.TopScore) {
			s := *score
			ev.TopScore = &s
		}
	}
	ev.ResultCount = len(ev.Results)
}

func decodeMemoryReadRows(op, route string, body []byte) ([]memoryReadRow, string, error) {
	switch {
	case route == "GET /api/v1/memories/:id" || route == "GET /api/v1/projects/:proj_id/updates/latest":
		var one memoryReadRow
		if err := json.Unmarshal(body, &one); err != nil {
			return nil, "", err
		}
		return []memoryReadRow{one}, "", nil
	case op == "get_project_knowledge":
		var env struct {
			WorkspaceMemories []memoryReadRow `json:"workspace_memories"`
			ProjectMemories   []memoryReadRow `json:"project_memories"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, "", err
		}
		return append(env.ProjectMemories, env.WorkspaceMemories...), "", nil
	default:
		var env struct {
			Items      []memoryReadRow `json:"items"`
			SearchMode string          `json:"search_mode"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, "", err
		}
		return env.Items, env.SearchMode, nil
	}
}

func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// teeResponseWriter copies what a handler writes, up to limit bytes, and
// counts the total.
type teeResponseWriter struct {
	http.ResponseWriter
	buf       bytes.Buffer
	limit     int
	total     int
	truncated bool
}

func (w *teeResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.total += n
	if room := w.limit - w.buf.Len(); room > 0 {
		if n <= room {
			w.buf.Write(p[:n])
		} else {
			w.buf.Write(p[:room])
			w.truncated = true
		}
	} else if n > 0 {
		w.truncated = true
	}
	return n, err
}

// Flush keeps http.Flusher working through the tee.
func (w *teeResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *teeResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

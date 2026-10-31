package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// extensionEventSettle keeps the newest seconds of the delivery log out of the
// feed. A delivery row is written with a created_at taken just before its
// INSERT commits; without the window a reader could move its cursor past a row
// that commits a moment later with an earlier created_at and never see it.
const extensionEventSettle = 5 * time.Second

const (
	extensionEventsDefaultLimit = 100
	extensionEventsMaxLimit     = 500
)

// ExtensionEventReplayer serves the event feed and redelivery of an extension
// connection (GET /ext/me/events, POST /ext/me/events/:delivery_id/redeliver).
// The webhook service implements it.
type ExtensionEventReplayer interface {
	ListExtensionEvents(ctx context.Context, p *ExtensionPrincipal, q ExtensionEventsQuery) (*ExtensionEventPage, error)
	RedeliverExtensionEvent(ctx context.Context, p *ExtensionPrincipal, deliveryID uuid.UUID) (*ExtensionRedelivery, error)
}

// ExtensionEventsQuery is one page request of the event feed.
type ExtensionEventsQuery struct {
	After domain.ExtensionEventCursor
	Limit int
	// Undelivered keeps only events that no delivery or redelivery got a 2xx for.
	Undelivered bool
}

// ExtensionEvent is one event of the feed. DeliveryID is the X-Mesh-Delivery the
// receiver got (and gets again on every redelivery).
type ExtensionEvent struct {
	DeliveryID     uuid.UUID       `json:"delivery_id"`
	SubscriptionID uuid.UUID       `json:"subscription_id"`
	Event          string          `json:"event"`
	Payload        json.RawMessage `json:"payload"`
	CreatedAt      time.Time       `json:"created_at"`
	Delivered      bool            `json:"delivered"`
	Attempts       int             `json:"attempts"`
	ResponseStatus *int            `json:"response_status,omitempty"`
}

// ExtensionEventPage is one page of the feed. NextCursor continues after the
// last row read, including rows left out because the connection may no longer
// see them. HasMore is true exactly when a readable event follows the page.
type ExtensionEventPage struct {
	Items      []ExtensionEvent `json:"items"`
	NextCursor string           `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

// ExtensionRedelivery is the outcome of one resend.
type ExtensionRedelivery struct {
	ID             uuid.UUID `json:"id"`
	DeliveryID     uuid.UUID `json:"delivery_id"`
	Event          string    `json:"event"`
	Success        bool      `json:"success"`
	ResponseStatus *int      `json:"response_status,omitempty"`
	Error          string    `json:"error,omitempty"`
}

// EncodeExtensionEventCursor renders a cursor as the opaque next_cursor string.
func EncodeExtensionEventCursor(c domain.ExtensionEventCursor) string {
	raw := strconv.FormatInt(c.CreatedAt.UnixNano(), 10) + "." + c.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// ParseExtensionEventSince reads the since parameter of the feed: empty (the
// start of the log), a next_cursor from an earlier page, or an RFC 3339 time
// (events created at or after it).
func ParseExtensionEventSince(since string) (domain.ExtensionEventCursor, error) {
	since = strings.TrimSpace(since)
	if since == "" {
		return domain.ExtensionEventCursor{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, since); err == nil {
		// uuid.Nil sorts before every id, so rows at exactly t are included.
		return domain.ExtensionEventCursor{CreatedAt: t}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(since)
	if err != nil {
		return domain.ExtensionEventCursor{}, errors.New("since is neither a cursor nor an RFC 3339 time")
	}
	nanos, id, ok := strings.Cut(string(raw), ".")
	if !ok {
		return domain.ExtensionEventCursor{}, errors.New("since is not a valid cursor")
	}
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return domain.ExtensionEventCursor{}, errors.New("since is not a valid cursor")
	}
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.ExtensionEventCursor{}, errors.New("since is not a valid cursor")
	}
	return domain.ExtensionEventCursor{CreatedAt: time.Unix(0, n).UTC(), ID: uid}, nil
}

func (s *webhookService) deliveryLog() (repository.ExtensionDeliveryLog, error) {
	dl, ok := s.repo.(repository.ExtensionDeliveryLog)
	if !ok || s.ext == nil {
		return nil, apierror.ServiceUnavailable("the extension event log is not available")
	}
	return dl, nil
}

// ListExtensionEvents returns the connection's events after q.After. Every row
// is checked against what the connection may see now, not when it was sent:
// the event must still be consented in capabilities.events and its project
// must pass the project scope.
func (s *webhookService) ListExtensionEvents(ctx context.Context, p *ExtensionPrincipal, q ExtensionEventsQuery) (*ExtensionEventPage, error) {
	dl, err := s.deliveryLog()
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = extensionEventsDefaultLimit
	}
	if limit > extensionEventsMaxLimit {
		limit = extensionEventsMaxLimit
	}
	// has_more must mean an event the caller can actually read follows the
	// page, so hidden rows are skipped (and the cursor moved past them) until
	// either limit+1 readable events are found or the log is exhausted. Each
	// batch moves the cursor forward and is an indexed read of limit+1 rows;
	// the request context bounds a very long run of hidden rows.
	until := time.Now().Add(-extensionEventSettle)
	page := &ExtensionEventPage{Items: []ExtensionEvent{}}
	next := q.After
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, err := dl.ListConnectionDeliveries(ctx, p.ConnectionID, next, until, limit+1)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			r := rows[i]
			visible, err := s.extensionEventVisible(ctx, p, r.EventType, r.Payload)
			if err != nil {
				// Fail closed: the caller keeps its own cursor, skipping the
				// row would lose the event for good.
				return nil, err
			}
			if visible && (!q.Undelivered || !r.Delivered) {
				if len(page.Items) == limit {
					// A readable event follows the page; the cursor stops before it.
					page.HasMore = true
					page.NextCursor = EncodeExtensionEventCursor(next)
					return page, nil
				}
				page.Items = append(page.Items, ExtensionEvent{
					DeliveryID: r.ID, SubscriptionID: *r.SubscriptionID, Event: r.EventType,
					Payload: json.RawMessage(r.Payload), CreatedAt: r.CreatedAt,
					Delivered: r.Delivered, Attempts: r.Attempt, ResponseStatus: r.ResponseStatus,
				})
			}
			next = domain.ExtensionEventCursor{CreatedAt: r.CreatedAt, ID: r.ID}
		}
		if len(rows) <= limit {
			break
		}
	}
	page.NextCursor = EncodeExtensionEventCursor(next)
	return page, nil
}

// RedeliverExtensionEvent sends one event again, once, to the subscription's
// current target with its current secret, and records the attempt as a new row
// pointing at the original. The request carries the original X-Mesh-Delivery,
// so a receiver that dedups on it does the work once. Only the connection that
// owns the subscription can resend, and only what it may still see.
func (s *webhookService) RedeliverExtensionEvent(ctx context.Context, p *ExtensionPrincipal, deliveryID uuid.UUID) (*ExtensionRedelivery, error) {
	dl, err := s.deliveryLog()
	if err != nil {
		return nil, err
	}
	d, err := dl.GetDelivery(ctx, deliveryID)
	if err != nil {
		return nil, err
	}
	if d == nil || d.SubscriptionID == nil {
		return nil, apierror.NotFound("Event delivery")
	}
	sub, err := s.ext.subs.GetByID(ctx, *d.SubscriptionID)
	if err != nil || sub == nil || sub.ConnectionID != p.ConnectionID {
		// Another connection's delivery is reported exactly like a missing one.
		return nil, apierror.NotFound("Event delivery")
	}
	if sub.Delivery != domain.ExtensionDeliveryWebhook {
		return nil, apierror.Unprocessable("only webhook subscriptions can be redelivered", string(sub.Delivery))
	}
	visible, err := s.extensionEventVisible(ctx, p, d.EventType, d.Payload)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, apierror.Forbidden("the connection may no longer see this event")
	}

	original := d.ID
	if d.RedeliveryOf != nil {
		original = *d.RedeliveryOf
	}
	// The resend carries the event's own id, the value the first delivery sent
	// in X-Mesh-Event-Id, so a receiver dedups on it: the recorded id, else the
	// payload's event_id; only a row that predates both falls back to the
	// delivery id.
	eventID := original
	if d.EventID != nil && *d.EventID != uuid.Nil {
		eventID = *d.EventID
	} else if id, ok := payloadUUIDField(d.Payload, "event_id"); ok {
		eventID = id
	}
	out := &ExtensionRedelivery{ID: uuid.New(), DeliveryID: original, Event: d.EventType}
	row := &domain.WebhookDelivery{
		ID: out.ID, SubscriptionID: &sub.ID, EventType: d.EventType, Payload: d.Payload,
		Attempt: 1, RedeliveryOf: &original, EventID: &eventID,
	}
	secret, reason := s.ext.resolveSecret(ctx, p.WorkspaceID, sub.SecretRef)
	if secret == "" {
		out.Error = reason
		row.ResponseBody = &reason
	} else {
		// The resend uses the extension's negotiated scheme: a v2 extension
		// must never be sent a v1 signature.
		status, body, duration, sendErr := s.sendSignedWith(s.ext.schemeFor(p.ExtensionID), sub.Target, secret, d.EventType, eventID, original, d.Payload)
		row.DurationMs = &duration
		if sendErr != nil {
			msg := sendErr.Error()
			out.Error = msg
			row.ResponseBody = &msg
		} else {
			row.ResponseStatus = &status
			row.ResponseBody = &body
			row.Success = status >= 200 && status < 300
			out.ResponseStatus = &status
		}
	}
	out.Success = row.Success
	row.CreatedAt = time.Now()
	if err := s.repo.CreateDelivery(context.WithoutCancel(ctx), row); err != nil {
		// The receiver may already have it; the caller must still learn that
		// the log does not show this attempt. A retry carries the same
		// X-Mesh-Delivery, so a deduping receiver does the work once.
		log.Printf("[webhook] failed to record redelivery %s of %s: %v", row.ID, original, err)
		return nil, apierror.InternalError("the redelivery was attempted but could not be recorded")
	}
	return out, nil
}

// extensionEventVisible reports whether the connection may see an event now:
// the event is still among its consented capabilities.events, and the event's
// project (payload project_id, when there is one) passes the project scope.
// With a scope configured, a payload or project_id that cannot be read is
// hidden: only an event that names no project skips the scope, and a
// project-bound connection never sees such an event (as in delivery).
func (s *webhookService) extensionEventVisible(ctx context.Context, p *ExtensionPrincipal, event string, payload []byte) (bool, error) {
	if !slices.Contains(p.Capabilities["events"], event) {
		return false, nil
	}
	if s.extScope == nil {
		return !p.ProjectBound, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil || body == nil {
		return false, nil
	}
	raw, named := body["project_id"]
	if !named || string(raw) == "null" {
		return !p.ProjectBound, nil
	}
	var projectID uuid.UUID
	if err := json.Unmarshal(raw, &projectID); err != nil {
		return false, nil
	}
	return s.extScope.AllowsProject(ctx, p.ConnectionID, projectID)
}

// payloadUUIDField reads a UUID field of a JSON object payload.
func payloadUUIDField(payload []byte, field string) (uuid.UUID, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return uuid.Nil, false
	}
	var id uuid.UUID
	if err := json.Unmarshal(m[field], &id); err != nil || id == uuid.Nil {
		return uuid.Nil, false
	}
	return id, true
}

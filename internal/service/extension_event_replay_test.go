package service

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// next_cursor comes back to the same position, to the nanosecond: a cursor
// rounded to microseconds would re-list or skip rows written in the same
// microsecond.
func TestExtensionEventCursor_RoundTrip(t *testing.T) {
	c := domain.ExtensionEventCursor{CreatedAt: time.Date(2026, 10, 29, 18, 0, 0, 123456789, time.UTC), ID: uuid.New()}
	got, err := ParseExtensionEventSince(EncodeExtensionEventCursor(c))
	require.NoError(t, err)
	assert.True(t, c.CreatedAt.Equal(got.CreatedAt))
	assert.Equal(t, c.ID, got.ID)
}

func TestParseExtensionEventSince(t *testing.T) {
	start, err := ParseExtensionEventSince("")
	require.NoError(t, err)
	assert.Equal(t, domain.ExtensionEventCursor{}, start)

	at, err := ParseExtensionEventSince("2026-10-29T18:00:00Z")
	require.NoError(t, err)
	assert.True(t, at.CreatedAt.Equal(time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)))
	assert.Equal(t, uuid.Nil, at.ID, "a time includes rows created exactly at it")

	for _, bad := range []string{"yesterday", "bm90LWEtY3Vyc29y", "MTIzLm5vdC1hLXV1aWQ"} {
		_, err := ParseExtensionEventSince(bad)
		assert.Error(t, err, bad)
	}
}

type allowAll struct {
	repository.ExtensionProjectScopeRepository
}

func (allowAll) AllowsProject(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}

func TestWithExtensionProjectScope(t *testing.T) {
	svc := NewWebhookService(&recordingWebhookRepo{}, WithExtensionProjectScope(allowAll{})).(*webhookService)
	assert.Equal(t, allowAll{}, svc.extScope)
}

// Without a delivery log (a repository that does not keep one, or extensions
// not wired) both routes answer 503 instead of an empty feed that looks like
// "nothing was missed".
func TestExtensionEvents_NoDeliveryLog(t *testing.T) {
	svc := &webhookService{repo: &recordingWebhookRepo{}}
	p := &ExtensionPrincipal{ConnectionID: uuid.New()}
	_, err := svc.ListExtensionEvents(context.Background(), p, ExtensionEventsQuery{})
	assert.ErrorContains(t, err, "not available")
	_, err = svc.RedeliverExtensionEvent(context.Background(), p, uuid.New())
	assert.ErrorContains(t, err, "not available")
}

func TestParseExtensionEventSince_BadNanos(t *testing.T) {
	raw := base64.RawURLEncoding.EncodeToString([]byte("x." + uuid.NewString()))
	_, err := ParseExtensionEventSince(raw)
	assert.Error(t, err)
}

type denyAll struct {
	repository.ExtensionProjectScopeRepository
}

func (denyAll) AllowsProject(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

// With a project scope configured, only an event that names no project skips
// it. A payload that is not JSON, or a project_id that is not a UUID, cannot be
// checked and is hidden rather than shown.
func TestExtensionEventVisible_ProjectScopeFailsClosed(t *testing.T) {
	p := &ExtensionPrincipal{ConnectionID: uuid.New(), Capabilities: map[string][]string{"events": {"task.updated"}}}
	svc := &webhookService{extScope: denyAll{}}
	for name, tc := range map[string]struct {
		payload string
		want    bool
	}{
		"no project_id":         {`{"task_id":"t"}`, true},
		"null project_id":       {`{"project_id":null}`, true},
		"denied project":        {`{"project_id":"` + uuid.NewString() + `"}`, false},
		"project_id not a uuid": {`{"project_id":"not-a-uuid"}`, false},
		"project_id a number":   {`{"project_id":42}`, false},
		"payload not json":      {`not json`, false},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := svc.extensionEventVisible(context.Background(), p, "task.updated", []byte(tc.payload))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

type replayLogRepo struct {
	*recordingWebhookRepo
	delivery  *domain.WebhookDelivery
	createErr error
	rows      []domain.ExtensionEventRecord
}

func (r *replayLogRepo) ListConnectionDeliveries(_ context.Context, _ uuid.UUID, after domain.ExtensionEventCursor, _ time.Time, limit int) ([]domain.ExtensionEventRecord, error) {
	rows := r.rows
	for i := range rows {
		if rows[i].ID == after.ID {
			rows = rows[i+1:]
			break
		}
	}
	return rows[:min(limit, len(rows))], nil
}

// has_more tells whether a row exists after the page, not whether the page is
// full: a page holding exactly the last rows must not send the reader after an
// empty one.
func TestListExtensionEvents_HasMoreOnlyWhenARowFollows(t *testing.T) {
	sub := uuid.New()
	p := &ExtensionPrincipal{ConnectionID: uuid.New(), Capabilities: map[string][]string{"events": {"task.updated"}}}
	rows := make([]domain.ExtensionEventRecord, 3)
	for i := range rows {
		rows[i].ID, rows[i].SubscriptionID, rows[i].EventType = uuid.New(), &sub, "task.updated"
		rows[i].Payload = []byte(`{}`)
		rows[i].CreatedAt = time.Now().Add(-time.Duration(10-i) * time.Minute)
	}
	svc := &webhookService{
		repo: &replayLogRepo{recordingWebhookRepo: &recordingWebhookRepo{}, rows: rows},
		ext:  &extensionDelivery{},
	}

	exact, err := svc.ListExtensionEvents(context.Background(), p, ExtensionEventsQuery{Limit: 3})
	require.NoError(t, err)
	assert.Len(t, exact.Items, 3)
	assert.False(t, exact.HasMore)

	short, err := svc.ListExtensionEvents(context.Background(), p, ExtensionEventsQuery{Limit: 2})
	require.NoError(t, err)
	require.Len(t, short.Items, 2)
	assert.True(t, short.HasMore)
	after, err := ParseExtensionEventSince(short.NextCursor)
	require.NoError(t, err)
	assert.Equal(t, rows[1].ID, after.ID, "the cursor stops at the last row of the page, not at the probe row")
}

// A hidden row after the page must not promise a continuation: has_more is
// true only when a readable event follows, and the cursor moves past hidden
// rows on the way.
func TestListExtensionEvents_HasMoreIgnoresHiddenRows(t *testing.T) {
	sub := uuid.New()
	p := &ExtensionPrincipal{ConnectionID: uuid.New(), Capabilities: map[string][]string{"events": {"task.updated"}}}
	types := []string{"task.updated", "comment.created", "task.updated", "comment.created"}
	rows := make([]domain.ExtensionEventRecord, len(types))
	for i := range rows {
		rows[i].ID, rows[i].SubscriptionID, rows[i].EventType = uuid.New(), &sub, types[i]
		rows[i].Payload = []byte(`{}`)
		rows[i].CreatedAt = time.Now().Add(-time.Duration(10-i) * time.Minute)
	}
	svc := &webhookService{
		repo: &replayLogRepo{recordingWebhookRepo: &recordingWebhookRepo{}, rows: rows},
		ext:  &extensionDelivery{},
	}

	first, err := svc.ListExtensionEvents(context.Background(), p, ExtensionEventsQuery{Limit: 1})
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	assert.True(t, first.HasMore, "rows[2] is readable and follows the hidden rows[1]")
	cur, err := ParseExtensionEventSince(first.NextCursor)
	require.NoError(t, err)
	assert.Equal(t, rows[1].ID, cur.ID, "the cursor moves past the hidden row")

	last, err := svc.ListExtensionEvents(context.Background(), p, ExtensionEventsQuery{After: cur, Limit: 1})
	require.NoError(t, err)
	require.Len(t, last.Items, 1)
	assert.Equal(t, rows[2].ID, last.Items[0].DeliveryID)
	assert.False(t, last.HasMore, "only a hidden row remains")
}

// A tail of already delivered rows is hidden when only undelivered ones are
// asked for, so it must not raise has_more either.
func TestListExtensionEvents_HasMoreIgnoresDeliveredTailWhenUndelivered(t *testing.T) {
	sub := uuid.New()
	p := &ExtensionPrincipal{ConnectionID: uuid.New(), Capabilities: map[string][]string{"events": {"task.updated"}}}
	rows := make([]domain.ExtensionEventRecord, 3)
	for i := range rows {
		rows[i].ID, rows[i].SubscriptionID, rows[i].EventType = uuid.New(), &sub, "task.updated"
		rows[i].Payload = []byte(`{}`)
		rows[i].CreatedAt = time.Now().Add(-time.Duration(10-i) * time.Minute)
		rows[i].Delivered = i > 0
	}
	svc := &webhookService{
		repo: &replayLogRepo{recordingWebhookRepo: &recordingWebhookRepo{}, rows: rows},
		ext:  &extensionDelivery{},
	}

	page, err := svc.ListExtensionEvents(context.Background(), p, ExtensionEventsQuery{Limit: 1, Undelivered: true})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.False(t, page.HasMore)

	all, err := svc.ListExtensionEvents(context.Background(), p, ExtensionEventsQuery{Limit: 1})
	require.NoError(t, err)
	assert.True(t, all.HasMore, "positive control: the delivered tail is readable without the filter")
}

// The tail is hidden by project scope and far longer than one batch: has_more
// stays false, and a readable event behind the same run flips it to true.
func TestListExtensionEvents_HasMoreAcrossLongHiddenRun(t *testing.T) {
	sub := uuid.New()
	p := &ExtensionPrincipal{ConnectionID: uuid.New(), Capabilities: map[string][]string{"events": {"task.updated"}}}
	denied := []byte(`{"project_id":"` + uuid.NewString() + `"}`)
	mk := func(hidden int, visibleAfter bool) []domain.ExtensionEventRecord {
		rows := make([]domain.ExtensionEventRecord, 2+hidden)
		for i := range rows {
			rows[i].ID, rows[i].SubscriptionID, rows[i].EventType = uuid.New(), &sub, "task.updated"
			rows[i].Payload = denied
			rows[i].CreatedAt = time.Now().Add(-time.Duration(1000-i) * time.Minute)
		}
		rows[0].Payload = []byte(`{}`)
		if visibleAfter {
			rows[len(rows)-1].Payload = []byte(`{}`)
		}
		return rows
	}
	list := func(rows []domain.ExtensionEventRecord) *ExtensionEventPage {
		svc := &webhookService{
			repo:     &replayLogRepo{recordingWebhookRepo: &recordingWebhookRepo{}, rows: rows},
			ext:      &extensionDelivery{},
			extScope: denyAll{},
		}
		page, err := svc.ListExtensionEvents(context.Background(), p, ExtensionEventsQuery{Limit: 1})
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		return page
	}

	assert.False(t, list(mk(45, false)).HasMore, "only denied-project rows follow")
	assert.True(t, list(mk(45, true)).HasMore, "positive control: a readable event ends the same run")
}

// A project-bound connection never sees an event without a project, with or
// without a scope: delivery does not send it one either.
func TestExtensionEventVisible_ProjectBoundHidesProjectlessEvents(t *testing.T) {
	events := map[string][]string{"events": {"task.updated"}}
	bound := &ExtensionPrincipal{ConnectionID: uuid.New(), Capabilities: events, ProjectBound: true}
	open := &ExtensionPrincipal{ConnectionID: uuid.New(), Capabilities: events}
	for name, svc := range map[string]*webhookService{"scope": {extScope: allowAll{}}, "no scope": {}} {
		for _, payload := range []string{`{"task_id":"t"}`, `{"project_id":null}`} {
			ok, err := svc.extensionEventVisible(context.Background(), bound, "task.updated", []byte(payload))
			require.NoError(t, err)
			assert.False(t, ok, "%s bound %s", name, payload)
			ok, err = svc.extensionEventVisible(context.Background(), open, "task.updated", []byte(payload))
			require.NoError(t, err)
			assert.True(t, ok, "%s workspace-scoped %s (positive control)", name, payload)
		}
	}
}

// The resend carries the event_id the first delivery sent; anything that is not
// a UUID field falls back to the caller's default.
func TestPayloadUUIDField(t *testing.T) {
	id := uuid.New()
	got, ok := payloadUUIDField([]byte(`{"event_id":"`+id.String()+`","x":1}`), "event_id")
	assert.True(t, ok)
	assert.Equal(t, id, got)
	for name, payload := range map[string]string{
		"missing": `{"x":1}`, "null": `{"event_id":null}`, "not a uuid": `{"event_id":"nope"}`,
		"number": `{"event_id":7}`, "not json": `nope`, "nil uuid": `{"event_id":"` + uuid.Nil.String() + `"}`,
	} {
		_, ok := payloadUUIDField([]byte(payload), "event_id")
		assert.False(t, ok, name)
	}
}

func (r *replayLogRepo) GetDelivery(context.Context, uuid.UUID) (*domain.WebhookDelivery, error) {
	return r.delivery, nil
}

func (r *replayLogRepo) CreateDelivery(context.Context, *domain.WebhookDelivery) error {
	return r.createErr
}

type replaySubs struct {
	fakeExtSubs
	sub *domain.ExtensionEventSubscription
}

func (r *replaySubs) GetByID(context.Context, uuid.UUID) (*domain.ExtensionEventSubscription, error) {
	return r.sub, nil
}

// A resend whose attempt cannot be recorded is reported as an error, not as a
// plain success: otherwise the log silently loses an attempt the receiver saw.
func TestRedeliverExtensionEvent_UnrecordedAttemptIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	p := &ExtensionPrincipal{ConnectionID: uuid.New(), WorkspaceID: uuid.New(), Capabilities: map[string][]string{"events": {"task.updated"}}}
	sub := &domain.ExtensionEventSubscription{
		ID: uuid.New(), ConnectionID: p.ConnectionID, Event: "task.updated",
		Delivery: domain.ExtensionDeliveryWebhook, Target: srv.URL, SecretRef: "s",
	}
	d := &domain.WebhookDelivery{ID: uuid.New(), SubscriptionID: &sub.ID, EventType: "task.updated", Payload: []byte(`{}`)}
	newSvc := func(createErr error) *webhookService {
		return &webhookService{
			repo:   &replayLogRepo{recordingWebhookRepo: &recordingWebhookRepo{}, delivery: d, createErr: createErr},
			client: newWebhookHTTPClient(func(net.IP) bool { return true }),
			ext: &extensionDelivery{
				subs:    &replaySubs{sub: sub},
				secrets: &fakeExtSecrets{values: []domain.MaterializedSecret{{Name: "s", Value: "v"}}},
			},
		}
	}

	// Positive control: recorded, so a plain success.
	res, err := newSvc(nil).RedeliverExtensionEvent(context.Background(), p, d.ID)
	require.NoError(t, err)
	assert.True(t, res.Success)

	_, err = newSvc(errors.New("db down")).RedeliverExtensionEvent(context.Background(), p, d.ID)
	assert.ErrorContains(t, err, "could not be recorded")
}

// The resend keeps the event id of the first send: the recorded one even when
// the payload cannot carry it (not a JSON object), else the payload's event_id,
// and only a row that predates both uses the delivery id.
func TestRedeliverExtensionEvent_KeepsTheEventID(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Mesh-Event-Id")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p := &ExtensionPrincipal{ConnectionID: uuid.New(), WorkspaceID: uuid.New(), Capabilities: map[string][]string{"events": {"task.updated"}}}
	sub := &domain.ExtensionEventSubscription{
		ID: uuid.New(), ConnectionID: p.ConnectionID, Event: "task.updated",
		Delivery: domain.ExtensionDeliveryWebhook, Target: srv.URL, SecretRef: "s",
	}
	recorded, inPayload := uuid.New(), uuid.New()
	for name, tc := range map[string]struct {
		payload string
		event   *uuid.UUID
		want    func(deliveryID uuid.UUID) uuid.UUID
	}{
		"recorded id, payload cannot carry it": {`[1,2]`, &recorded, func(uuid.UUID) uuid.UUID { return recorded }},
		"recorded id wins":                     {`{"event_id":"` + inPayload.String() + `"}`, &recorded, func(uuid.UUID) uuid.UUID { return recorded }},
		"payload event_id":                     {`{"event_id":"` + inPayload.String() + `"}`, nil, func(uuid.UUID) uuid.UUID { return inPayload }},
		"row from before event ids":            {`{}`, nil, func(id uuid.UUID) uuid.UUID { return id }},
	} {
		d := &domain.WebhookDelivery{ID: uuid.New(), SubscriptionID: &sub.ID, EventType: "task.updated", Payload: []byte(tc.payload), EventID: tc.event}
		svc := &webhookService{
			repo:   &replayLogRepo{recordingWebhookRepo: &recordingWebhookRepo{}, delivery: d},
			client: newWebhookHTTPClient(func(net.IP) bool { return true }),
			ext: &extensionDelivery{
				subs:    &replaySubs{sub: sub},
				secrets: &fakeExtSecrets{values: []domain.MaterializedSecret{{Name: "s", Value: "v"}}},
			},
		}
		gotHeader = ""
		res, err := svc.RedeliverExtensionEvent(context.Background(), p, d.ID)
		require.NoError(t, err, name)
		assert.True(t, res.Success, name)
		assert.Equal(t, tc.want(d.ID).String(), gotHeader, name)
	}
}

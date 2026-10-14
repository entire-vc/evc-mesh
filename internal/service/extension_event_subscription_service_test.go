package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Plain in-memory stand-ins for the three tables the service reads. The same
// rules run against real Postgres in extension_event_subscription_db_test.go;
// these keep them in the untagged suite, which has no database in CI.
type memSubs struct {
	rows map[uuid.UUID]domain.ExtensionEventSubscription
	err  error
}

func (m *memSubs) Create(_ context.Context, s *domain.ExtensionEventSubscription) (*domain.ExtensionEventSubscription, error) {
	if m.err != nil {
		return nil, m.err
	}
	for _, r := range m.rows {
		if r.ConnectionID == s.ConnectionID && r.Event == s.Event && r.Delivery == s.Delivery && r.Target == s.Target {
			return &r, nil
		}
	}
	m.rows[s.ID] = *s
	return s, nil
}

func (m *memSubs) GetByID(_ context.Context, id uuid.UUID) (*domain.ExtensionEventSubscription, error) {
	if m.err != nil {
		return nil, m.err
	}
	if r, ok := m.rows[id]; ok {
		return &r, nil
	}
	return nil, nil
}

func (m *memSubs) ListByConnection(_ context.Context, c uuid.UUID) ([]domain.ExtensionEventSubscription, error) {
	var out []domain.ExtensionEventSubscription
	for _, r := range m.rows {
		if r.ConnectionID == c {
			out = append(out, r)
		}
	}
	return out, m.err
}

func (m *memSubs) ListByEvent(context.Context, string) ([]domain.ExtensionEventSubscription, error) {
	return nil, nil
}

func (m *memSubs) Delete(_ context.Context, id uuid.UUID) error { delete(m.rows, id); return nil }

// Embedding the interface leaves every method this service never calls nil.
type memConn struct {
	repository.IntegrationRepository
	row *domain.IntegrationConfig
	err error
}

func (m memConn) GetByID(context.Context, uuid.UUID) (*domain.IntegrationConfig, error) {
	return m.row, m.err
}

type memInstalled struct {
	repository.InstalledExtensionRepository
	row *domain.InstalledExtension
	err error
}

func (m memInstalled) Get(context.Context, string) (*domain.InstalledExtension, error) {
	return m.row, m.err
}

func newExtEventSvc(events []string) (ExtensionEventSubscriptionService, *memSubs, uuid.UUID) {
	manifest := `{"id":"ext","version":"1","runtime":"builtin","capabilities":{"events":["` + joinQuoted(events) + `"]}}`
	conn := &domain.IntegrationConfig{ID: uuid.New(), Provider: "ext"}
	subs := &memSubs{rows: map[uuid.UUID]domain.ExtensionEventSubscription{}}
	svc := NewExtensionEventSubscriptionService(subs, memConn{row: conn},
		memInstalled{row: &domain.InstalledExtension{ExtensionID: "ext", Manifest: []byte(manifest)}})
	return svc, subs, conn.ID
}

func joinQuoted(l []string) string {
	out := ""
	for i, e := range l {
		if i > 0 {
			out += `","`
		}
		out += e
	}
	return out
}

func apiStatus(t *testing.T, err error) int {
	t.Helper()
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	return apiErr.StatusCode()
}

func webhookSub(conn uuid.UUID, event string) CreateExtensionEventSubscriptionInput {
	return CreateExtensionEventSubscriptionInput{
		ConnectionID: conn, Event: event, Delivery: domain.ExtensionDeliveryWebhook,
		Target: "https://ext.example.com/hook", SecretRef: "ext-signing",
	}
}

func TestExtEventSvc_StatusCodes(t *testing.T) {
	ctx := context.Background()
	svc, _, conn := newExtEventSvc([]string{extensions.EventTaskUpdated})

	_, err := svc.Subscribe(ctx, webhookSub(conn, "task.exploded"))
	assert.Equal(t, 400, apiStatus(t, err), "event outside the public list")
	_, err = svc.Subscribe(ctx, webhookSub(conn, extensions.EventCommentCreated))
	assert.Equal(t, 403, apiStatus(t, err), "public event the manifest does not declare")

	first, err := svc.Subscribe(ctx, webhookSub(conn, extensions.EventTaskUpdated))
	require.NoError(t, err)
	again, err := svc.Subscribe(ctx, webhookSub(conn, extensions.EventTaskUpdated))
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)

	list, err := svc.ListByConnection(ctx, conn)
	require.NoError(t, err)
	assert.Len(t, list, 1)
	require.NoError(t, svc.Unsubscribe(ctx, first.ID))
	assert.Equal(t, 404, apiStatus(t, svc.Unsubscribe(ctx, first.ID)))
}

func TestExtEventSvc_DeliveryValidation(t *testing.T) {
	ctx := context.Background()
	svc, _, conn := newExtEventSvc([]string{extensions.EventTaskUpdated})

	for name, mutate := range map[string]func(*CreateExtensionEventSubscriptionInput){
		"webhook without secret_ref": func(in *CreateExtensionEventSubscriptionInput) { in.SecretRef = "" },
		"webhook with non-url":       func(in *CreateExtensionEventSubscriptionInput) { in.Target = "not a url" },
		"webhook with ftp":           func(in *CreateExtensionEventSubscriptionInput) { in.Target = "ftp://x.example/h" },
		"unknown delivery":           func(in *CreateExtensionEventSubscriptionInput) { in.Delivery = "carrier-pigeon" },
		"nats with a target":         func(in *CreateExtensionEventSubscriptionInput) { in.Delivery = domain.ExtensionDeliveryNATS },
	} {
		in := webhookSub(conn, extensions.EventTaskUpdated)
		mutate(&in)
		_, err := svc.Subscribe(ctx, in)
		assert.Equal(t, 400, apiStatus(t, err), name)
	}

	nats := CreateExtensionEventSubscriptionInput{ConnectionID: conn, Event: extensions.EventTaskUpdated, Delivery: domain.ExtensionDeliveryNATS}
	_, err := svc.Subscribe(ctx, nats)
	assert.NoError(t, err)
}

func TestExtEventSvc_MissingParentsAndStoreErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("db down")
	in := webhookSub(uuid.New(), extensions.EventTaskUpdated)
	subs := &memSubs{rows: map[uuid.UUID]domain.ExtensionEventSubscription{}}
	conn := &domain.IntegrationConfig{ID: uuid.New(), Provider: "ext"}
	ext := &domain.InstalledExtension{ExtensionID: "ext", Manifest: []byte(`{"id":"ext","version":"1","runtime":"builtin"}`)}

	_, err := NewExtensionEventSubscriptionService(subs, memConn{}, memInstalled{row: ext}).Subscribe(ctx, in)
	assert.Equal(t, 404, apiStatus(t, err), "unknown connection")
	_, err = NewExtensionEventSubscriptionService(subs, memConn{row: conn}, memInstalled{}).Subscribe(ctx, in)
	assert.Equal(t, 404, apiStatus(t, err), "extension not installed")
	_, err = NewExtensionEventSubscriptionService(subs, memConn{err: boom}, memInstalled{row: ext}).Subscribe(ctx, in)
	assert.ErrorIs(t, err, boom)
	_, err = NewExtensionEventSubscriptionService(subs, memConn{row: conn}, memInstalled{err: boom}).Subscribe(ctx, in)
	assert.ErrorIs(t, err, boom)

	bad := &domain.InstalledExtension{ExtensionID: "ext", Manifest: []byte(`{`)}
	_, err = NewExtensionEventSubscriptionService(subs, memConn{row: conn}, memInstalled{row: bad}).Subscribe(ctx, in)
	assert.Error(t, err, "an unreadable manifest is refused, not treated as declaring everything")

	failing := &memSubs{rows: map[uuid.UUID]domain.ExtensionEventSubscription{}, err: boom}
	assert.ErrorIs(t, NewExtensionEventSubscriptionService(failing, memConn{}, memInstalled{}).Unsubscribe(ctx, uuid.New()), boom)
}

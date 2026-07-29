package service

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// These tests exercise the browser-push fan-out against a real database and a
// real Web Push send, because the thing being asserted is that a particular HTTP
// request is never made. A fake that reports "no send" tells you what the fake
// was written to say; an endpoint that records zero requests tells you the
// payload did not leave the process.
//
// No //go:build integration tag, deliberately — the same reasoning as
// userRepoTestDB in the postgres package. CI's "Test" and "Go coverage" jobs run
// an untagged `go test ./...` against a migrated DATABASE_URL, and anything
// behind the integration tag is not run by either. Skips rather than fails when
// no database is reachable, so a plain local `go test ./...` still works.
func pushMembershipTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("Postgres at %s not accepting connections, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// pushFixture is one workspace with three people in the world: the owner, a
// member, and somebody who is in neither list.
type pushFixture struct {
	wsID     uuid.UUID
	member   uuid.UUID
	outsider uuid.UUID
}

func newPushFixture(t *testing.T, db *sqlx.DB) pushFixture {
	t.Helper()

	owner := createPushTestUser(t, db, "owner")
	member := createPushTestUser(t, db, "member")
	outsider := createPushTestUser(t, db, "outsider")

	wsID := uuid.New()
	_, err := db.Exec(
		`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1, $2, $3, $4)`,
		wsID, "Push Membership Test", "push-ws-"+wsID.String()[:8], owner,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, wsID) })

	_, err = db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1, $2, $3, 'member')`,
		uuid.New(), wsID, member,
	)
	require.NoError(t, err)

	return pushFixture{wsID: wsID, member: member, outsider: outsider}
}

func createPushTestUser(t *testing.T, db *sqlx.DB, label string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	handle := label + "-" + id.String()[:8]
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, display_name, username, is_active)
		 VALUES ($1, $2, 'not-a-real-hash', $3, $4, true)`,
		id, handle+"@example.test", label, handle,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, id) })
	return id
}

// insertPreferenceDirectly writes the subscription row with SQL rather than
// through PUT /notifications/preferences. That is the point of it: the handler
// now rejects a body naming a workspace the caller is not in, so a row created
// through the API could only ever belong to a member and the read side would
// never be asked the question. Rows like this one exist anyway — written before
// the route was guarded, or left behind by someone who has since been removed
// from the workspace — and the fan-out reads the table, not the audit trail.
func insertPreferenceDirectly(t *testing.T, db *sqlx.DB, wsID, userID uuid.UUID, channel string, events ...string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO notification_preferences (id, workspace_id, user_id, channel, events, is_enabled)
		 VALUES ($1, $2, $3, $4, $5, true)`,
		uuid.New(), wsID, userID, channel, pq.StringArray(events),
	)
	require.NoError(t, err)
}

func insertSubscription(t *testing.T, db *sqlx.DB, userID uuid.UUID, endpoint string) {
	t.Helper()
	p256dh, auth := newSubscriptionKeys(t)
	_, err := db.Exec(
		`INSERT INTO push_subscriptions (id, user_id, endpoint, p256dh_key, auth_key, user_agent)
		 VALUES ($1, $2, $3, $4, $5, 'TestBrowser/1.0')`,
		uuid.New(), userID, endpoint, p256dh, auth,
	)
	require.NoError(t, err)
}

// newSubscriptionKeys returns a genuine P-256 public key and auth secret, in the
// encoding a browser's PushSubscription uses. They have to be real: the payload
// is encrypted to this key before any request is made, so a placeholder would
// fail during encryption and produce the no-request-was-sent result these tests
// are looking for, for entirely the wrong reason.
func newSubscriptionKeys(t *testing.T) (p256dh, auth string) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(t, err)
	authSecret := make([]byte, 16)
	_, err = rand.Read(authSecret)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(authSecret)
}

// newDBPushService builds the production PushService over real repositories, so
// the membership question is answered by the real SQL and not by a stub that
// agrees with the test.
func newDBPushService(t *testing.T, db *sqlx.DB) PushService {
	t.Helper()
	vapidPrivate, vapidPublic, err := webpush.GenerateVAPIDKeys()
	require.NoError(t, err)
	return NewPushService(
		postgres.NewPushSubscriptionRepo(db),
		postgres.NewNotificationRepo(db),
		vapidPublic, vapidPrivate, "mailto:test@example.test",
	)
}

func commentPayload() domain.PushPayload {
	return domain.PushPayload{
		Title:     "New comment",
		Body:      "the contents of a comment in a workspace you are not in",
		EventType: "comment.created",
	}
}

// TestPushService_SendToUser_StrangerWithASubscriptionIsNotPushedTo is the one
// that matters. A preference row plus a registered device is everything the push
// path used to require, and neither of them is evidence of membership: both are
// written by the recipient about themselves.
//
// The assertion is on the endpoint, not on the return value. SendToUser reports
// nil in both the "sent" and "declined" cases, so the only honest way to tell
// them apart is whether the push service received a request.
func TestPushService_SendToUser_StrangerWithASubscriptionIsNotPushedTo(t *testing.T) {
	db := pushMembershipTestDB(t)
	fx := newPushFixture(t, db)

	var delivered int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&delivered, 1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer endpoint.Close()

	insertPreferenceDirectly(t, db, fx.wsID, fx.outsider, "browser_push", "comment.created")
	insertSubscription(t, db, fx.outsider, endpoint.URL+"/stranger-device")

	svc := newDBPushService(t, db)
	require.NoError(t, svc.SendToUser(context.Background(), fx.outsider, fx.wsID, commentPayload()))

	assert.Equal(t, int64(0), atomic.LoadInt64(&delivered),
		"a comment body was pushed to the browser of somebody who is not a member of the workspace")
}

// TestPushService_SendToUser_MemberStillReceivesThePush is the other half, and
// the reason the check is a membership lookup rather than an allowlist: a filter
// that delivers nothing also passes the test above.
func TestPushService_SendToUser_MemberStillReceivesThePush(t *testing.T) {
	db := pushMembershipTestDB(t)
	fx := newPushFixture(t, db)

	var delivered int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&delivered, 1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer endpoint.Close()

	insertPreferenceDirectly(t, db, fx.wsID, fx.member, "browser_push", "comment.created")
	insertSubscription(t, db, fx.member, endpoint.URL+"/member-device")

	svc := newDBPushService(t, db)
	require.NoError(t, svc.SendToUser(context.Background(), fx.member, fx.wsID, commentPayload()))

	assert.Equal(t, int64(1), atomic.LoadInt64(&delivered),
		"a member of the workspace stopped receiving their own push notifications")
}

// TestPushService_SendToUser_RemovedMemberStopsReceivingPushes: the preference
// row and the device both outlive the membership, which is what makes this
// different from the stranger case — nothing about the subscriber's own records
// changes when they are removed, so only a membership lookup at send time
// notices.
func TestPushService_SendToUser_RemovedMemberStopsReceivingPushes(t *testing.T) {
	db := pushMembershipTestDB(t)
	fx := newPushFixture(t, db)

	var delivered int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&delivered, 1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer endpoint.Close()

	insertPreferenceDirectly(t, db, fx.wsID, fx.member, "browser_push", "comment.created")
	insertSubscription(t, db, fx.member, endpoint.URL+"/ex-member-device")

	svc := newDBPushService(t, db)
	require.NoError(t, svc.SendToUser(context.Background(), fx.member, fx.wsID, commentPayload()))
	require.Equal(t, int64(1), atomic.LoadInt64(&delivered), "precondition: the member should be reachable first")

	_, err := db.Exec(`DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, fx.wsID, fx.member)
	require.NoError(t, err)

	require.NoError(t, svc.SendToUser(context.Background(), fx.member, fx.wsID, commentPayload()))

	assert.Equal(t, int64(1), atomic.LoadInt64(&delivered),
		"a removed member kept receiving the workspace's comment bodies on their device")
}

// --- fail-closed, without a database ---------------------------------------

// TestPushService_SendToUser_NonMemberIsRefusedWithoutTouchingSubscriptions
// covers the same rule as the database test above and runs everywhere, so the
// check is still guarded when no Postgres is reachable and the DB tests skip.
func TestPushService_SendToUser_NonMemberIsRefusedWithoutTouchingSubscriptions(t *testing.T) {
	repo := newMockPushSubRepo()
	stranger := uuid.New()
	repo.subs["stranger-ep"] = &domain.PushSubscription{
		ID: uuid.New(), UserID: stranger, Endpoint: "stranger-ep", P256DHKey: "k", AuthKey: "a",
	}

	prefs := &mockNotifPrefsGetter{
		prefs:      []domain.NotificationPreference{prefFor(stranger, "comment.created")},
		nonMembers: map[uuid.UUID]bool{stranger: true},
	}
	svc := NewPushService(repo, prefs, "pub", "priv", "mailto:test@test.com")

	require.NoError(t, svc.SendToUser(context.Background(), stranger, uuid.New(),
		domain.PushPayload{EventType: "comment.created"}))

	assert.Zero(t, repo.calls.listUser,
		"a non-member's devices were looked up, so the send was decided after the payload had a destination")
}

// TestPushService_SendToUser_UnanswerableMembershipSendsNothing pins the
// direction of the failure. dispatch fails closed on the same error for the same
// reason: a membership question that could not be answered is not permission.
func TestPushService_SendToUser_UnanswerableMembershipSendsNothing(t *testing.T) {
	repo := newMockPushSubRepo()
	userID := uuid.New()
	repo.subs["ep"] = &domain.PushSubscription{
		ID: uuid.New(), UserID: userID, Endpoint: "ep", P256DHKey: "k", AuthKey: "a",
	}

	prefs := &mockNotifPrefsGetter{
		prefs:         []domain.NotificationPreference{prefFor(userID, "comment.created")},
		membershipErr: errors.New("connection reset"),
	}
	svc := NewPushService(repo, prefs, "pub", "priv", "mailto:test@test.com")

	require.NoError(t, svc.SendToUser(context.Background(), userID, uuid.New(),
		domain.PushPayload{EventType: "comment.created"}))

	assert.Zero(t, repo.calls.listUser,
		"a failed membership lookup was treated as permission to send")
}

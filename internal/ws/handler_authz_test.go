package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
)

const testJWTSecret = "unit-test-secret-32-chars-minimum!!"

// mintToken produces an access token the real auth.Service will accept, so the
// handshake tests exercise the same validation path production does.
func mintToken(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	claims := auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
			Issuer:    "evc-mesh",
			ID:        uuid.New().String(),
		},
		Email: "unit@test.local",
		Name:  "Unit Test",
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testJWTSecret)) // nosemgrep: go.jwt-go.security.jwt.hardcoded-jwt-key -- testJWTSecret (defined above) is a literal test-only fixture, never a real credential; it signs and verifies exclusively within this test file
	require.NoError(t, err)
	return signed
}

// handshakeAuthorizer admits exactly one (workspace slug, user) pair.
type handshakeAuthorizer struct {
	slug   string
	wsID   uuid.UUID
	member uuid.UUID
}

func (a *handshakeAuthorizer) WorkspaceIDBySlug(_ context.Context, slug string) (uuid.UUID, error) {
	if slug == a.slug {
		return a.wsID, nil
	}
	return uuid.Nil, ErrWorkspaceNotFound
}

func (a *handshakeAuthorizer) UserIsWorkspaceMember(_ context.Context, wsID, userID uuid.UUID) bool {
	return wsID == a.wsID && userID == a.member
}

func (a *handshakeAuthorizer) ProjectWorkspaceID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, ErrWorkspaceNotFound
}

// callHandler runs the /ws handler against a plain recorder. Every case here is
// refused before the upgrade, so the recorder never has to be hijackable.
func callHandler(t *testing.T, query string, authz Authorizer) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ws?"+query, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	authService := auth.NewService(nil, nil, nil, nil, testJWTSecret)
	require.NoError(t, Handler(NewHub(nil), authService, nil, authz)(c))
	return rec
}

// TestHandler_HandshakeRefusesNonMembers is the regression test for the finding
// itself: /ws is registered on the root Echo instance, so no middleware guard can
// run on it, and it used to subscribe the client to whatever workspace= said.
//
// "not a member" and "no such workspace" must be the same answer: slugs are
// ws-<8 hex> and public in every /w/<slug>/... URL, so a different answer would
// let anyone enumerate the tenants on the instance.
func TestHandler_HandshakeRefusesNonMembers(t *testing.T) {
	authz := &handshakeAuthorizer{slug: "ws-11111111", wsID: uuid.New(), member: uuid.New()}
	stranger := uuid.New()
	token := mintToken(t, stranger)

	existing := callHandler(t, "token="+token+"&workspace=ws-11111111", authz)
	missing := callHandler(t, "token="+token+"&workspace=ws-99999999", authz)

	assert.Equal(t, http.StatusForbidden, existing.Code)
	assert.Equal(t, existing.Code, missing.Code,
		"an existing workspace and a non-existent one must answer alike")
	assert.Equal(t, existing.Body.String(), missing.Body.String(),
		"the refusal body must not say which workspaces exist")
}

// TestHandler_NoAuthorizerFailsClosed: a miswired server must refuse workspace
// subscriptions rather than serve them unguarded — the failure mode that put this
// endpoint in the state it was in.
func TestHandler_NoAuthorizerFailsClosed(t *testing.T) {
	rec := callHandler(t, "token="+mintToken(t, uuid.New())+"&workspace=ws-11111111", nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandler_RejectsMissingAndInvalidCredentials(t *testing.T) {
	authz := &handshakeAuthorizer{slug: "ws-11111111", wsID: uuid.New(), member: uuid.New()}

	assert.Equal(t, http.StatusUnauthorized, callHandler(t, "workspace=ws-11111111", authz).Code)
	assert.Equal(t, http.StatusUnauthorized, callHandler(t, "token=not-a-jwt", authz).Code)
	// A well-formed token whose subject is not a UUID.
	badSubject, err := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "not-a-uuid",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
			Issuer:    "evc-mesh",
		},
	}).SignedString([]byte(testJWTSecret)) // nosemgrep: go.jwt-go.security.jwt.hardcoded-jwt-key -- same test-only fixture secret as above, not a real credential
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, callHandler(t, "token="+badSubject, authz).Code)
}

// TestHandler_MemberIsAdmitted proves the refusals above are not simply "the
// handler always says no": the member of the named workspace gets past the
// membership check and on to the upgrade.
func TestHandler_MemberIsAdmitted(t *testing.T) {
	member := uuid.New()
	authz := &handshakeAuthorizer{slug: "ws-11111111", wsID: uuid.New(), member: member}

	rec := callHandler(t, "token="+mintToken(t, member)+"&workspace=ws-11111111", authz)
	// websocket.Accept then fails on the non-hijackable recorder, which is as far
	// as this test can go — but it is past the guard, which is the point.
	assert.NotEqual(t, http.StatusForbidden, rec.Code)
}

// TestReadPump_RefusesUnauthorisedSubscribe drives the real read loop over a real
// socket: the in-band subscribe is the second door into the leak, and a client
// that asks for a channel it is not entitled to must be told no and must not end
// up subscribed.
func TestReadPump_RefusesUnauthorisedSubscribe(t *testing.T) {
	ownWorkspace := uuid.New()
	me := uuid.New()

	clientCh := make(chan *Client, 1)
	hub := NewHub(nil)
	// Drain hub registrations so ReadPump's unregister send cannot block.
	go func() {
		for {
			select {
			case <-hub.register:
			case <-hub.unregister:
			}
		}
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		client := NewClient(conn, hub, Principal{
			UserID:        me,
			WorkspaceID:   ownWorkspace,
			WorkspaceSlug: "ws-mine",
		}, &handshakeAuthorizer{})
		clientCh <- client
		client.ReadPump(r.Context())
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil) //nolint:bodyclose // handshake response body is not used
	require.NoError(t, err)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	client := <-clientCh

	for _, channel := range []string{"ws:ws-somebody-else", "project:" + uuid.New().String(), "nonsense"} {
		payload, mErr := json.Marshal(IncomingMessage{Action: "subscribe", Channel: channel})
		require.NoError(t, mErr)
		require.NoError(t, conn.Write(ctx, websocket.MessageText, payload))

		select {
		case msg := <-client.Send:
			assert.Contains(t, string(msg), "subscribe.denied", "channel %q was not refused", channel)
		case <-time.After(3 * time.Second):
			t.Fatalf("no answer to a subscribe for %q", channel)
		}

		client.mu.RLock()
		subscribed := client.Subscriptions[channel]
		client.mu.RUnlock()
		assert.False(t, subscribed, "client ended up subscribed to %q", channel)
	}

	// The connection's own workspace is still allowed, and unsubscribe still works.
	allowed, err := json.Marshal(IncomingMessage{Action: "subscribe", Channel: "ws:ws-mine"})
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, websocket.MessageText, allowed))
	require.Eventually(t, func() bool {
		client.mu.RLock()
		defer client.mu.RUnlock()
		return client.Subscriptions["ws:ws-mine"]
	}, 3*time.Second, 20*time.Millisecond, "the client's own workspace channel was refused")

	drop, err := json.Marshal(IncomingMessage{Action: "unsubscribe", Channel: "ws:ws-mine"})
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, websocket.MessageText, drop))
	require.Eventually(t, func() bool {
		client.mu.RLock()
		defer client.mu.RUnlock()
		return !client.Subscriptions["ws:ws-mine"]
	}, 3*time.Second, 20*time.Millisecond)

	// Garbage and unknown actions are ignored rather than fatal.
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte("{not json")))
	unknown, err := json.Marshal(IncomingMessage{Action: "explode", Channel: "ws:ws-mine"})
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, websocket.MessageText, unknown))
}

package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A workspace's Slack webhook_url is admin-controlled and POSTed to fresh on
// every task-event notification (slack_service.go's SendMessage), same class
// of blind-SSRF exposure as a workspace webhook's url (webhook_service.go)
// or an agent's callback_url (agent_callback_url.go). These tests mirror
// webhook_delivery_ssrf_test.go's TestWebhookDelivery_RefusesNonPublicAddress
// and TestWebhookDelivery_RedirectNotFollowed for the Slack delivery path.

// TestSlackDelivery_RefusesNonPublicAddress proves the production client
// (isPubliclyRoutable) never reaches a loopback receiver, using an
// open-predicate client against the same server as a positive control — a
// refusal below is the guard, not a broken fixture.
func TestSlackDelivery_RefusesNonPublicAddress(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	open := &slackService{client: newSlackHTTPClient(func(net.IP) bool { return true })}
	err := open.SendMessage(context.Background(), srv.URL, SlackMessage{Text: "hi"})
	require.NoError(t, err)
	require.EqualValues(t, 1, hits.Load())

	hits.Store(0)
	prod := &slackService{client: newSlackHTTPClient(isPubliclyRoutable)}
	err = prod.SendMessage(context.Background(), srv.URL, SlackMessage{Text: "hi"})
	require.Error(t, err)
	assert.ErrorIs(t, err, errDialAddressRefused)
	assert.Zero(t, hits.Load(), "the request must not reach a loopback receiver")
}

// TestSlackDelivery_RefusesHostnameResolvingToLoopback mirrors the workspace
// webhook DNS-rebinding test: a hostname (localhost stands in for a name that
// resolved to something public when the integration was configured and to
// loopback at delivery time) is stopped at dial — only the dial-time check
// can catch this, not write-time validation.
func TestSlackDelivery_RefusesHostnameResolvingToLoopback(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)

	prod := &slackService{client: newSlackHTTPClient(isPubliclyRoutable)}
	err = prod.SendMessage(context.Background(), "http://localhost:"+port+"/hook", SlackMessage{Text: "hi"})
	require.Error(t, err)
	assert.ErrorIs(t, err, errDialAddressRefused)
	assert.Zero(t, hits.Load())
}

// TestSlackDelivery_RedirectNotFollowed proves a receiver that answers 307 to
// an internal address never gets the client sent there.
func TestSlackDelivery_RedirectNotFollowed(t *testing.T) {
	var innerHits atomic.Int32
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		innerHits.Add(1)
	}))
	defer inner.Close()
	var outerHits atomic.Int32
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outerHits.Add(1)
		http.Redirect(w, r, inner.URL, http.StatusTemporaryRedirect)
	}))
	defer outer.Close()

	// Both receivers sit on loopback, so admit loopback: what is under test is
	// redirect handling, not the address check.
	s := &slackService{client: newSlackHTTPClient(func(net.IP) bool { return true })}

	err := s.SendMessage(context.Background(), outer.URL, SlackMessage{Text: "hi"})
	require.Error(t, err)
	assert.ErrorIs(t, err, errSlackRedirectRefused)
	require.EqualValues(t, 1, outerHits.Load())
	assert.Zero(t, innerHits.Load(), "the redirect target must never be requested")
}

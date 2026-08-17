package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The failure these tests are about is not a bug in this codebase: an api
// container with no outbound route to api.telegram.org is a deployment
// problem. It earns tests anyway because of how it presented — "context
// deadline exceeded", on a channel whose settings page said everything was
// fine — which sent the investigation everywhere except the network. What is
// under test is whether the software says what is wrong.

// blackholeClient is a Telegram client whose requests never reach anything,
// which is what a blocked egress looks like from inside the process.
type blackholeClient struct{ err error }

func (c *blackholeClient) GetMe(context.Context, string) (string, error) { return "", c.err }
func (c *blackholeClient) SendMessage(context.Context, string, int64, string) error {
	return c.err
}
func (c *blackholeClient) GetUpdates(context.Context, string, int64, int) ([]TelegramUpdate, error) {
	return nil, c.err
}

func unreachableErr() error {
	return &TelegramUnreachableError{
		Host:   TelegramAPIBaseURL,
		Method: "getMe",
		Err:    errors.New("context deadline exceeded"),
	}
}

func reachabilitySvc(client TelegramClient) *notificationService {
	integrations := &fakeTelegramIntegrationLookup{cfg: telegramIntegration(true, "bot-token")}
	return NewNotificationService(&fakeNotificationRepo{}, WithTelegramService(client, integrations, nil, nil)).(*notificationService)
}

// --- the error message --------------------------------------------------------

// TestTelegramUnreachableError_SaysWhatToCheck: the whole point of the type.
// "context deadline exceeded" is true and useless; this message has to name
// the host and both remedies, because the person reading it has already ruled
// out the token and the bot by the time they get here.
func TestTelegramUnreachableError_SaysWhatToCheck(t *testing.T) {
	msg := unreachableErr().Error()

	assert.Contains(t, msg, "api.telegram.org", "the unreachable host is not named")
	assert.Contains(t, msg, "443", "outbound HTTPS is not mentioned as the thing to allow")
	assert.Contains(t, msg, "HTTPS_PROXY", "the proxy alternative is not mentioned")
	assert.Contains(t, msg, "curl", "no way to verify the diagnosis from the host")
	assert.Contains(t, msg, "context deadline exceeded", "the underlying error was swallowed")
}

// TestTelegramUnreachableError_UnwrapsToTheTransportFailure keeps errors.Is/As
// working for callers that want the cause rather than the advice.
func TestTelegramUnreachableError_UnwrapsToTheTransportFailure(t *testing.T) {
	cause := errors.New("dial tcp: i/o timeout")
	err := error(&TelegramUnreachableError{Host: TelegramAPIBaseURL, Method: "sendMessage", Err: cause})

	assert.ErrorIs(t, err, cause)

	var unreachable *TelegramUnreachableError
	require.True(t, errors.As(err, &unreachable))
	assert.Equal(t, "sendMessage", unreachable.Method)
}

// TestTelegramClient_TransportFailureIsUnreachableNotAPIError: a connection
// that never produced an HTTP response must not be reported as though Telegram
// answered. The two have completely different remedies.
func TestTelegramClient_TransportFailureIsUnreachableNotAPIError(t *testing.T) {
	// A server that is closed before use is the cheapest reliable
	// connection-refused in a test.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := srv.URL
	srv.Close()

	err := NewTelegramClientWithBaseURL(baseURL).SendMessage(context.Background(), "secret-token", 42, "hi")
	require.Error(t, err)

	var unreachable *TelegramUnreachableError
	assert.True(t, errors.As(err, &unreachable), "a refused connection was not reported as unreachable: %v", err)

	var apiErr *TelegramAPIError
	assert.False(t, errors.As(err, &apiErr), "a refused connection was reported as a Telegram API error")
}

// TestTelegramClient_TransportFailureDoesNotLogTheBotToken: http.Client
// failures are *url.Error, whose message embeds the whole request URL — and
// the Bot API carries the token in the path. Every connection failure was
// printing a live credential into the log.
func TestTelegramClient_TransportFailureDoesNotLogTheBotToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := srv.URL
	srv.Close()

	const token = "123456:AAHsuperSecretBotTokenValue"
	err := NewTelegramClientWithBaseURL(baseURL).SendMessage(context.Background(), token, 42, "hi")
	require.Error(t, err)

	assert.NotContains(t, err.Error(), token, "the bot token is in the error, and therefore in the log")
	assert.Contains(t, err.Error(), "<bot-token-redacted>")
}

// TestRedactToken_CoversBothFormsTheURLCanCarry: the token appears in the
// error as it appears in the request URL, and the client path-escapes it
// before building that URL. A redaction that only knew the raw form would keep
// leaking any token whose escaped form differs.
func TestRedactToken_CoversBothFormsTheURLCanCarry(t *testing.T) {
	const token = "123456:AA needs/escaping"

	raw := redactToken(errors.New(`Post "https://api.telegram.org/bot`+token+`/sendMessage": timeout`), token)
	assert.NotContains(t, raw.Error(), token)

	escaped := redactToken(
		errors.New(`Post "https://api.telegram.org/bot`+url.PathEscape(token)+`/sendMessage": timeout`), token)
	assert.NotContains(t, escaped.Error(), url.PathEscape(token),
		"the percent-encoded token, which is the form actually sent, survived redaction")
}

// TestRedactToken_PassesThroughWhenThereIsNothingToRedact keeps the helper from
// inventing an error where there was none, and preserves the original when no
// token is in play.
func TestRedactToken_PassesThroughWhenThereIsNothingToRedact(t *testing.T) {
	assert.NoError(t, redactToken(nil, "tok"))

	orig := errors.New("some failure")
	assert.Equal(t, orig, redactToken(orig, ""))
}

// --- the probe ----------------------------------------------------------------

// TestTelegramReachable_UnreachableHostExplainsEgress is the settings page's
// half: the browser gets the same explanation the log does, because the person
// clicking around in the UI is not the person tailing the server log.
func TestTelegramReachable_UnreachableHostExplainsEgress(t *testing.T) {
	ok, reason := reachabilitySvc(&blackholeClient{err: unreachableErr()}).
		TelegramReachable(context.Background(), uuid.New())

	assert.False(t, ok)
	assert.Contains(t, reason, "api.telegram.org")
	assert.Contains(t, reason, "HTTPS_PROXY")
	assert.NotContains(t, strings.ToLower(reason), "context deadline",
		"the raw transport error leaked into user-facing copy")
}

// TestTelegramReachable_RejectedTokenIsADifferentAnswer: Telegram answering
// "no" is a configuration problem with a different fix than not being able to
// ask it at all, and conflating the two is what made this hard to diagnose in
// the first place.
func TestTelegramReachable_RejectedTokenIsADifferentAnswer(t *testing.T) {
	apiErr := &TelegramAPIError{StatusCode: 401, ErrorCode: 401, Description: "Unauthorized"}
	ok, reason := reachabilitySvc(&blackholeClient{err: apiErr}).
		TelegramReachable(context.Background(), uuid.New())

	assert.False(t, ok)
	assert.Contains(t, reason, "token")
	assert.NotContains(t, reason, "HTTPS_PROXY", "a rejected token was blamed on the network")
}

// TestTelegramReachable_HealthyBotReportsNoReason: a working channel hands the
// settings page nothing to display, so the banner is absent rather than empty.
func TestTelegramReachable_HealthyBotReportsNoReason(t *testing.T) {
	ok, reason := reachabilitySvc(&fakeTelegramClient{username: "mesh_bot"}).
		TelegramReachable(context.Background(), uuid.New())

	assert.True(t, ok)
	assert.Empty(t, reason)
}

// TestTelegramReachable_NoBotConfiguredDoesNotClaimANetworkProblem: with no
// integration row there is nothing to reach, and saying "check your firewall"
// would send someone after a problem they do not have.
func TestTelegramReachable_NoBotConfiguredDoesNotClaimANetworkProblem(t *testing.T) {
	svc := NewNotificationService(&fakeNotificationRepo{},
		WithTelegramService(&fakeTelegramClient{}, &fakeTelegramIntegrationLookup{}, nil, nil)).(*notificationService)

	ok, reason := svc.TelegramReachable(context.Background(), uuid.New())

	assert.False(t, ok)
	assert.NotContains(t, reason, "HTTPS_PROXY")
	assert.Contains(t, reason, "No active Telegram bot")
}

// TestTelegramReachable_ChannelNotWiredUp: an instance built without the
// Telegram deps answers immediately rather than pretending to probe.
func TestTelegramReachable_ChannelNotWiredUp(t *testing.T) {
	ok, reason := NewNotificationService(&fakeNotificationRepo{}).(*notificationService).
		TelegramReachable(context.Background(), uuid.New())

	assert.False(t, ok)
	assert.Contains(t, reason, "not enabled on this instance")
}

// TestTelegramReachable_ProbeIsBounded: the probe runs while somebody waits on
// a settings page, and the failure it exists to detect is a timeout — so it
// must carry its own deadline rather than inherit an open-ended request
// context.
func TestTelegramReachable_ProbeIsBounded(t *testing.T) {
	var gotDeadline bool
	client := &deadlineRecordingClient{seen: &gotDeadline}

	ok, _ := reachabilitySvc(client).TelegramReachable(context.Background(), uuid.New())

	assert.True(t, ok)
	assert.True(t, gotDeadline, "the probe inherited a context with no deadline")
	assert.LessOrEqual(t, telegramProbeTimeout.Seconds(), 5.0,
		"the probe can block a page load for longer than a person will wait")
}

type deadlineRecordingClient struct{ seen *bool }

func (c *deadlineRecordingClient) GetMe(ctx context.Context, _ string) (string, error) {
	_, ok := ctx.Deadline()
	*c.seen = ok
	return "mesh_bot", nil
}
func (c *deadlineRecordingClient) SendMessage(context.Context, string, int64, string) error {
	return nil
}
func (c *deadlineRecordingClient) GetUpdates(context.Context, string, int64, int) ([]TelegramUpdate, error) {
	return nil, nil
}

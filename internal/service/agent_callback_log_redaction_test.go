package service

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A webhook address commonly carries its secret in the query
// (?token=...) or in the path (Slack-style /T000/B000/xxxx). The delivery
// loop logs on every attempt, so whatever it prints ends up in mesh-api's
// logs for as long as those are kept.
const (
	logSecretQuery = "QUERYSECRET-9f2c"
	logSecretPath  = "PATHSECRET-71ab"
)

func assertNoSecretLogged(t *testing.T, logged string) {
	t.Helper()
	assert.NotContains(t, logged, logSecretQuery, "callback query must not be logged")
	assert.NotContains(t, logged, logSecretPath, "callback path must not be logged")
	assert.NotContains(t, logged, "token=", "callback query must not be logged")
}

// Positive control for the redaction tests: the same capture does see log
// lines and they do carry the agent id. Without it "no secret in the log"
// could just mean nothing was captured.
func TestAgentCallback_Log_PositiveControl(t *testing.T) {
	logged := captureLog(t)
	srv, _ := countingServer(t, nil)
	s := &agentNotifyService{client: newAgentCallbackHTTPClient(allowAnyIP)}
	agentID := uuid.New()

	s.deliverWithRetry(srv.URL, agentID, "task.assigned", "d1", []byte(`{}`))

	assert.Contains(t, logged(), "callback delivered for agent "+agentID.String())
}

// Success path.
func TestAgentCallback_Log_DeliveredDoesNotLeakQueryOrPath(t *testing.T) {
	logged := captureLog(t)
	srv, hits := countingServer(t, nil)
	s := &agentNotifyService{client: newAgentCallbackHTTPClient(allowAnyIP)}

	s.deliverWithRetry(srv.URL+"/hook/"+logSecretPath+"?token="+logSecretQuery, uuid.New(), "task.assigned", "d1", []byte(`{}`))

	require.Equal(t, int32(1), hits.Load(), "delivery must actually have happened")
	assertNoSecretLogged(t, logged())
}

// Permanent refusal path: the error returned by http.Client.Do is a
// *url.Error whose text embeds the full request URL, so redacting only the
// explicit "url: %s" argument is not enough.
func TestAgentCallback_Log_RefusedDoesNotLeakQueryOrPath(t *testing.T) {
	logged := captureLog(t)
	srv, hits := countingServer(t, nil)
	s := &agentNotifyService{client: newAgentCallbackHTTPClient(isPubliclyRoutable)}

	s.deliverWithRetry(srv.URL+"/hook/"+logSecretPath+"?token="+logSecretQuery, uuid.New(), "task.assigned", "d1", []byte(`{}`))

	require.Equal(t, int32(0), hits.Load())
	require.Contains(t, logged(), "refused permanently", "the refusal line must have been logged")
	assertNoSecretLogged(t, logged())
}

// Retry path: a 5xx-free network failure that is not permanent. A closed
// server gives connection refused, which the loop retries; the first attempt
// is enough to observe the log line, so the backoff is shortened.
func TestAgentCallback_Log_FailedAttemptDoesNotLeakQueryOrPath(t *testing.T) {
	logged := captureLog(t)
	prev := callbackRetryBackoffs
	callbackRetryBackoffs = nil
	t.Cleanup(func() { callbackRetryBackoffs = prev })

	srv, _ := countingServer(t, nil)
	target := srv.URL + "/hook/" + logSecretPath + "?token=" + logSecretQuery
	srv.Close()
	s := &agentNotifyService{client: newAgentCallbackHTTPClient(allowAnyIP)}

	s.deliverWithRetry(target, uuid.New(), "task.assigned", "d1", []byte(`{}`))

	require.Contains(t, logged(), "callback POST failed", "the failure line must have been logged")
	assertNoSecretLogged(t, logged())
}

func TestRedactCallbackURL(t *testing.T) {
	cases := map[string]string{
		"https://hooks.example.com/a/b?token=x":   "https://hooks.example.com",
		"http://hooks.example.com:8080/cb#f":      "http://hooks.example.com:8080",
		"https://user:pw@hooks.example.com/cb":    "https://hooks.example.com",
		"https://[2606:2800:220:1::1]:443/cb?t=1": "https://[2606:2800:220:1::1]:443",
		"://broken?token=x":                       "<unparseable callback url>",
		"":                                        "<unparseable callback url>",
	}
	for in, want := range cases {
		got := redactCallbackURL(in)
		assert.Equal(t, want, got, "input %q", in)
		assert.False(t, strings.Contains(got, "token"), "input %q", in)
	}
}

// Sanity for the helper the log path relies on: an error from the HTTP
// client carries the URL, and the redacted form does not.
func TestRedactCallbackError_StripsURL(t *testing.T) {
	_, err := http.NewRequest(http.MethodPost, "http://a b/?token="+logSecretQuery, http.NoBody)
	require.Error(t, err)
	require.Contains(t, err.Error(), logSecretQuery, "premise: the raw error does carry the URL")

	assert.NotContains(t, redactCallbackError(err), logSecretQuery)
}

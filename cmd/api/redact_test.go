package main

import (
	"os"
	"strings"
	"testing"
)

// TestRedactInviteToken pins the access-log half of "the invite token must not
// be written to logs in full".
//
// The invite-creation path was the obvious leak, but the token also reached the
// log through the request logger every time the invitee opened their own link:
// `GET /api/v1/invites/<token> -> 200`. Same credential, same log, different
// code path.
func TestRedactInviteToken(t *testing.T) {
	const token = "f89e78c38f571976372925aa6a254ed67a1f78ddd060bb098a3840ce4670c5e1"

	cases := []struct {
		name string
		uri  string
		want string
	}{
		{
			name: "public invite lookup",
			uri:  "/api/v1/invites/" + token,
			want: "/api/v1/invites/<redacted>",
		},
		{
			name: "public invite accept keeps the operation visible",
			uri:  "/api/v1/invites/" + token + "/accept",
			want: "/api/v1/invites/<redacted>/accept",
		},
		{
			name: "query string is preserved",
			uri:  "/api/v1/invites/" + token + "?foo=bar",
			want: "/api/v1/invites/<redacted>?foo=bar",
		},
		{
			name: "admin route carries a row id, not a secret — left readable",
			uri:  "/api/v1/workspaces/cb0cb41a-86f6-48b8-8e8d-e72412fc61ee/invites/74c7fc08-d874-4249-8a58-174621e9d763",
			want: "/api/v1/workspaces/cb0cb41a-86f6-48b8-8e8d-e72412fc61ee/invites/74c7fc08-d874-4249-8a58-174621e9d763",
		},
		{
			name: "unrelated route untouched",
			uri:  "/api/v1/workspaces",
			want: "/api/v1/workspaces",
		},
		{
			name: "collection route has no token to redact",
			uri:  "/api/v1/invites/",
			want: "/api/v1/invites/",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactInviteToken(tc.uri)
			if got != tc.want {
				t.Fatalf("redactInviteToken(%q)\n got: %q\nwant: %q", tc.uri, got, tc.want)
			}
		})
	}
}

// TestRedactInviteToken_NeverEmitsTheToken is the property the log actually
// needs: whatever the shape of the public invite URI, the credential must not
// survive into the logged string.
func TestRedactInviteToken_NeverEmitsTheToken(t *testing.T) {
	const token = "8c01a2143db68256b17d5158e00b41f726450069cd4e1d27dc96af9f06a9d079"

	for _, uri := range []string{
		"/api/v1/invites/" + token,
		"/api/v1/invites/" + token + "/accept",
		"/api/v1/invites/" + token + "?x=1",
	} {
		if got := redactInviteToken(uri); strings.Contains(got, token) {
			t.Fatalf("token survived redaction of %q: %q", uri, got)
		}
	}
}

// TestRedactRequestURI pins the credential half of the request log: /ws takes
// an agent key or a JWT in the query string, and the access log printed that
// URI verbatim.
func TestRedactRequestURI(t *testing.T) {
	const key = "agk_ws-1234abcd_0123456789abcdef0123456789abcdef" // pragma: allowlist secret
	cases := []struct{ uri, want string }{
		{"/ws?agent_key=" + key, "/ws?agent_key=<redacted>"},
		{"/ws?token=eyJhbGciOi.payload.sig&workspace=ws-1234abcd", "/ws?token=<redacted>&workspace=ws-1234abcd"},
		{"/ws?workspace=ws-1234abcd&agent_key=" + key + "&x=1", "/ws?workspace=ws-1234abcd&agent_key=<redacted>&x=1"},
		{"/ws?agent%5Fkey=" + key, "/ws?agent%5Fkey=<redacted>"},
		{"/ws?AGENT_KEY=" + key, "/ws?AGENT_KEY=<redacted>"},
		{"/ws?agent_key=", "/ws?agent_key=<redacted>"},
		{"/ws?agent_key", "/ws?agent_key"},
		{"/api/v1/tasks?page=1&tokens=3", "/api/v1/tasks?page=1&tokens=3"},
		{"/api/v1/workspaces", "/api/v1/workspaces"},
		{"/api/v1/invites/abc123?token=" + key, "/api/v1/invites/<redacted>?token=<redacted>"},
	}
	for _, tc := range cases {
		if got := redactRequestURI(tc.uri); got != tc.want {
			t.Errorf("redactRequestURI(%q)\n got: %q\nwant: %q", tc.uri, got, tc.want)
		}
		if strings.Contains(redactRequestURI(tc.uri), key) {
			t.Errorf("key survived redaction of %q", tc.uri)
		}
	}
}

// TestRequestLogsUseRedactRequestURI: every logger that prints a request URI
// goes through redactRequestURI, not the invite-only helper.
func TestRequestLogsUseRedactRequestURI(t *testing.T) {
	src, err := os.ReadFile("wire_echo.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if strings.Contains(s, "redactInviteToken") {
		t.Fatal("wire_echo.go logs through redactInviteToken, which leaves agent_key/token in the URI")
	}
	for _, want := range []string{
		"accessLogLines(c.Request().Method, redactRequestURI(v.URI)",
		"mw.SlowBodyConfig{Redact: redactRequestURI}",
		"Redact: redactRequestURI}",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("wire_echo.go: %q not found", want)
		}
	}
}

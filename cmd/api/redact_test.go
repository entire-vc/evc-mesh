package main

import (
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

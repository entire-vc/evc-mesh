// Package oautherror is RFC 6749 §5.2 / §4.1.2.1 / RFC 7591 §3.2.2 error
// codes as a typed Go error, kept separate from pkg/apierror deliberately:
// apierror's {code,message,details,validation} JSON shape is this
// codebase's own convention, but /oauth/token, /oauth/authorize's error
// redirect, and /oauth/register are all spec-mandated to answer
// {"error": "<code>", "error_description": "..."} instead. Reusing apierror
// here would mean bending a fixed wire format to fit a shape it was never
// designed for.
package oautherror

import "net/http"

// Error is one RFC 6749/7591 OAuth error: a short machine-readable Code plus
// a human-readable Description, carrying the HTTP status it should be
// answered with (400 for almost everything, 401 for invalid_client).
type Error struct {
	Code        string
	Description string
	Status      int
}

func (e *Error) Error() string {
	if e.Description == "" {
		return e.Code
	}
	return e.Code + ": " + e.Description
}

// serverErrorPublicDescription is what a caller sees for every server_error,
// whatever Description the error was built with. A server_error's Description
// is written for us, not for the caller: it is typically err.Error() from the
// database or an outbound fetch, and these endpoints answer unauthenticated
// callers. Log Description server-side (see IsServerError); never send it.
const serverErrorPublicDescription = "internal server error"

// IsServerError reports whether e is a server_error, whose Description must be
// logged rather than returned.
func (e *Error) IsServerError() bool { return e.Code == "server_error" }

// Body returns the RFC-shaped JSON body: {"error": ..., "error_description": ...}.
// For a server_error the description is replaced with a fixed public text —
// see serverErrorPublicDescription.
func (e *Error) Body() map[string]string {
	m := map[string]string{"error": e.Code}
	desc := e.Description
	if e.IsServerError() {
		desc = serverErrorPublicDescription
	}
	if desc != "" {
		m["error_description"] = desc
	}
	return m
}

func new400(code, desc string) *Error {
	return &Error{Code: code, Description: desc, Status: http.StatusBadRequest}
}

// InvalidRequest: the request is missing a required parameter, includes an
// unsupported parameter value, repeats a parameter, or is otherwise
// malformed (RFC 6749 §5.2 / §4.1.2.1).
func InvalidRequest(desc string) *Error { return new400("invalid_request", desc) }

// InvalidClient: client authentication failed (unknown client_id, or a CIMD
// document that could not be fetched/validated). 401 per RFC 6749 §5.2.
func InvalidClient(desc string) *Error {
	return &Error{Code: "invalid_client", Description: desc, Status: http.StatusUnauthorized}
}

// InvalidGrant: the authorization code, refresh token, or PKCE verifier is
// invalid, expired, revoked, already used, or does not match the client/
// redirect_uri it was issued to. This is the code the acceptance criteria's
// "red controls" (§4 of the task) all expect.
func InvalidGrant(desc string) *Error { return new400("invalid_grant", desc) }

// UnauthorizedClient: the authenticated client is not authorized to use this
// grant type / response type.
func UnauthorizedClient(desc string) *Error { return new400("unauthorized_client", desc) }

// UnsupportedGrantType: /oauth/token was asked for a grant_type this AS does
// not implement.
func UnsupportedGrantType(desc string) *Error { return new400("unsupported_grant_type", desc) }

// UnsupportedResponseType: /oauth/authorize was asked for a response_type
// other than "code".
func UnsupportedResponseType(desc string) *Error {
	return new400("unsupported_response_type", desc)
}

// InvalidScope: the requested scope is invalid, unknown, or malformed.
func InvalidScope(desc string) *Error { return new400("invalid_scope", desc) }

// AccessDenied: the resource owner denied the consent request (RFC 6749
// §4.1.2.1) — the redirect-back error for a "Deny" click.
func AccessDenied(desc string) *Error { return new400("access_denied", desc) }

// InvalidClientMetadata: RFC 7591 §3.2.2 — the DCR/CIMD registration request
// includes a metadata field that is invalid or malformed (e.g. no
// redirect_uris, a non-https CIMD client_id).
func InvalidClientMetadata(desc string) *Error {
	return new400("invalid_client_metadata", desc)
}

// ServerError: an unexpected condition on our side. 500 — the one case this
// package's error is not the caller's fault.
func ServerError(desc string) *Error {
	return &Error{Code: "server_error", Description: desc, Status: http.StatusInternalServerError}
}

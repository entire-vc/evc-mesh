package service

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// An agent's callback_url is an address the server POSTs every task event to
// (agent_notify_service). Whoever holds the agent's key sets it, so without a
// guard a leaked key is a blind SSRF into our own network — cloud metadata,
// the Postgres/Redis containers, anything on loopback — plus a standing copy
// of the agent's events to an address of the attacker's choosing.
//
// Two layers, because neither is enough alone:
//   - write time (ValidateAgentCallbackURL): refuse what is visibly wrong —
//     wrong scheme, credentials in the URL, a literal internal IP, localhost.
//     It does NOT resolve DNS: a name's answer can change after we check it.
//   - delivery time (newAgentCallbackHTTPClient): dial only publicly routable
//     addresses, checked on the resolved IP right before connect (closes DNS
//     rebinding), and never follow a redirect (a 30x to an internal address
//     would otherwise walk straight past the dial check's intent).

const (
	agentCallbackURLMaxLen      = 2048
	agentCallbackDialTimeout    = 5 * time.Second
	agentCallbackRequestTimeout = 10 * time.Second
)

// errCallbackRedirectRefused is returned by the delivery client's
// CheckRedirect. Delivery treats it as permanent, same as a refused dial.
var errCallbackRedirectRefused = errors.New("callback redirects are not followed")

// ValidateAgentCallbackURL checks a callback_url before it is stored. The
// empty string is valid and means "no callback".
func ValidateAgentCallbackURL(raw string) error {
	if raw == "" {
		return nil
	}
	fail := func(msg string) error {
		return apierror.ValidationError(map[string]string{"callback_url": msg})
	}
	if strings.TrimSpace(raw) != raw {
		return fail("callback_url must not have leading or trailing whitespace")
	}
	if len(raw) > agentCallbackURLMaxLen {
		return fail("callback_url is too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fail("callback_url is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fail("callback_url must use http or https")
	}
	if u.User != nil {
		return fail("callback_url must not contain credentials")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return fail("callback_url must not contain a fragment")
	}
	host := u.Hostname()
	if host == "" {
		return fail("callback_url must have a host")
	}
	// An IPv6 zone-id ("fe80::1%25eth0" in the URL, "fe80::1%eth0" here) is
	// not understood by net.ParseIP, so the literal would fall through to the
	// DNS-name branch below and be stored. No public callback needs a zone; a
	// zone means a link-local or otherwise on-host address. Refuse any '%'.
	if strings.Contains(host, "%") {
		return fail("callback_url host must not contain an IPv6 zone identifier")
	}
	lh := strings.ToLower(strings.TrimSuffix(host, "."))
	if ip := net.ParseIP(lh); ip != nil {
		if !isPubliclyRoutable(ip) {
			return fail("callback_url must not point to a private or internal address")
		}
		return nil
	}
	// 2130706433, 0x7f000001, 017700000001, 127.1: not canonical IPs, so
	// ParseIP says no, yet a libc resolver reads each one as 127.0.0.1. No
	// real DNS name ends in a numeric label, so refuse the whole shape.
	if isNumericHostLabel(lh[strings.LastIndex(lh, ".")+1:]) {
		return fail("callback_url host must be a DNS name or a canonical IP address")
	}
	if lh == "localhost" || strings.HasSuffix(lh, ".localhost") {
		return fail("callback_url must not point to a private or internal address")
	}
	return nil
}

// isNumericHostLabel reports whether a host label is a decimal, octal or
// 0x-hex number.
func isNumericHostLabel(label string) bool {
	if l := strings.TrimPrefix(strings.TrimPrefix(label, "0x"), "0X"); l != label {
		if l == "" {
			return true
		}
		for _, c := range l {
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return false
			}
		}
		return true
	}
	if label == "" {
		return false
	}
	for _, c := range label {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// newAgentCallbackHTTPClient builds the client used for callback delivery.
// allow decides which resolved IPs may be dialled; production passes
// isPubliclyRoutable, tests pass a predicate that admits their loopback
// httptest server so redirect handling can be exercised.
//
// Proxy is deliberately left nil: an HTTP(S)_PROXY from the environment would
// make the dial check see the proxy's address, not the callback's.
func newAgentCallbackHTTPClient(allow func(net.IP) bool) *http.Client {
	return &http.Client{
		Timeout: agentCallbackRequestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errCallbackRedirectRefused
		},
		Transport: &http.Transport{
			DialContext:         guardedDialContext(agentCallbackDialTimeout, allow),
			TLSHandshakeTimeout: agentCallbackDialTimeout,
		},
	}
}

// isPermanentCallbackError reports whether a delivery error is our own guard
// refusing the request. Retrying cannot change that outcome, and the retry
// schedule is 10s+60s+300s of sleeping per event.
// A name that does not exist (NXDOMAIN) is treated the same way: it will not
// start existing within the retry window, and each retry holds a goroutine
// for up to six minutes. Temporary resolver failures still retry.
func isPermanentCallbackError(err error) bool {
	if errors.Is(err, errDialAddressRefused) || errors.Is(err, errCallbackRedirectRefused) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// redactCallbackURL renders a callback address for logs: scheme and host
// only. Webhook addresses routinely carry their secret in the query
// (?token=...) or in the path (Slack-style /T000/B000/xxxx), and delivery
// logs on every attempt, so neither may reach the log. Credentials and
// fragments are dropped too. An address that does not parse is not echoed.
func redactCallbackURL(raw string) string {
	const unparseable = "<unparseable callback url>"
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return unparseable
	}
	return u.Scheme + "://" + u.Host
}

// redactCallbackError renders an error from the delivery client for logs.
// http.Client.Do wraps its failures in *url.Error, whose text embeds the full
// request URL — query included — so logging the error verbatim leaks exactly
// what redactCallbackURL hides. Only the underlying cause is kept.
func redactCallbackError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}

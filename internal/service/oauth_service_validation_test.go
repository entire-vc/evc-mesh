package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Pure-function tests for the OAuth AS's validation helpers and SSRF guard.
// No database: these are the checks that decide what an attacker-supplied
// string is allowed to become (a redirect target, a dial address, a PKCE
// proof), so each case pins an accept/reject decision, not just a line.

func TestIsPubliclyRoutable_DeniesEverySSRFTargetClass(t *testing.T) {
	denied := map[string]string{
		"127.0.0.1":         "IPv4 loopback",
		"127.255.255.254":   "IPv4 loopback, top of /8",
		"::1":               "IPv6 loopback",
		"10.0.0.1":          "RFC1918 10/8",
		"172.16.0.1":        "RFC1918 172.16/12",
		"192.168.1.1":       "RFC1918 192.168/16",
		"fc00::1":           "IPv6 ULA",
		"169.254.169.254":   "cloud metadata (link-local)",
		"fe80::1":           "IPv6 link-local",
		"224.0.0.1":         "IPv4 multicast",
		"ff02::1":           "IPv6 multicast",
		"0.0.0.0":           "unspecified v4",
		"::":                "unspecified v6",
		"100.64.0.1":        "CGNAT 100.64/10 bottom",
		"100.127.255.255":   "CGNAT 100.64/10 top",
		"0.1.2.3":           "this-network 0/8",
		"198.18.0.1":        "benchmarking 198.18/15 bottom",
		"198.19.255.255":    "benchmarking 198.18/15 top",
		"240.0.0.1":         "reserved 240/4",
		"255.255.255.254":   "reserved 240/4 top",
		"64:ff9b::a00:1":    "NAT64 embedding 10.0.0.1",
		"64:ff9b::7f00:1":   "NAT64 embedding 127.0.0.1",
		"::ffff:127.0.0.1":  "IPv4-mapped loopback",
		"::ffff:10.1.2.3":   "IPv4-mapped RFC1918",
		"::ffff:100.64.0.9": "IPv4-mapped CGNAT",
		"192.0.0.1":         "IETF protocol assignments 192.0.0/24 bottom",
		"192.0.0.255":       "IETF protocol assignments 192.0.0/24 top",
		"2002:c0a8:101::1":  "6to4 embedding 192.168.1.1",
		"2002:7f00:1::1":    "6to4 embedding 127.0.0.1",
		"64:ff9b:1::a00:1":  "local-use NAT64 embedding 10.0.0.1",
		"::102:304":         "IPv4-compatible ::/96",
		"::a00:1":           "IPv4-compatible embedding 10.0.0.1",
		"fec0::1":           "deprecated site-local fec0::/10",
		"feff::1":           "deprecated site-local fec0::/10 top",
	}
	for addr, why := range denied {
		ip := net.ParseIP(addr)
		require.NotNil(t, ip, addr)
		assert.False(t, isPubliclyRoutable(ip), "%s (%s) must be refused", addr, why)
	}

	allowed := []string{
		"8.8.8.8",
		"1.1.1.1",
		"100.63.255.255",  // just below CGNAT
		"100.128.0.0",     // just above CGNAT
		"198.17.255.255",  // just below benchmarking
		"198.20.0.0",      // just above benchmarking
		"1.0.0.0",         // just above 0/8
		"223.255.255.254", // just below multicast/240/4
		"2606:4700:4700::1111",
		"64:ff9c::1",   // outside the NAT64 /96
		"192.0.1.1",    // just above 192.0.0/24
		"2003::1",      // just above 6to4 2002::/16
		"64:ff9b:2::1", // just above the local-use NAT64 /48
		"2001:4860:4860::8888",
	}
	for _, addr := range allowed {
		ip := net.ParseIP(addr)
		require.NotNil(t, ip, addr)
		assert.True(t, isPubliclyRoutable(ip), "%s must be allowed", addr)
	}
}

func TestMustParseCIDRs_PanicsOnGarbage(t *testing.T) {
	assert.Panics(t, func() { mustParseCIDRs("not-a-cidr") })
	nets := mustParseCIDRs("10.0.0.0/8", "fd00::/8")
	require.Len(t, nets, 2)
	assert.True(t, nets[0].Contains(net.ParseIP("10.9.9.9")))
}

// TestSSRFSafeDialContext_RefusesLoopbackBeforeConnecting proves the guard
// fires on the resolved address and that no TCP connection ever reaches the
// listener — not merely that an error came back.
func TestSSRFSafeDialContext_RefusesLoopbackBeforeConnecting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	var accepted atomic.Int32
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()

	for _, addr := range []string{ln.Addr().String(), "localhost:" + portOf(t, ln.Addr().String())} {
		conn, derr := ssrfSafeDialContext(context.Background(), "tcp", addr)
		if conn != nil {
			_ = conn.Close()
		}
		require.Error(t, derr, "dialing %s must be refused", addr)
		assert.Contains(t, derr.Error(), "refusing to dial non-public address", addr)
	}
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), accepted.Load(), "the guard must refuse before connect, not after")
}

// TestSSRFSafeDialContext_PublicAddressPassesTheGuard: the guard's allow path.
// Whether the connect itself succeeds depends on the network the test runs on,
// so only the guard's own verdict is asserted: a public address must never be
// refused *by the guard*.
func TestSSRFSafeDialContext_PublicAddressPassesTheGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	conn, err := ssrfSafeDialContext(ctx, "tcp", "1.1.1.1:443")
	if conn != nil {
		_ = conn.Close()
	}
	if err != nil {
		assert.NotContains(t, err.Error(), "refusing to dial", "a public address must pass the SSRF guard")
	}
}

func portOf(t *testing.T, hostport string) string {
	t.Helper()
	_, p, err := net.SplitHostPort(hostport)
	require.NoError(t, err)
	return p
}

// TestNewCIMDHTTPClient_ProductionClientRefusesLoopbackDocumentHost: the real
// (non-test) CIMD fetch client, pointed at a loopback https server, must fail
// with invalid_client and the server must never see the request.
func TestNewCIMDHTTPClient_ProductionClientRefusesLoopbackDocumentHost(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// The guard's reason is for the operator, not for an unauthenticated
	// caller: capture the server log to prove it went THERE.
	var logged bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(prevOut)

	s := &oauthService{httpClient: newCIMDHTTPClient()}
	doc, oerr := s.fetchCIMD(context.Background(), srv.URL+"/client.json")
	assert.Nil(t, doc)
	require.NotNil(t, oerr)
	assert.Equal(t, "invalid_client", oerr.Code)
	assert.Equal(t, "failed to fetch client metadata document", oerr.Description,
		"the caller gets fixed text — the refused address is an oracle for internal hosts")
	assert.Contains(t, logged.String(), "refusing to dial non-public address",
		"the guard, not some other failure, refused the connection, and the reason is in the log")
	assert.Equal(t, int32(0), hits.Load())
}

func TestNewCIMDHTTPClient_NeverFollowsRedirects(t *testing.T) {
	c := newCIMDHTTPClient()
	require.NotNil(t, c.CheckRedirect)
	assert.ErrorIs(t, c.CheckRedirect(nil, nil), http.ErrUseLastResponse)
	assert.Equal(t, oauthCIMDFetchTimeout, c.Timeout)
}

func TestFetchCIMD_RejectsNonHTTPSClientIDWithoutFetching(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	s := &oauthService{httpClient: srv.Client()}
	for _, id := range []string{srv.URL + "/c.json", "https://", "ftp://example.com/c.json", "://nope"} {
		_, oerr := s.fetchCIMD(context.Background(), id)
		require.NotNil(t, oerr, id)
		assert.Equal(t, "invalid_client_metadata", oerr.Code, id)
	}
	assert.Equal(t, int32(0), hits.Load())
}

func TestValidRedirectURI(t *testing.T) {
	cases := map[string]bool{
		"https://app.example.com/cb":          true,
		"https://app.example.com:8443/cb?x=1": true,
		"http://localhost/cb":                 true,
		"http://localhost:33418/cb":           true,
		"http://127.0.0.1:9/cb":               true,
		"http://[::1]:9/cb":                   true,
		"http://example.com/cb":               false, // plain http off-loopback
		"http://10.0.0.1/cb":                  false,
		"https://app.example.com/cb#frag":     false, // fragment smuggling
		"http://localhost/cb#x":               false,
		"javascript:alert(1)":                 false,
		"data:text/html,hi":                   false,
		"myapp://callback":                    false, // custom scheme
		"https:///nohost":                     false,
		"/relative/path":                      false,
		"":                                    false,
		"https://exa mple.com/%zz":            false,
	}
	for raw, want := range cases {
		assert.Equal(t, want, validRedirectURI(raw), "validRedirectURI(%q)", raw)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	assert.True(t, isLoopbackHost("localhost"))
	assert.True(t, isLoopbackHost("127.0.0.1"))
	assert.True(t, isLoopbackHost("127.8.8.8"))
	assert.True(t, isLoopbackHost("::1"))
	assert.False(t, isLoopbackHost("localhost.evil.com"))
	assert.False(t, isLoopbackHost("10.0.0.1"))
	assert.False(t, isLoopbackHost(""))
}

func TestRedirectURIAllowed(t *testing.T) {
	reg := []string{"https://app.example.com/cb", "http://127.0.0.1/callback"}

	assert.True(t, redirectURIAllowed("https://app.example.com/cb", reg), "exact match")
	assert.True(t, redirectURIAllowed("http://127.0.0.1:51234/callback", reg), "loopback, any port")
	assert.True(t, redirectURIAllowed("http://localhost:51234/callback", reg), "loopback aliases match each other")

	assert.False(t, redirectURIAllowed("https://app.example.com:444/cb", reg), "non-loopback port is NOT ignored")
	assert.False(t, redirectURIAllowed("https://app.example.com/cb2", reg))
	assert.False(t, redirectURIAllowed("http://127.0.0.1:1/other", reg), "loopback path must still match")
	assert.False(t, redirectURIAllowed("http://127.0.0.1:1/callback?x=1", reg), "loopback query must still match")
	assert.False(t, redirectURIAllowed("https://127.0.0.1/callback", reg), "loopback scheme must still match")
	assert.False(t, redirectURIAllowed("http://127.0.0.1.evil.com/callback", reg))
	assert.False(t, redirectURIAllowed("http://%zz/callback", reg), "unparsable presented")
	assert.False(t, redirectURIAllowed("http://127.0.0.1/callback", []string{"http://%zz/callback"}), "unparsable registered")
	assert.False(t, redirectURIAllowed("https://app.example.com/cb", nil))
}

func TestAllRedirectURIsLoopback(t *testing.T) {
	assert.False(t, allRedirectURIsLoopback(nil))
	assert.True(t, allRedirectURIsLoopback([]string{"http://localhost/cb", "http://127.0.0.1:9/cb"}))
	assert.False(t, allRedirectURIsLoopback([]string{"http://localhost/cb", "https://app.example.com/cb"}))
	assert.False(t, allRedirectURIsLoopback([]string{"http://%zz/cb"}))
}

func TestPKCEHelpers(t *testing.T) {
	good := strings.Repeat("a", 43)
	assert.True(t, validPKCEVerifier(good))
	assert.True(t, validPKCEVerifier(strings.Repeat("Z9-._~", 21)+"ab")) // 128
	assert.False(t, validPKCEVerifier(strings.Repeat("a", 42)), "too short")
	assert.False(t, validPKCEVerifier(strings.Repeat("a", 129)), "too long")
	assert.False(t, validPKCEVerifier(strings.Repeat("a", 42)+"+"), "outside unreserved charset")
	assert.False(t, validPKCEVerifier(strings.Repeat("a", 42)+"é"))

	sum := sha256.Sum256([]byte(good))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	assert.True(t, validPKCEChallenge(challenge))
	assert.False(t, validPKCEChallenge(challenge[:42]), "wrong length")
	assert.False(t, validPKCEChallenge(challenge+"="), "padded")
	assert.False(t, validPKCEChallenge(strings.Repeat("+", 43)), "std base64 alphabet, not url")
	assert.False(t, validPKCEChallenge(strings.Repeat(".", 43)))

	assert.True(t, verifyPKCE(good, challenge))
	assert.False(t, verifyPKCE(good+"b", challenge))
	assert.False(t, verifyPKCE("", challenge))
	assert.False(t, verifyPKCE(good, ""))
	// "plain" method would mean challenge == verifier; S256 must not accept it.
	assert.False(t, verifyPKCE(good, good))
}

func TestAppendRedirectHelpers(t *testing.T) {
	got := appendRedirectCode("https://app.example.com/cb?keep=1", "abc", "st")
	u, err := url.Parse(got)
	require.NoError(t, err)
	assert.Equal(t, "1", u.Query().Get("keep"), "existing query survives")
	assert.Equal(t, "abc", u.Query().Get("code"))
	assert.Equal(t, "st", u.Query().Get("state"))

	got = appendRedirectError("http://localhost/cb", "access_denied", "")
	u, err = url.Parse(got)
	require.NoError(t, err)
	assert.Equal(t, "access_denied", u.Query().Get("error"))
	_, hasState := u.Query()["state"]
	assert.False(t, hasState, "empty state must be omitted, not sent as state=")

	assert.Equal(t, "http://%zz", appendQuery("http://%zz", map[string]string{"a": "b"}), "unparsable base returned unchanged")
}

func TestIsSlugConflict(t *testing.T) {
	assert.True(t, isSlugConflict(&pq.Error{Code: "23505", Constraint: "uq_agents_workspace_slug"}))
	assert.True(t, isSlugConflict(errors.Join(errors.New("wrapped"), &pq.Error{Code: "23505", Constraint: "uq_agents_workspace_slug"})))
	assert.False(t, isSlugConflict(&pq.Error{Code: "23505", Constraint: "some_other_unique"}))
	assert.False(t, isSlugConflict(&pq.Error{Code: "23503", Constraint: "uq_agents_workspace_slug"}))
	assert.False(t, isSlugConflict(errors.New("23505 uq_agents_workspace_slug")))
	assert.False(t, isSlugConflict(nil))
}

func TestSHA256HexAndRandomHex(t *testing.T) {
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", sha256Hex(""))
	a, err := randomHex(16)
	require.NoError(t, err)
	b, err := randomHex(16)
	require.NoError(t, err)
	assert.Len(t, a, 32)
	assert.NotEqual(t, a, b)
}

// TestSSRFSafeDialContext_RefusesEveryDeniedRangeBeforeConnecting drives the
// DIALER — not just isPubliclyRoutable — with a literal address from every
// range the deny-list adds beyond the net.IP predicates. The end-to-end tests
// cannot reach this: their CIMD host is loopback behind a swapped-in client,
// so the real dialer is never on their path.
func TestSSRFSafeDialContext_RefusesEveryDeniedRangeBeforeConnecting(t *testing.T) {
	for _, addr := range []string{
		"100.64.0.1:443", "0.1.2.3:443", "192.0.0.8:443", "198.18.0.1:443", "240.0.0.1:443",
		"[64:ff9b::a00:1]:443", "[64:ff9b:1::a00:1]:443", "[2002:c0a8:101::1]:443",
		"[::102:304]:443", "[fec0::1]:443",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		conn, err := ssrfSafeDialContext(ctx, "tcp", addr)
		cancel()
		if conn != nil {
			_ = conn.Close()
		}
		require.Error(t, err, "%s must be refused", addr)
		assert.Contains(t, err.Error(), "refusing to dial non-public address", "%s must be refused by the guard, not fail for another reason", addr)
	}
}

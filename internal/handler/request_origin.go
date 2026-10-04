package handler

import "net/http"

// requestOrigin resolves the origin (scheme and host) a client used to reach
// the API, for building canonical deep-links on responses. Behind the reverse
// proxy (Caddy) the request hop the Go server sees is internal http on a
// bare host, so the public values come from X-Forwarded-Proto and
// X-Forwarded-Host; without them the request's own scheme/host is used
// (direct access in dev and tests). Single source of this logic for
// computeTaskURL, computeDocumentURL and computeProjectURL — it used to be
// three hand-copied blocks that could drift apart.
func requestOrigin(r *http.Request) (scheme, host string) {
	scheme = "https"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	} else if r.TLS == nil {
		scheme = "http"
	}
	host = r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme, host
}

package teamrelay

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// SyncFilesIndex
// ---------------------------------------------------------------------------

func TestSyncFilesIndex_ReadsSHA256AndUpdatedAt(t *testing.T) {
	// AC-2: the response has to carry sha256 and updated_at, or R3 (staleness
	// check) and R8 (safe write-back) have nothing to compare against. Asserted
	// on the parsed VALUE, not just that the call succeeded.
	var gotPath, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Get("X-Agent-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"path":"Notes/Welcome.md","sha256":"abc123","size":42,"updated_at":"2026-08-20T10:00:00Z","type":"doc"}]`))
	}))
	defer srv.Close()

	entries, err := SyncFilesIndex(context.Background(), srv.URL, "8c5e7efd-0000-0000-0000-000000000000", "tr_agent_secret")

	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "Notes/Welcome.md", entries[0].Path)
	assert.Equal(t, "abc123", entries[0].SHA256)
	assert.Equal(t, int64(42), entries[0].Size)
	assert.Equal(t, "2026-08-20T10:00:00Z", entries[0].UpdatedAt)
	// UUID share_id in the path, not a slug — the sync protocol doesn't accept one.
	assert.Equal(t, "/v1/shares/8c5e7efd-0000-0000-0000-000000000000/files-index", gotPath)
	assert.Equal(t, "tr_agent_secret", gotHeader)
}

func TestSyncFilesIndex_EmptyListIsNotNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	entries, err := SyncFilesIndex(context.Background(), srv.URL, "share-id", "k")

	require.NoError(t, err)
	assert.NotNil(t, entries)
	assert.Empty(t, entries)
}

func TestSyncFilesIndex_RejectedKeyIsErrKeyRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := SyncFilesIndex(context.Background(), srv.URL, "share-id", "bad-key")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrKeyRejected)
}

func TestSyncFilesIndex_ForeignShareIsErrForeignShare(t *testing.T) {
	// Distinct from ErrKeyRejected: a real key, wrong share (403), measured live
	// on this exact protocol per #ee1745ce. Collapsing this into ErrKeyRejected
	// would tell an operator to rotate a key that was never the problem.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := SyncFilesIndex(context.Background(), srv.URL, "share-id", "k")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrForeignShare)
	assert.NotErrorIs(t, err, ErrKeyRejected)
}

// TestSyncFilesIndex_ExpiredKeyIsErrKeyExpired is the direct regression test
// for #218d5847's AC4: an expired key and a foreign-share key are BOTH 403,
// and before this the distinction was thrown away — classifySyncError
// returned ErrForeignShare for either, so nothing downstream could ever tell
// "renew your key" from "this key was never valid here". The body shape here
// is Team Relay's REAL error envelope (middleware/errors.py), not FastAPI's
// bare {"detail": "..."} default — verified against the Team Relay source
// directly, not assumed.
func TestSyncFilesIndex_ExpiredKeyIsErrKeyExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Agent key has expired","request_id":"req-1"}}`))
	}))
	defer srv.Close()

	_, err := SyncFilesIndex(context.Background(), srv.URL, "share-id", "k")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrKeyExpired)
	assert.NotErrorIs(t, err, ErrForeignShare)
}

// TestSyncFilesIndex_ForeignShareWithRealEnvelope_IsStillErrForeignShare is
// the negative control for the test above: a 403 whose real envelope carries
// a DIFFERENT message must not accidentally match keyExpiredMessage and must
// still classify as ErrForeignShare. Without this, a change that made the
// match too loose (a substring check instead of an exact one, say) would
// pass the positive test above for the wrong reason.
func TestSyncFilesIndex_ForeignShareWithRealEnvelope_IsStillErrForeignShare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Agent key not valid for this share","request_id":"req-2"}}`))
	}))
	defer srv.Close()

	_, err := SyncFilesIndex(context.Background(), srv.URL, "share-id", "k")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrForeignShare)
	assert.NotErrorIs(t, err, ErrKeyExpired)
}

func TestSyncFilesIndex_UnreachableIsErrUnreachable(t *testing.T) {
	// A closed server: connection refused, not a status code. This is the
	// "we could not ask" case, distinct from "we asked and were told no".
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	badURL := srv.URL
	srv.Close() // port is now refusing connections

	_, err := SyncFilesIndex(context.Background(), badURL, "share-id", "k")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnreachable)
}

func TestSyncFilesIndex_RefusesWithoutARelayURL(t *testing.T) {
	_, err := SyncFilesIndex(context.Background(), "", "share-id", "k")
	require.Error(t, err)
}

// --- AC-7: no /v1/web/shares/... in this client's code path ---------------

func TestSyncFilesIndex_NeverCallsTheWebPublishFamily(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NotContains(t, r.URL.Path, "/v1/web/shares/")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	_, err := SyncFilesIndex(context.Background(), srv.URL, "share-id", "k")
	require.NoError(t, err)
}

// --- version tolerance ------------------------------------------------------

func TestSyncFilesIndex_UnknownFieldDoesNotBreakParsing(t *testing.T) {
	// Go ignores unknown JSON fields by construction — this test documents
	// that fact against the REAL parse path rather than asserting nothing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"path":"a.md","sha256":"x","size":1,"updated_at":"t","type":"doc","mime":"text/markdown","source":"sync-artifact","brand_new_field_from_the_future":{"nested":true}}]`))
	}))
	defer srv.Close()

	entries, err := SyncFilesIndex(context.Background(), srv.URL, "share-id", "k")

	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "a.md", entries[0].Path)
}

func TestSyncFilesIndex_TypeChangeOnAConsumedFieldFailsLoudly(t *testing.T) {
	// The counterpart to the unknown-field test above: a type change on a field
	// we DO declare (size: int64) must not silently zero out — it must error,
	// so a caller building on Size never gets a wrong number instead of a
	// visible failure. A test asserting only "added a field, nothing broke"
	// would be vacuously green here too and prove nothing about this half.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"path":"a.md","sha256":"x","size":"not-a-number","updated_at":"t","type":"doc"}]`))
	}))
	defer srv.Close()

	_, err := SyncFilesIndex(context.Background(), srv.URL, "share-id", "k")

	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// SyncDownload
// ---------------------------------------------------------------------------

func TestSyncDownload_ReadsBodyAndHashFromHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Notes/Welcome.md", r.URL.Query().Get("path"))
		w.Header().Set("ETag", `"deadbeef"`)
		w.Header().Set("X-Updated-At", "2026-08-20T10:00:00Z")
		_, _ = w.Write([]byte("# Hello"))
	}))
	defer srv.Close()

	doc, err := SyncDownload(context.Background(), srv.URL, "share-id", "Notes/Welcome.md", "k")

	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, "# Hello", string(doc.Content))
	// ETag is quoted per HTTP convention — the quotes are not part of the hash.
	assert.Equal(t, "deadbeef", doc.SHA256)
	assert.Equal(t, "2026-08-20T10:00:00Z", doc.UpdatedAt)
}

func TestSyncDownload_MissingIsErrNotFound(t *testing.T) {
	// Negative control for AC-5: a document that isn't there gets a distinct,
	// named error — never confused with an empty-but-present document.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	doc, err := SyncDownload(context.Background(), srv.URL, "share-id", "gone.md", "k")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.Nil(t, doc)
}

func TestSyncDownload_ReadsCyrillicPathAndBody(t *testing.T) {
	// AC-3: a document with a Cyrillic title reads whole. The path travels as
	// a query parameter — url.QueryEscape handles the encoding — and the
	// server is trusted to echo back what the request asked for.
	const cyrillicPath = "Планы/План изменений Argus 2026-06-13.md"
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("path")
		w.Header().Set("ETag", `"cyr123"`)
		_, _ = w.Write([]byte("# Кириллица работает"))
	}))
	defer srv.Close()

	doc, err := SyncDownload(context.Background(), srv.URL, "share-id", cyrillicPath, "k")

	require.NoError(t, err)
	assert.Equal(t, cyrillicPath, gotQuery)
	assert.Equal(t, "# Кириллица работает", string(doc.Content))
}

// ---------------------------------------------------------------------------
// RequestFileToken / FetchAttachment
// ---------------------------------------------------------------------------

func TestRequestFileToken_UsesAPlaceholderHashWhenNoneIsKnown(t *testing.T) {
	// The load-bearing finding this codifies: sha256/content_length are
	// required by the request SCHEMA but not verified server-side on read
	// (shares.py:297-327, measured live on #ee1745ce). An attachment resolved
	// through an embed link never has a known hash ahead of the request — this
	// asserts the placeholder path actually works end to end, not just that it
	// compiles.
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"ft_abc","base_url":"https://cp.tr.entire.vc/shares/s1/files/image.png"}`))
	}))
	defer srv.Close()

	token, baseURL, err := RequestFileToken(context.Background(), srv.URL, "s1", "image.png", "k", "", 0)

	require.NoError(t, err)
	assert.Equal(t, "ft_abc", token)
	assert.Equal(t, "https://cp.tr.entire.vc/shares/s1/files/image.png", baseURL)
	assert.Contains(t, string(gotBody), unverifiedPlaceholderSHA256)
}

func TestRequestFileToken_ReturnsTheServersBaseURLVerbatim(t *testing.T) {
	// The load-bearing rule from the brief: don't reconstruct the server's
	// quote(path, safe='/') encoding — use base_url as returned.
	const serverURL = "https://cp.tr.entire.vc/shares/s1/files/nested%2Fpath/with%20space.png"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"t","base_url":"` + serverURL + `"}`))
	}))
	defer srv.Close()

	_, baseURL, err := RequestFileToken(context.Background(), srv.URL, "s1", "nested/path/with space.png", "k", "", 0)

	require.NoError(t, err)
	assert.Equal(t, serverURL, baseURL)
}

// --- Deliberate asymmetry vs Team Relay's browser-facing embed path --------
//
// Verified live (task #836ebffe, real Chromium against a request-line-logging
// server): a browser requesting `<img src="/{slug}/_assets/pic#1.png">`
// truncates everything from '#' onward BEFORE the request is sent — the
// server never sees it — and a target containing '?' loses everything from
// '?' onward the same way, one hop later, when SvelteKit's router splits it
// as a query string it never forwards. This client has no browser in its
// path, so it must NOT reproduce either truncation: a real attachment named
// "pic#1.png" is unreachable through the browser embed path but must remain
// reachable through this one, because our POST body is JSON, not a URL a
// browser parses.
func TestRequestFileToken_DoesNotTruncateHashUnlikeTheBrowserEmbedPath(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"token":"t","base_url":"x"}`))
	}))
	defer srv.Close()

	_, _, err := RequestFileToken(context.Background(), srv.URL, "s1", "pic#1.png", "k", "", 0)

	require.NoError(t, err)
	// A browser would never send "#1.png" at all (RFC 3986 fragment). This
	// server-to-server call must, or the attachment becomes unreachable.
	assert.Contains(t, string(gotBody), `"path":"pic#1.png"`)
}

func TestRequestFileToken_DoesNotDropQueryTailUnlikeTheBrowserEmbedPath(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"token":"t","base_url":"x"}`))
	}))
	defer srv.Close()

	_, _, err := RequestFileToken(context.Background(), srv.URL, "s1", "pic?x.png", "k", "", 0)

	require.NoError(t, err)
	// SvelteKit's router would match only "pic" and silently drop "?x.png" —
	// verified live. This path must carry the whole target through.
	assert.Contains(t, string(gotBody), `"path":"pic?x.png"`)
}

func TestRequestFileToken_TolerateMalformedPercentEscapeUnlikeGosURLParser(t *testing.T) {
	// A literal '%' not followed by two hex digits is a legal Obsidian
	// filename character with no encoding intent behind it — verified live
	// that a real browser passes it through unmodified. net/url.Parse and
	// url.PathUnescape both hard-fail on it (verified via go run,
	// task #836ebffe) — which is exactly why this call must never route the
	// raw target through either: it goes into a JSON body untouched instead.
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"token":"t","base_url":"x"}`))
	}))
	defer srv.Close()

	_, _, err := RequestFileToken(context.Background(), srv.URL, "s1", "pic%file.png", "k", "", 0)

	require.NoError(t, err)
	assert.Contains(t, string(gotBody), `"path":"pic%file.png"`)
}

func TestFetchAttachment_TwoHopFlow(t *testing.T) {
	// Hop 1: base_url/download-url with Bearer file-token → presigned URL.
	// Hop 2: GET the presigned URL with NO relay credential attached.
	fileBytes := []byte("PNGDATA")
	presignedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A presigned URL is its own credential — sending our agent key to it
		// would leak it to whatever host the presigned URL happens to point at.
		assert.Empty(t, r.Header.Get("X-Agent-Key"))
		assert.Empty(t, r.Header.Get("Authorization"))
		_, _ = w.Write(fileBytes)
	}))
	defer presignedSrv.Close()

	var gotAuthHeader string
	relaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/shares/s1/files/image.png/download-url", r.URL.Path)
		gotAuthHeader = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"downloadUrl":"` + presignedSrv.URL + `"}`))
	}))
	defer relaySrv.Close()

	data, err := FetchAttachment(context.Background(), relaySrv.URL+"/shares/s1/files/image.png", "ft_abc")

	require.NoError(t, err)
	assert.Equal(t, fileBytes, data)
	assert.Equal(t, "Bearer ft_abc", gotAuthHeader)
}

func TestFetchAttachment_NotInIndexIsStillFetchable(t *testing.T) {
	// AC-5's sharper form: an attachment absent from files-index (which only
	// lists sync-artifact rows — see SyncIndexEntry) must still be fetchable
	// through the file-token flow, because that flow never consults the index
	// at all. This test constructs a files-index response that does NOT
	// contain the attachment's path, then fetches the attachment anyway —
	// proving the two paths are genuinely independent, not proving a mock
	// returns what it was told to.
	indexSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"path":"Notes/Welcome.md","sha256":"x","size":1,"updated_at":"t","type":"doc"}]`))
	}))
	defer indexSrv.Close()

	entries, err := SyncFilesIndex(context.Background(), indexSrv.URL, "s1", "k")
	require.NoError(t, err)
	for _, e := range entries {
		require.NotEqual(t, "assets/photo.png", e.Path, "fixture is invalid: attachment must be absent from the index")
	}

	fileBytes := []byte("PHOTO")
	presignedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fileBytes)
	}))
	defer presignedSrv.Close()
	var relaySrv *httptest.Server
	relaySrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download-url") {
			_, _ = w.Write([]byte(`{"downloadUrl":"` + presignedSrv.URL + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"ft_x","base_url":"` + relaySrv.URL + `/shares/s1/files/assets/photo.png"}`))
	}))
	defer relaySrv.Close()

	token, baseURL, err := RequestFileToken(context.Background(), relaySrv.URL, "s1", "assets/photo.png", "k", "", 0)
	require.NoError(t, err)

	data, err := FetchAttachment(context.Background(), baseURL, token)
	require.NoError(t, err)
	assert.Equal(t, fileBytes, data)
}

func TestFetchAttachment_NonexistentAttachmentIsErrNotFound(t *testing.T) {
	relaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer relaySrv.Close()

	_, err := FetchAttachment(context.Background(), relaySrv.URL+"/shares/s1/files/gone.png", "ft_abc")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
}

// ---------------------------------------------------------------------------
// Generic server-side rejection propagation
// ---------------------------------------------------------------------------

// This test used to be pinned to a specific upstream bug (ё/Ё rejected by
// Team Relay's file-path regex, task #ee1745ce) and framed as a drift guard:
// "currently RED on purpose, goes green the day upstream ships a fix." That
// premise was stale before the test even merged — evc-team-relay#213 (ё/Ё
// added to the allowed pattern) deployed ~2s before evc-mesh#671 merged the
// "still RED, tracked upstream" comment, three hours later. And the test
// could never have gone green OR red from upstream's actual state either
// way: it runs entirely against its own httptest mock, never against real
// Team Relay, so nothing about TR's live regex is observable from it.
// Verified live 2026-08-20: a path containing ё/Ё is accepted (200) by real
// Team Relay today, not rejected — task #a353fbb1.
//
// What the test correctly verified all along — our client surfaces a 400
// from the relay rather than swallowing it — has nothing to do with ё. That
// part is real and kept below, with the mock's rejection trigger now an
// arbitrary string instead of a claim about a specific character.
func TestRequestFileToken_PropagatesServerRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("reject-me")) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"detail":"path contains invalid characters"}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"t","base_url":"x"}`))
	}))
	defer srv.Close()

	_, _, err := RequestFileToken(context.Background(), srv.URL, "s1", "reject-me.png", "k", "", 0)
	require.Error(t, err, "client must surface a server-side path rejection, not swallow it")
}

// Negative control for the test above: the same mock, but a path it accepts.
// Without this, TestRequestFileToken_PropagatesServerRejection could pass
// vacuously against a mock (or a mutated client) that errors unconditionally.
func TestRequestFileToken_PropagatesServerRejection_AcceptedPathSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("reject-me")) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"detail":"path contains invalid characters"}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"t","base_url":"x"}`))
	}))
	defer srv.Close()

	token, baseURL, err := RequestFileToken(context.Background(), srv.URL, "s1", "fine.png", "k", "", 0)
	require.NoError(t, err)
	assert.Equal(t, "t", token)
	assert.Equal(t, "x", baseURL)
}

// ---------------------------------------------------------------------------
// SyncWrite — the conditional write-back (R8)
// ---------------------------------------------------------------------------

// The wire contract in one test: method, path family, the quoted If-Match, and
// the body. Asserted against what the server actually receives, because every
// one of these is a place a blind write could hide.
func TestSyncWrite_SendsConditionalPutOnTheSyncFamily(t *testing.T) {
	var gotMethod, gotPath, gotQuery, gotIfMatch, gotKey, gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.Query().Get("path")
		gotIfMatch = r.Header.Get("If-Match")
		gotKey = r.Header.Get("X-Agent-Key")
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"notes/a.md","sha256":"newhash","size":7}`))
	}))
	defer srv.Close()

	res, err := SyncWrite(context.Background(), srv.URL, "share-uuid", "notes/a.md", "key", "oldhash", []byte("content"))
	require.NoError(t, err)

	assert.Equal(t, http.MethodPut, gotMethod)
	assert.Equal(t, "/v1/shares/share-uuid/sync-write", gotPath)
	assert.NotContains(t, gotPath, "/v1/web/", "the write must never address the web-publish family")
	assert.Equal(t, "notes/a.md", gotQuery)
	assert.Equal(t, `"oldhash"`, gotIfMatch, "If-Match must carry the prior sha256, quoted per HTTP convention")
	assert.Equal(t, "key", gotKey)
	assert.Equal(t, "text/markdown", gotCT, "markdown must be declared as text so the relay files it as a document, not an asset")
	assert.Equal(t, "content", gotBody)
	assert.Equal(t, "newhash", res.SHA256)
}

// 412 is the whole point: it must arrive as ErrSyncConflict and nothing else,
// because that is the only signal that distinguishes "the original moved" from
// "the relay is broken".
func TestSyncWrite_PreconditionFailedIsErrSyncConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = w.Write([]byte(`{"detail":"sha256 mismatch: the file changed since it was read"}`))
	}))
	defer srv.Close()

	_, err := SyncWrite(context.Background(), srv.URL, "s", "p.md", "key", "stale", []byte("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncConflict)
	assert.NotErrorIs(t, err, ErrSyncPreconditionMissing, "a lost race is not the same fault as a malformed request")
}

// 428 means we sent an unconditional write — our bug, and deliberately a
// different sentinel so no caller can "handle" it by retrying.
func TestSyncWrite_PreconditionRequiredIsItsOwnSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPreconditionRequired)
	}))
	defer srv.Close()

	_, err := SyncWrite(context.Background(), srv.URL, "s", "p.md", "key", "somehash", []byte("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncPreconditionMissing)
	assert.NotErrorIs(t, err, ErrSyncConflict)
}

// An empty precondition never reaches the network. If it did, the only thing
// standing between us and a blind overwrite would be the relay's own 428.
func TestSyncWrite_EmptyPreconditionNeverLeavesTheProcess(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := SyncWrite(context.Background(), srv.URL, "s", "p.md", "key", "", []byte("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSyncPreconditionMissing)
	assert.False(t, called, "a write with no precondition must be refused locally, not sent and rejected")
}

// A key without the write scope is a 403, and must stay legible as one — this
// is the error an operator sees if the agent key was minted read-only.
func TestSyncWrite_MissingWriteScopeIsErrForeignShare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"agent key lacks required scope: write"}`))
	}))
	defer srv.Close()

	_, err := SyncWrite(context.Background(), srv.URL, "s", "p.md", "key", "h", []byte("x"))
	assert.ErrorIs(t, err, ErrForeignShare)
}

// A 200 with no sha256 poisons the next write's precondition, so it is refused
// here rather than stored.
func TestSyncWrite_ResponseWithoutHashIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"path":"p.md","size":1}`))
	}))
	defer srv.Close()

	_, err := SyncWrite(context.Background(), srv.URL, "s", "p.md", "key", "h", []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no sha256")
}

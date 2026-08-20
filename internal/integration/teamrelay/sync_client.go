package teamrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// Sync-protocol client (GET/POST /v1/shares/...), NOT the web-publish family
// (/v1/web/shares/...). Pavel's 2026-08-20 decision excludes web-publish from
// the Docs↔Team-Relay reintegration entirely — it never carries sha256, and
// this package's other file, content.go, exists to serve a different, older
// feature (the relay:// preview) that this file does not touch or replace.
//
// Every function here talks to shareID as a UUID (shares.id), never a slug —
// the sync protocol does not accept one. Settings.ShareID, not ShareSlug.

// Sentinel errors, distinguishable by the caller via errors.Is without
// string-matching a log line. Before this, a rejected key and an unrelated 502
// both surfaced as one opaque error, and diagnosing which had happened cost a
// live investigation (task #c55556fa) that the client itself could have
// answered in its own log line — see classifySyncError.
var (
	// ErrKeyRejected means the request carried no X-Agent-Key, or a value the
	// relay does not recognize (401 — missing header or unknown key hash).
	ErrKeyRejected = errors.New("teamrelay: agent key rejected")
	// ErrForeignShare means the key is real but is not valid for the share ID
	// requested (403 — measured live on this exact protocol, task #ee1745ce).
	ErrForeignShare = errors.New("teamrelay: agent key not valid for this share")
	// ErrUnreachable means the request never got a response at all — DNS,
	// connection refused, timeout. Distinct from a rejected key: this is "we
	// could not ask", not "we asked and were told no".
	ErrUnreachable = errors.New("teamrelay: relay unreachable")
	// ErrNotFound means the relay responded 404: the share or the path inside
	// it does not exist. Ordinary — the caller answers "not found", not "broken".
	ErrNotFound = errors.New("teamrelay: not found")
)

// classifySyncError maps a non-200 sync-protocol response to one of the
// sentinels above and logs the distinguishing detail. This is what was missing
// on every read call before: transport() (the upload path) already logged a
// rejected key at client.go:214, but nothing on the read side did, so a log
// search for "agent key rejected" during a real incident (#c55556fa) surfaced
// only the unrelated upload-path failure while the actual read-path failure —
// a different cause, an unrelated feature — left no trace at all.
func classifySyncError(op, shareID string, statusCode int, body []byte) error {
	switch statusCode {
	case http.StatusUnauthorized:
		log.Printf("teamrelay: %s rejected — agent key missing or unrecognized (share %s, status 401): %s", op, shareID, body)
		return ErrKeyRejected
	case http.StatusForbidden:
		log.Printf("teamrelay: %s rejected — agent key not valid for share %s (status 403): %s", op, shareID, body)
		return ErrForeignShare
	case http.StatusNotFound:
		return ErrNotFound
	default:
		log.Printf("teamrelay: %s unexpected status %d for share %s: %s", op, statusCode, shareID, body)
		return fmt.Errorf("teamrelay: %s: unexpected status %d", op, statusCode)
	}
}

// SyncIndexEntry is one row of GET /v1/shares/{id}/files-index.
//
// Only the fields this client actually reads are declared. Go ignores unknown
// JSON fields by construction (grep confirms zero uses of DisallowUnknownFields
// anywhere in this repo) — what breaks a client is a TYPE change on a field it
// DID declare, silently or not. Declaring size/mime/source "for future use"
// would turn a harmless upstream addition into our outage; adding a field here
// is a decision to accept that risk for it, made when something actually needs
// it.
//
// files-index does not list every file in a share. It only carries rows where
// source == "sync-artifact" AND sha256 is non-empty — an attachment pushed by
// user-upload has neither, and is invisible here even though it can still be
// fetched (see RequestFileToken). "Not in this list" therefore never means
// "does not exist"; it means "not a synced document". Do not build an
// existence check on this list's contents.
type SyncIndexEntry struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	UpdatedAt string `json:"updated_at"`
	Type      string `json:"type"`
}

// SyncFilesIndex calls GET /v1/shares/{shareID}/files-index and returns the
// share's sync-artifact rows. shareID is the share UUID (shares.id), not a
// slug.
func SyncFilesIndex(ctx context.Context, relayURL, shareID, agentKey string) ([]SyncIndexEntry, error) {
	if relayURL == "" {
		return nil, fmt.Errorf("teamrelay: relay URL not configured")
	}

	endpoint := fmt.Sprintf("%s/v1/shares/%s/files-index", strings.TrimRight(relayURL, "/"), url.PathEscape(shareID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("teamrelay: build files-index request: %w", err)
	}
	req.Header.Set("X-Agent-Key", agentKey)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes))

	if resp.StatusCode != http.StatusOK {
		return nil, classifySyncError("files-index", shareID, resp.StatusCode, body)
	}

	var entries []SyncIndexEntry
	if jsonErr := json.Unmarshal(body, &entries); jsonErr != nil {
		return nil, fmt.Errorf("teamrelay: parse files-index response: %w", jsonErr)
	}
	if entries == nil {
		entries = []SyncIndexEntry{}
	}
	return entries, nil
}

// SyncDocument is a document body read via the sync protocol, carrying the two
// fields R1's schema exists to hold: SHA256 (source_sha256) and UpdatedAt (the
// caller stamps synced_at from it at write time).
type SyncDocument struct {
	Content   []byte
	SHA256    string
	UpdatedAt string
}

// SyncDownload calls GET /v1/shares/{shareID}/download?path= and returns the
// body together with the sha256 and updated-at Team Relay attaches to it.
//
// The hash comes from the ETag response header (quoted, per HTTP convention —
// stripped here), not a second files-index call: the relay computes it from
// the same raw bytes either way (control-plane hashes the request body it
// wrote, not a normalized or re-encoded form of it), so a caller that already
// has the body from this one call never needs files-index just to learn its
// hash.
func SyncDownload(ctx context.Context, relayURL, shareID, path, agentKey string) (*SyncDocument, error) {
	if relayURL == "" {
		return nil, fmt.Errorf("teamrelay: relay URL not configured")
	}

	endpoint := fmt.Sprintf("%s/v1/shares/%s/download?path=%s",
		strings.TrimRight(relayURL, "/"), url.PathEscape(shareID), url.QueryEscape(path))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("teamrelay: build download request: %w", err)
	}
	req.Header.Set("X-Agent-Key", agentKey)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes))

	if resp.StatusCode != http.StatusOK {
		return nil, classifySyncError("download", shareID, resp.StatusCode, body)
	}

	return &SyncDocument{
		Content:   body,
		SHA256:    strings.Trim(resp.Header.Get("ETag"), `"`),
		UpdatedAt: resp.Header.Get("X-Updated-At"),
	}, nil
}

// fileTokenRequest is the body of POST /v1/shares/{id}/file-token.
//
// SHA256 and ContentLength are required by the request schema but NOT
// verified server-side on read: _decode_file_token (shares.py:297-327) checks
// only scope, share_id and path. Measured live, not assumed (task #ee1745ce) —
// this is why RequestFileToken accepts them as optional hints rather than
// mandatory arguments a caller has to somehow produce for an attachment it has
// never downloaded before.
type fileTokenRequest struct {
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	ContentType   string `json:"content_type"`
	ContentLength int64  `json:"content_length"`
}

type fileTokenResponse struct {
	Token   string `json:"token"`
	BaseURL string `json:"base_url"`
}

// unverifiedPlaceholderSHA256 fills FileTokenRequest.SHA256 when the caller
// has no real hash for the attachment — the ordinary case for anything
// resolved through an embed link, since attachments never appear in
// files-index (see SyncIndexEntry) and so their hash is never known to us
// ahead of the request. Verified safe live, not assumed: see the doc comment
// on fileTokenRequest.
const unverifiedPlaceholderSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"

// RequestFileToken exchanges scoped access for one attachment path via
// POST /v1/shares/{shareID}/file-token. sha256Hint and contentLength may be
// left as "" / 0 when unknown — see fileTokenRequest's doc comment for why
// that is safe on this endpoint specifically, and only this one.
//
// The returned baseURL is the server's own already-encoded URL
// (base_url = f"{base}/shares/{share_id}/files/{quote(path, safe='/')}",
// shares.py) — pass it to FetchAttachment verbatim. Do not re-derive it from
// path with url.PathEscape or url.QueryEscape: Python's quote(path, safe='/')
// does not match either one exactly (both encode differently for at least
// some of !'()* and space), and reconstructing it would silently diverge for
// the inputs where it matters.
func RequestFileToken(ctx context.Context, relayURL, shareID, path, agentKey, sha256Hint string, contentLength int64) (token, baseURL string, err error) {
	if relayURL == "" {
		return "", "", fmt.Errorf("teamrelay: relay URL not configured")
	}
	if sha256Hint == "" {
		sha256Hint = unverifiedPlaceholderSHA256
	}

	reqBody, err := json.Marshal(fileTokenRequest{
		Path:          path,
		SHA256:        sha256Hint,
		ContentType:   "application/octet-stream",
		ContentLength: contentLength,
	})
	if err != nil {
		return "", "", fmt.Errorf("teamrelay: encode file-token request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1/shares/%s/file-token", strings.TrimRight(relayURL, "/"), url.PathEscape(shareID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(reqBody)))
	if err != nil {
		return "", "", fmt.Errorf("teamrelay: build file-token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Key", agentKey)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes))

	if resp.StatusCode != http.StatusOK {
		return "", "", classifySyncError("file-token", shareID, resp.StatusCode, body)
	}

	var parsed fileTokenResponse
	if jsonErr := json.Unmarshal(body, &parsed); jsonErr != nil {
		return "", "", fmt.Errorf("teamrelay: parse file-token response: %w", jsonErr)
	}
	if parsed.Token == "" || parsed.BaseURL == "" {
		return "", "", fmt.Errorf("teamrelay: file-token response missing token or base_url")
	}
	return parsed.Token, parsed.BaseURL, nil
}

// FetchAttachment reads an attachment's bytes given the token and baseURL
// RequestFileToken returned. Two hops: baseURL/download-url (Bearer file
// token) resolves a short-lived presigned MinIO URL, and that URL is then
// fetched with no relay credential on it at all — a presigned URL is its own
// credential, and attaching X-Agent-Key to it would be sending our key to
// whatever host it happens to point at.
func FetchAttachment(ctx context.Context, baseURL, fileToken string) ([]byte, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/download-url"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("teamrelay: build download-url request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+fileToken)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes))
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, classifySyncError("download-url", "", resp.StatusCode, body)
	}

	var parsed struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if jsonErr := json.Unmarshal(body, &parsed); jsonErr != nil {
		return nil, fmt.Errorf("teamrelay: parse download-url response: %w", jsonErr)
	}
	if parsed.DownloadURL == "" {
		return nil, fmt.Errorf("teamrelay: download-url response missing downloadUrl")
	}

	presignedReq, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.DownloadURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("teamrelay: build presigned request: %w", err)
	}
	presignedResp, err := relayHTTPClient.Do(presignedReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = presignedResp.Body.Close() }()

	if presignedResp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if presignedResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("teamrelay: attachment fetch: unexpected status %d", presignedResp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(presignedResp.Body, maxDocumentBytes))
}

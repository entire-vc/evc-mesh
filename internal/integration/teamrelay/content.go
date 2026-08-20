package teamrelay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Document is one file's content, as Team Relay stores it.
type Document struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// FetchDocument reads a document's source from a Team Relay share.
//
// # Why /files and not /download
//
// Team Relay exposes two ways to read the same bytes, and they differ in a way
// that decides whether this works at all:
//
//   - GET /v1/web/shares/{slug}/files?path=  — JSON, and its auth helper treats a
//     write-scoped key as satisfying a read requirement.
//   - GET /v1/web/shares/{id}/download?path= — raw bytes, and its auth helper
//     requires the literal "read" scope, always, even on a public share.
//
// Agent keys are issued with scopes ["write"] by default. Measured on the Team
// Relay side at the time this was written: of the live keys, 18 carry read+write
// and 7 carry write only — and one of those seven is ours. Reaching for
// /download would therefore have failed with `403 Agent key does not have read
// scope` on the key we already have, and would have looked like "the endpoint
// does not work" rather than "we asked the wrong one".
//
// So this uses /files, which the keys we hold today already satisfy. The cost is
// stated rather than hidden: /files takes only a slug (not a share UUID) and
// only serves published shares, so an unpublished share cannot be read this way.
// That case needs a read-scoped key and /download, and is deliberately not
// guessed at here.
//
// The key never leaves the server. Team Relay does not accept it as a query
// parameter at all (a deliberate change on their side, for the same reason our
// own #1f458291 existed: query strings reach proxy logs, referrers and history),
// so it travels only as a header, from this process.
func FetchDocument(ctx context.Context, relayURL, shareSlug, docPath, agentKey string) (*Document, error) {
	if relayURL == "" {
		return nil, fmt.Errorf("teamrelay: relay URL not configured")
	}

	endpoint := fmt.Sprintf("%s/v1/web/shares/%s/files?path=%s",
		strings.TrimRight(relayURL, "/"),
		url.PathEscape(shareSlug),
		url.QueryEscape(docPath),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("teamrelay: build content request: %w", err)
	}
	// Sent even for a public share, where it is not required: the same call then
	// works for a private one without a second code path deciding, from
	// information this process does not have, which kind of share it is talking
	// to.
	if agentKey != "" {
		req.Header.Set("X-Agent-Key", agentKey)
	}

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("teamrelay: content HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Bounded: this body is handed to a browser, and an unbounded read of a
	// remote response is a memory limit set by somebody else.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes))

	switch resp.StatusCode {
	case http.StatusOK:
		var doc Document
		if jsonErr := json.Unmarshal(body, &doc); jsonErr != nil {
			return nil, fmt.Errorf("teamrelay: parse content response: %w", jsonErr)
		}
		return &doc, nil
	case http.StatusNotFound:
		// The share or the path does not exist. Nil, not an error: the caller
		// answers 404, and a missing document is not a failure of ours.
		return nil, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("teamrelay: agent key rejected (status %d) reading %s from share %s",
			resp.StatusCode, docPath, shareSlug)
	default:
		return nil, fmt.Errorf("teamrelay: unexpected status %d reading %s from share %s",
			resp.StatusCode, docPath, shareSlug)
	}
}

// maxDocumentBytes caps what we will read from the relay in one response.
//
// Matches the document body cap on our own side, so a Team Relay document that
// is too large to open is too large for the same reason ours would be, rather
// than for a second, undocumented one.
const maxDocumentBytes = 5 << 20

// StripFrontMatter removes a leading YAML front-matter block.
//
// Obsidian writes one on most notes, and our editor has no concept of it: left
// in place it renders as a horizontal rule followed by the raw `title: "..."`
// lines, which is the first thing a reader sees and is not the document. The
// block is removed for DISPLAY only — nothing here writes back to Team Relay, so
// there is no round-trip to lose it from.
//
// Returns the body and the raw front matter, so a caller that wants the title
// can read it without parsing the document twice.
func StripFrontMatter(source string) (body, frontMatter string) {
	normalized := strings.ReplaceAll(source, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return source, ""
	}
	rest := normalized[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		// An unterminated block is not front matter; it is a document that
		// happens to start with a rule. Guessing otherwise would silently eat
		// the whole text.
		return source, ""
	}
	after := rest[end+len("\n---"):]
	after = strings.TrimPrefix(after, "\n")
	return after, rest[:end]
}

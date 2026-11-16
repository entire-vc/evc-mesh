package teamrelay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
)

// RelayDoc is a searchable TR share document returned by SearchDocs.
type RelayDoc struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Path     string `json:"path"`
	RelayURL string `json:"relay_url"`
}

type shareListResponse struct {
	WebFolderItems []webFolderItem `json:"web_folder_items"`
}

type webFolderItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // "folder" | "doc" | "canvas"
}

// SearchDocs fetches the TR share's folder listing and filters by q (case-insensitive substring).
// relayURL is the CP base URL (e.g. https://cp.tr.entire.vc).
// limit <= 0 means no limit.
//
// This is the slug path: GET /v1/web/shares/{slug} only serves web-published
// shares. A share that was never published has no slug at all — use
// SearchDocsByShareID for it.
func SearchDocs(ctx context.Context, relayURL, shareSlug, agentKey, q string, limit int) ([]RelayDoc, error) {
	endpoint := fmt.Sprintf("%s/v1/web/shares/%s", relayURL, url.PathEscape(shareSlug))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("teamrelay search: build request: %w", err)
	}
	req.Header.Set("X-Agent-Key", agentKey)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("teamrelay search: HTTP: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("teamrelay search: relay returned status %d", resp.StatusCode)
	}

	var shareResp shareListResponse
	if jsonErr := json.Unmarshal(body, &shareResp); jsonErr != nil {
		return nil, fmt.Errorf("teamrelay search: parse response: %w", jsonErr)
	}

	return filterDocs(shareResp.WebFolderItems, shareSlug, q, limit), nil
}

// webFilesIndexResponse is GET /v1/web/shares/{id}/files-index: a map of path
// to file metadata. Only the type is read here.
type webFilesIndexResponse struct {
	Files map[string]struct {
		Type string `json:"type"`
	} `json:"files"`
}

// SearchDocsByShareID lists a share by its UUID and filters by q, like
// SearchDocs.
//
// It calls GET /v1/web/shares/{shareID}/files-index, not the sync-protocol
// /v1/shares/{shareID}/files-index: the sync one only returns rows that are
// sync artifacts with a sha256, while this one returns every item of the
// share to an agent key. The UUID route skips the web_published gate, so it
// works for a share that was never published (and so has no slug). It needs a
// key with the literal `read` scope.
//
// linkHost is what goes after relay:// in each result — the slug when the
// integration still has one (links already pasted into tasks keep working),
// the share ID otherwise.
func SearchDocsByShareID(ctx context.Context, relayURL, shareID, linkHost, agentKey, q string, limit int) ([]RelayDoc, error) {
	if relayURL == "" {
		return nil, fmt.Errorf("teamrelay: relay URL not configured")
	}

	endpoint := fmt.Sprintf("%s/v1/web/shares/%s/files-index", strings.TrimRight(relayURL, "/"), url.PathEscape(shareID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("teamrelay search: build request: %w", err)
	}
	req.Header.Set("X-Agent-Key", agentKey)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes))

	if resp.StatusCode != http.StatusOK {
		return nil, classifySyncError("web files-index", shareID, resp.StatusCode, body)
	}

	var index webFilesIndexResponse
	if jsonErr := json.Unmarshal(body, &index); jsonErr != nil {
		return nil, fmt.Errorf("teamrelay search: parse files-index response: %w", jsonErr)
	}

	paths := make([]string, 0, len(index.Files))
	for p := range index.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	items := make([]webFolderItem, 0, len(paths))
	for _, p := range paths {
		items = append(items, webFolderItem{Name: path.Base(p), Path: p, Type: index.Files[p].Type})
	}
	return filterDocs(items, linkHost, q, limit), nil
}

// filterDocs drops folders, keeps items whose name or path contains q
// (case-insensitive) and stops at limit (<= 0: no limit). Never nil.
func filterDocs(items []webFolderItem, linkHost, q string, limit int) []RelayDoc {
	qLower := strings.ToLower(q)
	docs := []RelayDoc{}
	for _, item := range items {
		if item.Type == "folder" {
			continue
		}
		if qLower != "" &&
			!strings.Contains(strings.ToLower(item.Name), qLower) &&
			!strings.Contains(strings.ToLower(item.Path), qLower) {
			continue
		}
		docs = append(docs, RelayDoc{
			ID:       item.Path,
			Title:    item.Name,
			Path:     item.Path,
			RelayURL: "relay://" + linkHost + "/" + item.Path,
		})
		if limit > 0 && len(docs) >= limit {
			break
		}
	}
	return docs
}

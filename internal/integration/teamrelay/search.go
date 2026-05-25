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
}

// SearchDocs fetches the TR share's folder listing and filters by q (case-insensitive substring).
// relayURL is the CP base URL (e.g. https://cp.tr.entire.vc).
// shareID is used in relay_url construction; falls back to shareSlug when empty.
// limit <= 0 means no limit.
func SearchDocs(ctx context.Context, relayURL, shareSlug, shareID, agentKey, q string, limit int) ([]RelayDoc, error) {
	endpoint := fmt.Sprintf("%s/v1/web/shares/%s", relayURL, url.PathEscape(shareSlug))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("teamrelay search: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+agentKey)

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

	qualifier := shareID
	if qualifier == "" {
		qualifier = shareSlug
	}

	qLower := strings.ToLower(q)
	var docs []RelayDoc
	for _, item := range shareResp.WebFolderItems {
		if qLower != "" &&
			!strings.Contains(strings.ToLower(item.Name), qLower) &&
			!strings.Contains(strings.ToLower(item.Path), qLower) {
			continue
		}
		docs = append(docs, RelayDoc{
			ID:       item.Path,
			Title:    item.Name,
			Path:     item.Path,
			RelayURL: "relay://" + qualifier + "/" + item.Path,
		})
		if limit > 0 && len(docs) >= limit {
			break
		}
	}
	if docs == nil {
		docs = []RelayDoc{}
	}
	return docs, nil
}

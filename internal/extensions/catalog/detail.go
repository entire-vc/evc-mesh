package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Card is what an extension page shows from Spark's
// GET /mesh-extensions/{slug}: README, release history and the automatic
// checks of the latest release. It is display data only; nothing installable
// is read from it (the signed catalog is the only source for that).
type Card struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Readme      string        `json:"readme,omitempty"`
	Installs    int           `json:"installs"`
	Versions    []CardVersion `json:"versions"`
	Checks      *CardChecks   `json:"checks,omitempty"`
}

// CardVersion is one release row of the Versions tab.
type CardVersion struct {
	Version             string   `json:"version"`
	MeshAPI             string   `json:"mesh_api,omitempty"`
	CapabilitiesAdded   []string `json:"capabilities_added,omitempty"`
	CapabilitiesRemoved []string `json:"capabilities_removed,omitempty"`
	Yanked              bool     `json:"yanked,omitempty"`
	Blocked             bool     `json:"blocked,omitempty"`
}

// CardChecks summarises the latest release's checks the way the Checks tab
// reads them: passed, warnings or failed.
type CardChecks struct {
	Result string `json:"result"`
}

type sparkDetail struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Readme      *string `json:"readme"`
	Installs    int     `json:"installs"`
	Releases    []struct {
		Version string         `json:"version"`
		MeshAPI string         `json:"mesh_api"`
		Checks  map[string]any `json:"checks"`
		Delta   struct {
			Added   map[string][]string `json:"added"`
			Removed map[string][]string `json:"removed"`
		} `json:"capabilities_delta"`
		Yanked      bool    `json:"yanked"`
		BlockReason *string `json:"block_reason"`
	} `json:"releases"`
}

func flattenDelta(m map[string][]string) []string {
	var out []string
	for group, vals := range m {
		for _, v := range vals {
			out = append(out, group+":"+v)
		}
	}
	sort.Strings(out)
	return out
}

// checksResult folds Spark's per-check statuses into one word.
func checksResult(checks map[string]any) *CardChecks {
	if len(checks) == 0 {
		return nil
	}
	res := "passed"
	for _, v := range checks {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		switch m["status"] {
		case "fail":
			return &CardChecks{Result: "failed"}
		case "warn", "warning":
			res = "warnings"
		}
		if r, ok := m["reasons"].([]any); ok && len(r) > 0 && res == "passed" {
			res = "warnings"
		}
	}
	return &CardChecks{Result: res}
}

// Detail reads the display card of one extension from Spark.
func (c *Client) Detail(ctx context.Context, slug string) (*Card, error) {
	base := strings.TrimRight(strings.TrimSpace(c.baseURL()), "/")
	if base == "" {
		return nil, fmt.Errorf("spark url is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/mesh-extensions/"+url.PathEscape(slug), http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spark card answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}
	var d sparkDetail
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("spark card: %w", err)
	}
	card := &Card{ID: d.ID, Name: d.Name, Installs: d.Installs, Versions: []CardVersion{}}
	if d.Description != nil {
		card.Description = *d.Description
	}
	if d.Readme != nil {
		card.Readme = *d.Readme
	}
	for i := range d.Releases {
		r := &d.Releases[i]
		card.Versions = append(card.Versions, CardVersion{
			Version: r.Version, MeshAPI: r.MeshAPI, Yanked: r.Yanked, Blocked: r.BlockReason != nil,
			CapabilitiesAdded: flattenDelta(r.Delta.Added), CapabilitiesRemoved: flattenDelta(r.Delta.Removed),
		})
	}
	if len(d.Releases) > 0 {
		card.Checks = checksResult(d.Releases[0].Checks)
	}
	return card, nil
}

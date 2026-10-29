package teamrelay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This file is deliberately separate from sync_client.go, which carries its
// own header comment disclaiming the web-publish family (/v1/web/shares/...)
// entirely, and a source-level negative control
// (TestWriteBack_NeverUsesWebPublishFamily) enforcing that disclaimer against
// the write path specifically. DescribeAgentKey below is the one legitimate,
// read-only exception — Team Relay implemented the key self-describe route
// under its web router, not the sync-protocol one — and it belongs in its
// own file rather than carved out as an asterisk on a guard whose whole job
// is "nothing in here touches /v1/web/".

// AgentKeyDescription is what GET /v1/web/shares/{id}/agent-key returns about
// the key that called it — a fact from the source, not a claim someone typed
// into a form. ExpiresAt nil means the key was issued with no expiry at all,
// same meaning as domain.ProjectIntegration.KeyExpiresAt nil for "unknown" —
// this endpoint answering at all already proves the key is currently valid,
// so a nil here is "valid, no TTL set", not "we don't know".
type AgentKeyDescription struct {
	ID         string     `json:"id"`
	Label      *string    `json:"label"`
	ShareID    string     `json:"share_id"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// DescribeAgentKey calls GET /v1/web/shares/{shareID}/agent-key — the
// self-describe endpoint Team Relay shipped at Mesh's request specifically
// for this (evc-team-relay a PR): "requested by the Mesh
// integration, which holds a key issued by a share owner but has no way to
// learn the key's own lifetime today short of parsing a 403 after expiry".
//
// This hits /v1/web/shares/..., not /v1/shares/... — the WEB router, not the
// sync-protocol router — because that is where Team Relay implemented it (it
// reuses _resolve_share_agent_key, the same identity check every web-router
// route already runs). No scope is required to call it: a write-only key
// must be able to discover that it is write-only.
//
// A non-200 response is classified through the SAME classifySyncError as the
// sync-protocol calls in sync_client.go: the auth semantics (401
// missing/unrecognized key, 403 expired-or-foreign, distinguished by
// keyExpiredMessage) are identical because both routers resolve the key
// through the same underlying check.
func DescribeAgentKey(ctx context.Context, relayURL, shareID, agentKey string) (*AgentKeyDescription, error) {
	if relayURL == "" {
		return nil, fmt.Errorf("teamrelay: relay URL not configured")
	}

	endpoint := fmt.Sprintf("%s/v1/web/shares/%s/agent-key", strings.TrimRight(relayURL, "/"), url.PathEscape(shareID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("teamrelay: build agent-key request: %w", err)
	}
	req.Header.Set("X-Agent-Key", agentKey)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes))

	if resp.StatusCode != http.StatusOK {
		return nil, classifySyncError("agent-key", shareID, resp.StatusCode, body)
	}

	var parsed AgentKeyDescription
	if jsonErr := json.Unmarshal(body, &parsed); jsonErr != nil {
		return nil, fmt.Errorf("teamrelay: parse agent-key response: %w", jsonErr)
	}
	return &parsed, nil
}

// KeyShare is the share an agent key belongs to, as GET /v1/web/agent-key
// reports it. WebSlug is empty for a share that is not web-published.
type KeyShare struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	WebSlug string `json:"web_slug"`
}

// Name is how the share is shown to a person: the last segment of its path.
func (s KeyShare) Name() string {
	p := strings.Trim(s.Path, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// keyShareResponse is the body of GET /v1/web/agent-key; only the share is read.
type keyShareResponse struct {
	ShareID string    `json:"share_id"`
	Share   *KeyShare `json:"share"`
}

// ShareOfAgentKey asks the relay which share a key belongs to:
// GET /v1/web/agent-key, the share-less form of DescribeAgentKey. A key is
// bound to exactly one share, so a person pastes the key and never the share's
// UUID. A relay without the route answers 404 (ErrNotFound); the caller then
// falls back to a share it already knows.
func ShareOfAgentKey(ctx context.Context, relayURL, agentKey string) (*KeyShare, error) {
	if relayURL == "" {
		return nil, fmt.Errorf("teamrelay: relay URL not configured")
	}
	endpoint := strings.TrimRight(relayURL, "/") + "/v1/web/agent-key"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("teamrelay: build agent-key request: %w", err)
	}
	req.Header.Set("X-Agent-Key", agentKey)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes))

	if resp.StatusCode != http.StatusOK {
		return nil, classifySyncError("agent-key", "", resp.StatusCode, body)
	}
	var parsed keyShareResponse
	if jsonErr := json.Unmarshal(body, &parsed); jsonErr != nil {
		return nil, fmt.Errorf("teamrelay: parse agent-key response: %w", jsonErr)
	}
	share := KeyShare{ID: parsed.ShareID}
	if parsed.Share != nil {
		share = *parsed.Share
		if share.ID == "" {
			share.ID = parsed.ShareID
		}
	}
	if share.ID == "" {
		return nil, fmt.Errorf("teamrelay: agent-key response names no share")
	}
	return &share, nil
}

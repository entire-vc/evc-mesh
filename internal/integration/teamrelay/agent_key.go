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
// for this (evc-team-relay PR #230, #bc11d499): "requested by the Mesh
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

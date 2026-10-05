package teamrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// relayHTTPClient is a package-level HTTP client shared across transport calls.
// 30 s timeout; relay is local/private so this is generous.
var relayHTTPClient = &http.Client{Timeout: 30 * time.Second}

// probeHTTPClient is the confirm-probe client. Unlike relayHTTPClient it never
// follows redirects: an auth wall that bounces anonymous requests 302 →
// login-page-200 must fail the 2xx check instead of passing it by landing on
// the login page. The transport client keeps the default redirect policy — sync
// calls may legitimately rely on it (trailing-slash/https redirects on the base URL).
var probeHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// syncUploadResponse is the success body returned by POST /v1/web/shares/{id}/sync-upload.
type syncUploadResponse struct {
	SyncURL string `json:"sync_url"`
	WebURL  string `json:"web_url"`
	Path    string `json:"path"`
}

// Publisher is the interface consumed by the artifact upload hook.
// Publish returns the artifact's public URL only when publication is confirmed —
// the relay accepted the upload and the URL serves to an anonymous browser; ""
// otherwise (share not web-published, private visibility, unreachable, or the
// confirm probe failing). The agent key used for the upload is returned alongside
// (empty for public shares); err carries transport-level failures.
type Publisher interface {
	Publish(ctx context.Context, taskID uuid.UUID, artifactName string, content []byte, contentType string) (publicURL, agentKey string, err error)
}

type client struct {
	piRepo   repository.ProjectIntegrationRepository
	taskRepo repository.TaskRepository
	projRepo repository.ProjectRepository
}

// NewClient creates a new Team Relay publisher client.
func NewClient(piRepo repository.ProjectIntegrationRepository, taskRepo repository.TaskRepository, projRepo repository.ProjectRepository) Publisher {
	return &client{piRepo: piRepo, taskRepo: taskRepo, projRepo: projRepo}
}

func transportEnabled() bool {
	return os.Getenv("MESH_TEAMRELAY_TRANSPORT_ENABLED") == "true"
}

// Publish pushes an artifact to the Team Relay share for the project the task belongs to.
// It is best-effort: integration lookup failures are logged and yield ("", "", nil),
// while transport-level upload failures are returned as err. A non-empty publicURL is
// returned only when publication is confirmed: the relay accepted the upload AND the
// returned web URL serves to an anonymous browser (see confirmPublishedWebURL). The
// agent key is returned for callers that need authenticated access to the share.
func (c *client) Publish(ctx context.Context, taskID uuid.UUID, artifactName string, content []byte, contentType string) (publicURL, agentKey string, err error) {
	// Resolve task → project.
	task, tErr := c.taskRepo.GetByID(ctx, taskID)
	if tErr != nil || task == nil {
		return "", "", nil // best-effort; task gone or DB error
	}

	pi, pErr := c.piRepo.Get(ctx, task.ProjectID, "team_relay")
	if pErr != nil || pi == nil || !pi.Enabled {
		return "", "", nil // integration absent or disabled — silent no-op
	}

	var settings domain.TeamRelaySettings
	if uErr := json.Unmarshal(pi.Settings, &settings); uErr != nil {
		log.Printf("teamrelay: bad settings for project %s: %v", task.ProjectID, uErr)
		return "", "", nil
	}

	proj, prErr := c.projRepo.GetByID(ctx, task.ProjectID)
	if prErr != nil || proj == nil {
		return "", "", nil
	}

	taskShortID := taskID.String()[:8]
	filePath := BuildPath(
		settings.Subfolder,
		proj.Slug,
		settings.IncludeProjectSlug,
		taskShortID,
		artifactName,
		contentType,
		time.Now(),
	)

	if !transportEnabled() {
		log.Printf("teamrelay: would publish to relay: %s (transport disabled)", filePath)
		return "", "", nil
	}

	// Prefer ShareID (UUID) over ShareSlug — UUID-based path skips the web_published gate
	// on the relay, enabling uploads to private sync folders. Fall back to slug for
	// backward-compat with integrations configured before ShareID was introduced.
	shareIdentifier := settings.ShareID
	if shareIdentifier == "" {
		shareIdentifier = settings.ShareSlug
	}

	webURL, tErr := transport(ctx, shareIdentifier, filePath, content, contentType, pi.AgentKey)
	if tErr != nil {
		return "", "", tErr
	}
	if webURL == "" {
		// No web URL: the share is not web-published (or the relay rejected the
		// key). Nothing the caller may advertise as publicly openable.
		return "", pi.AgentKey, nil
	}
	// The control plane builds a web URL for any web_published share, including
	// ones with private visibility that answer 401 to anonymous browsers — so the
	// URL alone is a claim, not a confirmation. Only a URL that serves anonymously
	// counts as published.
	if !confirmPublishedWebURL(ctx, webURL) {
		return "", pi.AgentKey, nil
	}
	return webURL, pi.AgentKey, nil
}

// confirmProbeAttempts bounds how many times the confirm probe is tried before
// a web URL is declared unpublished. The probe fires immediately after the relay
// accepted the upload (2xx), and web-side propagation can lag that acceptance —
// a momentary 404 right after a successful upload is usually the file not being
// visible yet, not a private share. Two quick retries cut those false negatives;
// anything still not 2xx after the last attempt stays unconfirmed.
const confirmProbeAttempts = 3

// confirmProbeBackoff is the wait before probe retry N (N × backoff). A var so
// tests can shorten it instead of sleeping real time.
var confirmProbeBackoff = 250 * time.Millisecond

// confirmPublishedWebURL reports whether webURL actually serves to an anonymous
// browser. A plain credential-less GET is the same probe a user's browser makes
// when clicking the link: private shares answer 401 (or a redirect to a login
// page), not-yet-synced files 404, and anything but a final 2xx means the
// publication is not confirmed and the caller must not persist the URL as
// publicly openable.
func confirmPublishedWebURL(ctx context.Context, webURL string) bool {
	for attempt := 1; ; attempt++ {
		final := attempt == confirmProbeAttempts
		if confirmProbeOnce(ctx, webURL, final) {
			return true
		}
		if final {
			return false
		}
		select {
		case <-ctx.Done():
			log.Printf("teamrelay: confirm probe cancelled for %s: %v", webURL, ctx.Err())
			return false
		case <-time.After(confirmProbeBackoff * time.Duration(attempt)):
		}
	}
}

// confirmProbeOnce performs one anonymous, credential-less GET against webURL.
// Only the final attempt logs its failure — retries are expected while the web
// side catches up with the just-accepted upload.
func confirmProbeOnce(ctx context.Context, webURL string, finalAttempt bool) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, webURL, http.NoBody)
	if err != nil {
		log.Printf("teamrelay: cannot build confirm request for %s: %v", webURL, err)
		return false
	}
	resp, err := probeHTTPClient.Do(req)
	if err != nil {
		if finalAttempt {
			log.Printf("teamrelay: confirm fetch failed for %s: %v — treating as unpublished", webURL, err)
		}
		return false
	}
	defer resp.Body.Close()
	// Drain a little so the connection can be reused instead of torn down mid-stream.
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if finalAttempt {
			log.Printf("teamrelay: %s answered %d to an anonymous request — publication not confirmed, tr_public_url will not be written", webURL, resp.StatusCode)
		}
		return false
	}
	return true
}

// webShareResponse is the body returned by GET /v1/web/shares/{slug}.
type webShareResponse struct {
	WebFolderItems []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	} `json:"web_folder_items"`
}

// ListShareFiles calls GET /v1/web/shares/{shareSlug} and returns the file list.
// Returns nil slice (not error) when the share is not found (caller maps to 404).
func ListShareFiles(ctx context.Context, shareSlug, agentKey string) ([]domain.RelayFileItem, error) {
	baseURL := os.Getenv("MESH_TEAMRELAY_RELAY_URL")
	if baseURL == "" {
		return nil, fmt.Errorf("teamrelay: MESH_TEAMRELAY_RELAY_URL not set")
	}

	endpoint := fmt.Sprintf("%s/v1/web/shares/%s", baseURL, url.PathEscape(shareSlug))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("teamrelay: build list request: %w", err)
	}
	req.Header.Set("X-Agent-Key", agentKey)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("teamrelay: list files HTTP: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		var parsed webShareResponse
		if jsonErr := json.Unmarshal(body, &parsed); jsonErr != nil {
			return nil, fmt.Errorf("teamrelay: parse share response: %w", jsonErr)
		}
		items := make([]domain.RelayFileItem, 0, len(parsed.WebFolderItems))
		for _, it := range parsed.WebFolderItems {
			items = append(items, domain.RelayFileItem{Name: it.Name, Path: it.Path})
		}
		return items, nil
	case http.StatusNotFound:
		return nil, nil // share not found — caller maps to 404
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("teamrelay: agent key rejected (status %d) for share %s", resp.StatusCode, shareSlug)
	default:
		return nil, fmt.Errorf("teamrelay: unexpected status %d listing share %s: %s", resp.StatusCode, shareSlug, body)
	}
}

// transport POSTs artifact bytes to the relay upload endpoint.
// Contract (locked 2026-05-22, relay PR #2 commit 7b3f395):
//
//	POST /v1/web/shares/{shareIdentifier}/sync-upload?path=<urlenc>
//	shareIdentifier: UUID (shares.id = folder GUID) or slug (web-published only)
//	X-Agent-Key: tr_agent_<48hex>
//	Content-Type: <mime>
//	Body: raw bytes
//	200 → {sync_url, web_url, path}  — relay writes source=sync-artifact + bumps web_content_updated_at
//	401/403 → key revoked; log + return nil (no retry — key is static)
func transport(ctx context.Context, shareIdentifier, filePath string, content []byte, contentType, agentKey string) (string, error) {
	baseURL := os.Getenv("MESH_TEAMRELAY_RELAY_URL")
	if baseURL == "" {
		log.Printf("teamrelay: MESH_TEAMRELAY_RELAY_URL not set — cannot upload %s", filePath)
		return "", nil
	}

	endpoint := fmt.Sprintf("%s/v1/web/shares/%s/sync-upload?path=%s",
		baseURL,
		url.PathEscape(shareIdentifier),
		url.QueryEscape(filePath),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(content))
	if err != nil {
		log.Printf("teamrelay: failed to build request for %s: %v", filePath, err)
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Agent-Key", agentKey)

	resp, err := relayHTTPClient.Do(req)
	if err != nil {
		log.Printf("teamrelay: HTTP error uploading %s: %v", filePath, err)
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		var result syncUploadResponse
		if jsonErr := json.Unmarshal(body, &result); jsonErr != nil {
			log.Printf("teamrelay: sync-upload parse error for %s: %v (body: %s)", filePath, jsonErr, body)
			return "", fmt.Errorf("teamrelay: failed to parse sync-upload response: %w", jsonErr)
		}
		if result.Path == "" {
			log.Printf("teamrelay: sync-upload returned empty path for %s — relay may have rejected the upload (body: %s)", filePath, body)
			return "", fmt.Errorf("teamrelay: sync-upload returned unexpected response (empty path): %s", body)
		}
		log.Printf("teamrelay: sync-uploaded %s → relay path=%s", filePath, result.Path)
		return result.WebURL, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		log.Printf("teamrelay: agent key rejected by relay (status %d) for share %s — integration key may be revoked", resp.StatusCode, shareIdentifier)
		return "", nil
	default:
		log.Printf("teamrelay: unexpected relay status %d uploading %s: %s", resp.StatusCode, filePath, body)
		return "", fmt.Errorf("relay sync-upload returned status %d", resp.StatusCode)
	}
}

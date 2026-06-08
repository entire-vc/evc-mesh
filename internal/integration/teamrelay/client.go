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

// syncUploadResponse is the success body returned by POST /v1/web/shares/{id}/sync-upload.
type syncUploadResponse struct {
	SyncURL string `json:"sync_url"`
	WebURL  string `json:"web_url"`
	Path    string `json:"path"`
}

// Publisher is the interface consumed by the artifact upload hook.
// Publish returns the relay-reported public URL (or "" if none), the agent key used
// for the upload (so callers can persist it for authenticated open URLs), and a
// best-effort error.
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
// It is best-effort: all errors are logged but never returned to the caller.
// On a successful upload it returns the relay-reported public URL (or "" if the relay
// omits one) and the agent key used — the caller persists both into artifact metadata so
// the UI can construct an authenticated open URL without a server round-trip.
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
		task.Title,
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
	return webURL, pi.AgentKey, tErr
}

// transport POSTs artifact bytes to the relay sync-upload endpoint and returns the
// relay-reported web URL on success (empty string if the relay omits one).
// Contract (ADR-0045):
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
		if jsonErr := json.Unmarshal(body, &result); jsonErr == nil {
			log.Printf("teamrelay: sync-uploaded %s → relay path=%s", filePath, result.Path)
			return result.WebURL, nil
		}
		log.Printf("teamrelay: sync-uploaded %s (relay response: %s)", filePath, body)
		return "", nil
	case http.StatusUnauthorized, http.StatusForbidden:
		log.Printf("teamrelay: agent key rejected by relay (status %d) for share %s — integration key may be revoked", resp.StatusCode, shareIdentifier)
		return "", nil
	default:
		log.Printf("teamrelay: unexpected relay status %d uploading %s: %s", resp.StatusCode, filePath, body)
		return "", fmt.Errorf("relay sync-upload returned status %d", resp.StatusCode)
	}
}

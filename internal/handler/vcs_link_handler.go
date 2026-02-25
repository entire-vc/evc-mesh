package handler

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// VCSLinkHandler handles HTTP requests for VCS link management.
type VCSLinkHandler struct {
	vcsService service.VCSLinkService
}

// NewVCSLinkHandler creates a new VCSLinkHandler.
func NewVCSLinkHandler(svc service.VCSLinkService) *VCSLinkHandler {
	return &VCSLinkHandler{vcsService: svc}
}

// createVCSLinkRequest is the JSON body for creating a VCS link.
type createVCSLinkRequest struct {
	Provider   string `json:"provider"`
	LinkType   string `json:"link_type"`
	ExternalID string `json:"external_id"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Status     string `json:"status"`
}

// Create handles POST /tasks/:task_id/vcs-links
func (h *VCSLinkHandler) Create(c echo.Context) error {
	taskID, err := uuid.Parse(c.Param("task_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	var req createVCSLinkRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid request body"))
	}

	if req.URL == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("url is required"))
	}
	if req.LinkType == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("link_type is required"))
	}
	if req.ExternalID == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("external_id is required"))
	}

	provider := domain.VCSProvider(req.Provider)
	if provider == "" {
		provider = domain.VCSProviderGitHub
	}

	// Validate provider.
	switch provider {
	case domain.VCSProviderGitHub, domain.VCSProviderGitLab:
	default:
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("unsupported provider: "+string(provider)))
	}

	// Validate link_type.
	linkType := domain.VCSLinkType(req.LinkType)
	switch linkType {
	case domain.VCSLinkTypePR, domain.VCSLinkTypeCommit, domain.VCSLinkTypeBranch:
	default:
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("unsupported link_type: "+req.LinkType))
	}

	input := domain.CreateVCSLinkInput{
		TaskID:     taskID,
		Provider:   provider,
		LinkType:   linkType,
		ExternalID: req.ExternalID,
		URL:        req.URL,
		Title:      req.Title,
		Status:     domain.VCSLinkStatus(req.Status),
	}

	link, err := h.vcsService.Create(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	return c.JSON(http.StatusCreated, link)
}

// List handles GET /tasks/:task_id/vcs-links
func (h *VCSLinkHandler) List(c echo.Context) error {
	taskID, err := uuid.Parse(c.Param("task_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid task_id"))
	}

	links, err := h.vcsService.ListByTask(c.Request().Context(), taskID)
	if err != nil {
		return handleError(c, err)
	}

	if links == nil {
		links = []domain.VCSLink{}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"vcs_links": links,
		"count":     len(links),
	})
}

// Delete handles DELETE /vcs-links/:link_id
func (h *VCSLinkHandler) Delete(c echo.Context) error {
	linkID, err := uuid.Parse(c.Param("link_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid link_id"))
	}

	if err := h.vcsService.Delete(c.Request().Context(), linkID); err != nil {
		return handleError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

// GitHubWebhookPayload holds the fields we care about from a GitHub webhook event.
type GitHubWebhookPayload struct {
	Action      string             `json:"action"`
	PullRequest *gitHubPRPayload   `json:"pull_request"`
	HeadCommit  *gitHubCommitInfo  `json:"head_commit"`
	Repository  gitHubRepoPayload  `json:"repository"`
	Ref         string             `json:"ref"` // push events: refs/heads/branch-name
}

type gitHubPRPayload struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
}

type gitHubCommitInfo struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	URL     string `json:"url"`
}

type gitHubRepoPayload struct {
	FullName string `json:"full_name"`
	HTMLURL  string `json:"html_url"`
}

// GitHubWebhook handles POST /webhooks/github — receives GitHub webhook events.
// It auto-links tasks when commit messages or PR titles contain MESH-{task_id_prefix}.
func (h *VCSLinkHandler) GitHubWebhook(c echo.Context) error {
	event := c.Request().Header.Get("X-GitHub-Event")
	if event == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("missing X-GitHub-Event header"))
	}

	var payload GitHubWebhookPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid payload"))
	}

	ctx := c.Request().Context()

	switch event {
	case "pull_request":
		if payload.PullRequest == nil {
			return c.JSON(http.StatusOK, map[string]string{"status": "ignored"})
		}
		pr := payload.PullRequest
		taskID := extractMeshTaskID(pr.Title)
		if taskID == uuid.Nil {
			return c.JSON(http.StatusOK, map[string]string{"status": "no_task_ref"})
		}
		status := domain.VCSLinkStatus(pr.State)
		if pr.Merged {
			status = domain.VCSLinkStatusMerged
		}
		input := domain.CreateVCSLinkInput{
			TaskID:     taskID,
			Provider:   domain.VCSProviderGitHub,
			LinkType:   domain.VCSLinkTypePR,
			ExternalID: itoa(pr.Number),
			URL:        pr.HTMLURL,
			Title:      pr.Title,
			Status:     status,
		}
		if _, err := h.vcsService.Create(ctx, input); err != nil {
			// Log but don't fail the webhook response.
			c.Logger().Errorf("github webhook: create vcs link: %v", err)
		}

	case "push":
		if payload.HeadCommit == nil {
			return c.JSON(http.StatusOK, map[string]string{"status": "ignored"})
		}
		commit := payload.HeadCommit
		taskID := extractMeshTaskID(commit.Message)
		if taskID == uuid.Nil {
			return c.JSON(http.StatusOK, map[string]string{"status": "no_task_ref"})
		}
		sha := commit.ID
		if len(sha) > 12 {
			sha = sha[:12]
		}
		input := domain.CreateVCSLinkInput{
			TaskID:     taskID,
			Provider:   domain.VCSProviderGitHub,
			LinkType:   domain.VCSLinkTypeCommit,
			ExternalID: commit.ID,
			URL:        commit.URL,
			Title:      firstLine(commit.Message),
		}
		if _, err := h.vcsService.Create(ctx, input); err != nil {
			c.Logger().Errorf("github webhook: create vcs link: %v", err)
		}
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// extractMeshTaskID looks for a MESH-{uuid_prefix} pattern in the text
// and returns the task UUID if found. Returns uuid.Nil if not found.
func extractMeshTaskID(text string) uuid.UUID {
	// Look for MESH- followed by at least 8 hex chars (UUID prefix)
	const prefix = "MESH-"
	idx := strings.Index(text, prefix)
	if idx == -1 {
		return uuid.Nil
	}
	rest := text[idx+len(prefix):]
	// Take up to 36 chars (full UUID with dashes)
	end := len(rest)
	if end > 36 {
		end = 36
	}
	candidate := rest[:end]
	// Strip non-UUID chars at the end.
	for i, ch := range candidate {
		if !isHexOrDash(ch) {
			candidate = candidate[:i]
			break
		}
	}
	id, err := uuid.Parse(candidate)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func isHexOrDash(ch rune) bool {
	return (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F') || ch == '-'
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx != -1 {
		return s[:idx]
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 12)
	if n < 0 {
		buf = append(buf, '-')
		n = -n
	}
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}


package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// webhookDeliveryTTL is how long X-GitHub-Delivery keys are kept in the
// dedup store. GitHub retries failed deliveries for up to ~24h, so a week is
// a comfortable margin to absorb every retry without remembering forever.
const webhookDeliveryTTL = 7 * 24 * time.Hour

// WebhookDedupStore is the minimum surface the GitHub webhook handler needs
// from a dedup backend. Implemented by Redis in prod and by an in-memory
// map in tests.
type WebhookDedupStore interface {
	// Claim records the delivery_id as seen. Returns true if this call was
	// the first to record it (i.e. the request is fresh and should be
	// processed), false if a previous call already recorded it (duplicate).
	Claim(ctx context.Context, deliveryID string) (bool, error)
}

// redisWebhookDedupStore is a Redis-backed WebhookDedupStore.
type redisWebhookDedupStore struct {
	rdb *redis.Client
}

// NewRedisWebhookDedupStore wraps a Redis client as a WebhookDedupStore.
func NewRedisWebhookDedupStore(rdb *redis.Client) WebhookDedupStore {
	return &redisWebhookDedupStore{rdb: rdb}
}

// Claim uses SET ... NX ... EX to record the delivery key with a TTL.
// Returns true if the key was newly set (fresh delivery), false if it was
// already present (duplicate). The `SET ... NX` modifier is the modern
// equivalent of the deprecated SETNX command.
func (s *redisWebhookDedupStore) Claim(ctx context.Context, deliveryID string) (bool, error) {
	if s.rdb == nil {
		return true, nil
	}
	key := "mesh:webhook:gh:delivery:" + deliveryID
	res, err := s.rdb.SetArgs(ctx, key, "1", redis.SetArgs{Mode: "NX", TTL: webhookDeliveryTTL}).Result()
	if err != nil {
		// redis.Nil here means NX rejected the set — duplicate, not a real error.
		if errors.Is(err, redis.Nil) {
			return false, nil
		}
		return false, err
	}
	// SetArgs returns "OK" on success.
	return res == "OK", nil
}

// VCSLinkHandler handles HTTP requests for VCS link management.
type VCSLinkHandler struct {
	vcsService          service.VCSLinkService
	githubWebhookSecret string                          // legacy static HMAC-SHA256 secret; used only when vcsIntegrations is nil. Empty disables validation.
	gitlabWebhookSecret string                          // legacy static shared token; used only when vcsIntegrations is nil. Empty disables validation.
	vcsIntegrations     *service.VCSIntegrationResolver // resolves secrets fresh on every request (§C1/§C2) — see WithVCSIntegrationResolver
	dedup               WebhookDedupStore
}

// VCSLinkHandlerOption configures optional dependencies on VCSLinkHandler.
type VCSLinkHandlerOption func(*VCSLinkHandler)

// WithGitHubWebhookSecret enables HMAC validation against the given secret.
func WithGitHubWebhookSecret(secret string) VCSLinkHandlerOption {
	return func(h *VCSLinkHandler) { h.githubWebhookSecret = secret }
}

// WithGitLabWebhookSecret enables X-Gitlab-Token validation against the
// given secret. Unlike GitHub's HMAC-signed body, GitLab webhooks carry the
// shared secret verbatim in a header — validation is a direct (constant-
// time) compare, not a signature check.
func WithGitLabWebhookSecret(secret string) VCSLinkHandlerOption {
	return func(h *VCSLinkHandler) { h.gitlabWebhookSecret = secret }
}

// WithWebhookDedupStore wires the dedup store used to drop duplicate
// X-GitHub-Delivery values before any HMAC or JSON work happens.
func WithWebhookDedupStore(store WebhookDedupStore) VCSLinkHandlerOption {
	return func(h *VCSLinkHandler) { h.dedup = store }
}

// WithVCSIntegrationResolver switches webhook secret validation from the
// static WithGitHubWebhookSecret/WithGitLabWebhookSecret values (resolved
// once, at process start) to a fresh per-request lookup against the
// workspace integration store, falling back to env exactly as those static
// options did (§C1/§C2 of specsintegration-provider-contract, #33a4bb57).
// Takes priority over the static options when both are set. Unlike the
// static path — where an empty secret means "validation off" — once this
// resolver is wired, resolving NO secret at all (neither any active
// workspace row nor env) means the provider is DISABLED and the request is
// refused with a named reason, not silently accepted. See
// VCSIntegrationResolver's doc comment for why the webhook path validates
// against every active workspace's secret rather than one workspace's.
func WithVCSIntegrationResolver(r *service.VCSIntegrationResolver) VCSLinkHandlerOption {
	return func(h *VCSLinkHandler) { h.vcsIntegrations = r }
}

// githubWebhookSecrets returns the set of secrets that should currently
// validate an inbound GitHub webhook, and whether validation is mandatory
// at all. When vcsIntegrations is wired, validation is ALWAYS mandatory —
// an empty secret set means the provider is disabled and the caller must
// refuse, never fall through to "no secret = allow". When vcsIntegrations
// is nil, this reproduces the pre-#33a4bb57 static behavior exactly:
// validation is mandatory only when a secret was configured at
// construction.
func (h *VCSLinkHandler) githubWebhookSecrets(ctx context.Context) (secrets []string, required bool) {
	if h.vcsIntegrations != nil {
		secrets, _ := h.vcsIntegrations.GitHubWebhookSecrets(ctx)
		return secrets, true
	}
	if h.githubWebhookSecret != "" {
		return []string{h.githubWebhookSecret}, true
	}
	return nil, false
}

// gitlabWebhookSecrets is githubWebhookSecrets's GitLab counterpart.
func (h *VCSLinkHandler) gitlabWebhookSecrets(ctx context.Context) (secrets []string, required bool) {
	if h.vcsIntegrations != nil {
		secrets, _ := h.vcsIntegrations.GitLabWebhookSecrets(ctx)
		return secrets, true
	}
	if h.gitlabWebhookSecret != "" {
		return []string{h.gitlabWebhookSecret}, true
	}
	return nil, false
}

// NewVCSLinkHandler creates a new VCSLinkHandler.
func NewVCSLinkHandler(svc service.VCSLinkService, opts ...VCSLinkHandlerOption) *VCSLinkHandler {
	h := &VCSLinkHandler{vcsService: svc}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// NewVCSLinkHandlerWithSecret is preserved for backward compatibility with the
// existing main.go wiring. Prefer NewVCSLinkHandler(svc, WithGitHubWebhookSecret(...)).
func NewVCSLinkHandlerWithSecret(svc service.VCSLinkService, githubWebhookSecret string) *VCSLinkHandler {
	return NewVCSLinkHandler(svc, WithGitHubWebhookSecret(githubWebhookSecret))
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
	if err = c.Bind(&req); err != nil {
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

	// Validate link_type. Accepts the alias "pull_request" and any casing;
	// only the canonical value reaches the store.
	linkType, ok := domain.ParseVCSLinkType(req.LinkType)
	if !ok {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest(
			"unsupported link_type "+strconv.Quote(req.LinkType)+
				": expected one of "+strings.Join(domain.VCSLinkTypeNames, ", ")))
	}

	// Validate status the same way as link_type: empty means "let the
	// service pick a default", anything else must be a known value. Without
	// this, a typo (or a stale caller sending the pre-alias "pull_request"
	// spelling into the wrong field) would silently store an unrecognised
	// status string that the done-evidence gate's Status != merged check
	// then treats as indistinguishable from "open" — no error, wrong state.
	linkStatus, ok := domain.ParseVCSLinkStatus(req.Status)
	if !ok {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest(
			"unsupported status "+strconv.Quote(req.Status)+
				": expected one of "+strings.Join(domain.VCSLinkStatusNames, ", ")))
	}

	input := domain.CreateVCSLinkInput{
		TaskID:     taskID,
		Provider:   provider,
		LinkType:   linkType,
		ExternalID: req.ExternalID,
		URL:        req.URL,
		Title:      req.Title,
		Status:     linkStatus,
	}

	link, created, err := h.vcsService.Create(c.Request().Context(), input)
	if err != nil {
		return handleError(c, err)
	}

	// The upsert path (explicit status on an existing link) updates a row
	// rather than creating one — 201 there would claim a fresh row exists
	// when it doesn't, reading as a duplicate to any caller that trusts the
	// echo instead of re-fetching (#b73171fa).
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	return c.JSON(status, link)
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
	Action      string            `json:"action"`
	PullRequest *gitHubPRPayload  `json:"pull_request"`
	HeadCommit  *gitHubCommitInfo `json:"head_commit"`
	Repository  gitHubRepoPayload `json:"repository"`
	Ref         string            `json:"ref"` // push events: refs/heads/branch-name
}

type gitHubPRPayload struct {
	Number         int              `json:"number"`
	Title          string           `json:"title"`
	Body           string           `json:"body"`
	HTMLURL        string           `json:"html_url"`
	State          string           `json:"state"`
	Merged         bool             `json:"merged"`
	MergeCommitSHA string           `json:"merge_commit_sha"`
	Head           gitHubRefPayload `json:"head"`
}

// gitHubRefPayload carries the source branch of a pull request. Worth parsing
// because a branch cut from a task usually carries its id even when the author
// wrote nothing task-shaped in the title or body.
type gitHubRefPayload struct {
	Ref string `json:"ref"`
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

// GitHubWebhook handles POST /webhooks/github and the canonical alias
// POST /api/v1/integrations/github/webhook. It receives GitHub webhook
// events, deduplicates by X-GitHub-Delivery, validates the HMAC-SHA256
// signature when a secret is configured, and for pull_request events
// delegates to the service orchestrator (which manages link upsert + task
// transition policy). For push events it preserves the existing
// commit-linking behaviour.
func (h *VCSLinkHandler) GitHubWebhook(c echo.Context) error {
	event := c.Request().Header.Get("X-GitHub-Event")
	if event == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("missing X-GitHub-Event header"))
	}

	deliveryID := c.Request().Header.Get("X-GitHub-Delivery")
	if deliveryID == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("missing X-GitHub-Delivery header"))
	}

	// Dedup before HMAC: GitHub retries failed deliveries (incl. 5xx) and
	// guarantees X-GitHub-Delivery is unique per delivery attempt. Short-
	// circuiting here means a flapping handler can't accidentally apply the
	// same transition twice.
	if h.dedup != nil {
		fresh, err := h.dedup.Claim(c.Request().Context(), deliveryID)
		if err != nil {
			// On dedup-store failure, fall through (better to risk a duplicate
			// than to refuse all webhooks when Redis is down). Log via Echo.
			c.Logger().Errorf("webhook dedup claim error delivery=%s: %v", deliveryID, err)
		} else if !fresh {
			return c.JSON(http.StatusOK, map[string]string{"status": "duplicate"})
		}
	}

	// Read the raw body so we can (a) verify the HMAC signature and (b) decode JSON.
	rawBody, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("failed to read request body"))
	}

	// Validate HMAC-SHA256 signature. secrets is every value currently
	// allowed to sign a delivery (§C1: resolved fresh on every request, not
	// cached from process start); required tells us whether "no secret
	// resolved" means "validation is off" (legacy static path, no
	// vcsIntegrations wired) or "the provider is disabled" (dynamic path —
	// see githubWebhookSecrets's doc comment).
	secrets, required := h.githubWebhookSecrets(c.Request().Context())
	if required {
		if len(secrets) == 0 {
			return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("github integration is disabled: no active workspace webhook secret and no MESH_GITHUB_WEBHOOK_SECRET fallback configured"))
		}
		sig := c.Request().Header.Get("X-Hub-Signature-256")
		if sig == "" {
			return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("missing X-Hub-Signature-256 header"))
		}
		matched := false
		for _, secret := range secrets {
			if verifyGitHubSignature(rawBody, sig, secret) {
				matched = true
				break
			}
		}
		if !matched {
			return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("invalid webhook signature"))
		}
	}

	var payload GitHubWebhookPayload
	if jsonErr := decodeJSON(rawBody, &payload); jsonErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid payload"))
	}

	ctx := c.Request().Context()

	switch event {
	case "pull_request":
		if payload.PullRequest == nil {
			return c.JSON(http.StatusOK, map[string]string{"status": "ignored"})
		}
		pr := payload.PullRequest
		ev := service.GitHubWebhookEvent{
			Action:     payload.Action,
			PRNumber:   pr.Number,
			PRTitle:    pr.Title,
			PRBody:     pr.Body,
			PRHTMLURL:  pr.HTMLURL,
			PRState:    pr.State,
			PRMerged:   pr.Merged,
			MergeSHA:   pr.MergeCommitSHA,
			PRBranch:   pr.Head.Ref,
			Repository: payload.Repository.FullName,
		}
		result, herr := h.vcsService.HandleGitHubPullRequestEvent(ctx, ev)
		if herr != nil {
			c.Logger().Errorf("github webhook: pull_request handler: %v", herr)
			return c.JSON(http.StatusOK, map[string]string{"status": "error_logged"})
		}
		return c.JSON(http.StatusOK, map[string]any{
			"status":       "ok",
			"task_id":      result.TaskID,
			"transitioned": result.Transitioned,
			"reason":       result.Reason,
			"old_status":   result.OldStatus,
			"new_status":   result.NewStatus,
		})

	case "push":
		if payload.HeadCommit == nil {
			return c.JSON(http.StatusOK, map[string]string{"status": "ignored"})
		}
		commit := payload.HeadCommit
		// Same recognition as the pull_request path: a commit message carries
		// "Refs #<short id>" far more often than MESH-<uuid>, and the branch
		// (payload.Ref, e.g. refs/heads/linus/<id>-slug) is another free signal.
		taskID, matched := h.vcsService.ResolveTaskRef(ctx,
			service.TaskRefSource{Name: "body", Text: commit.Message},
			service.TaskRefSource{Name: "branch", Text: strings.TrimPrefix(payload.Ref, "refs/heads/")},
		)
		if taskID == uuid.Nil {
			// firstLine is bounded only by the next newline, so a long
			// single-line commit message would land in the journal whole.
			c.Logger().Infof("github webhook: push %s no_task_ref: commit=%s msg=%q ref=%q",
				payload.Repository.FullName, shortCommitSHA(commit.ID),
				service.TruncateForLog(commit.Message, 160), payload.Ref)
			return c.JSON(http.StatusOK, map[string]string{"status": "no_task_ref"})
		}
		c.Logger().Infof("github webhook: push %s resolved task=%s via=%s",
			payload.Repository.FullName, taskID, matched.Kind)
		input := domain.CreateVCSLinkInput{
			TaskID:     taskID,
			Provider:   domain.VCSProviderGitHub,
			LinkType:   domain.VCSLinkTypeCommit,
			ExternalID: commit.ID,
			URL:        commit.URL,
			Title:      firstLine(commit.Message),
		}
		if _, _, err := h.vcsService.Create(ctx, input); err != nil {
			c.Logger().Errorf("github webhook: create vcs link: %v", err)
		}
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// GitLabWebhookPayload holds the fields we care about from a GitLab
// "Merge Request Hook" webhook event. GitLab's payload shape differs from
// GitHub's (object_attributes nesting, path_with_namespace instead of
// full_name, iid instead of a global number) — kept as its own struct
// rather than shoehorned into GitHubWebhookPayload.
type GitLabWebhookPayload struct {
	ObjectKind       string               `json:"object_kind"`
	Project          gitLabProjectPayload `json:"project"`
	ObjectAttributes *gitLabMRAttrPayload `json:"object_attributes"`
	User             gitLabUserRefPayload `json:"user"`
}

type gitLabProjectPayload struct {
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
}

// gitLabUserRefPayload is unused today (no field of GitLabWebhookEvent reads
// it) but decoded for parity with the payload GitLab actually sends, should
// a future change need "who triggered this" (e.g. for the auto-comment).
type gitLabUserRefPayload struct {
	Username string `json:"username"`
}

type gitLabMRAttrPayload struct {
	IID            int    `json:"iid"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	State          string `json:"state"` // "opened" | "closed" | "merged" | "locked"
	Action         string `json:"action"`
	SourceBranch   string `json:"source_branch"`
	URL            string `json:"url"`
	MergeCommitSHA string `json:"merge_commit_sha"`
}

// GitLabWebhook handles POST /webhooks/gitlab. It receives GitLab webhook
// events, validates the X-Gitlab-Token shared secret when configured, and
// for "Merge Request Hook" events delegates to the service orchestrator
// (which manages link upsert + task transition policy) — mirrors
// GitHubWebhook's structure, adapted to GitLab's auth model (a shared
// secret sent verbatim, not an HMAC signature over the body) and payload
// shape. Any other event type (Push Hook, Note Hook, Pipeline Hook, ...) is
// accepted and ignored, same policy as GitHubWebhook's default case.
func (h *VCSLinkHandler) GitLabWebhook(c echo.Context) error {
	event := c.Request().Header.Get("X-Gitlab-Event")
	if event == "" {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("missing X-Gitlab-Event header"))
	}

	// GitLab authenticates webhooks with a shared secret sent verbatim in
	// X-Gitlab-Token — a direct (constant-time) compare against the
	// configured secret, unlike GitHub's HMAC-SHA256 signature over the
	// body. No body bytes are needed to validate it, so this can happen
	// before reading the body at all. secrets/required follow the same
	// §C1/§C2 contract as GitHubWebhook above — see gitlabWebhookSecrets's
	// doc comment.
	secrets, required := h.gitlabWebhookSecrets(c.Request().Context())
	if required {
		if len(secrets) == 0 {
			return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("gitlab integration is disabled: no active workspace webhook secret and no MESH_GITLAB_WEBHOOK_SECRET fallback configured"))
		}
		token := c.Request().Header.Get("X-Gitlab-Token")
		if token == "" {
			return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("missing X-Gitlab-Token header"))
		}
		matched := false
		for _, secret := range secrets {
			if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1 {
				matched = true
				break
			}
		}
		if !matched {
			return c.JSON(http.StatusUnauthorized, apierror.Unauthorized("invalid webhook token"))
		}
	}

	if event != "Merge Request Hook" {
		return c.JSON(http.StatusOK, map[string]string{"status": "ignored"})
	}

	rawBody, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("failed to read request body"))
	}

	var payload GitLabWebhookPayload
	if jsonErr := decodeJSON(rawBody, &payload); jsonErr != nil {
		return c.JSON(http.StatusBadRequest, apierror.BadRequest("invalid payload"))
	}
	if payload.ObjectAttributes == nil {
		return c.JSON(http.StatusOK, map[string]string{"status": "ignored"})
	}

	attrs := payload.ObjectAttributes
	ev := service.GitLabWebhookEvent{
		Action:      attrs.Action,
		MRIID:       attrs.IID,
		MRTitle:     attrs.Title,
		MRBody:      attrs.Description,
		MRURL:       attrs.URL,
		MRState:     attrs.State,
		MergeSHA:    attrs.MergeCommitSHA,
		MRBranch:    attrs.SourceBranch,
		ProjectPath: payload.Project.PathWithNamespace,
	}
	result, herr := h.vcsService.HandleGitLabMergeRequestEvent(c.Request().Context(), ev)
	if herr != nil {
		c.Logger().Errorf("gitlab webhook: merge_request handler: %v", herr)
		return c.JSON(http.StatusOK, map[string]string{"status": "error_logged"})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"status":       "ok",
		"task_id":      result.TaskID,
		"transitioned": result.Transitioned,
		"reason":       result.Reason,
		"old_status":   result.OldStatus,
		"new_status":   result.NewStatus,
	})
}

// verifyGitHubSignature validates the X-Hub-Signature-256 header value against the
// HMAC-SHA256 of body using secret. The header format is "sha256=<hex>".
// Uses constant-time comparison to prevent timing attacks.
func verifyGitHubSignature(body []byte, sigHeader, secret string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}
	hexSig := sigHeader[len(prefix):]
	gotSig, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)
	return subtle.ConstantTimeCompare(gotSig, expected) == 1
}

// decodeJSON unmarshals JSON bytes into v.
func decodeJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// shortCommitSHA abbreviates a commit id for a log line.
func shortCommitSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx != -1 {
		return s[:idx]
	}
	return s
}

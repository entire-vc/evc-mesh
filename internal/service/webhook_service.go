package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
)

const (
	// webhookMaxFailures is the number of consecutive failures before a webhook is auto-deactivated.
	webhookMaxFailures = 10
	// webhookMaxAttempts is the maximum number of delivery attempts per event.
	webhookMaxAttempts = 3
	// webhookDefaultDeliveryLimit is the default number of deliveries returned in ListDeliveries.
	webhookDefaultDeliveryLimit = 50
	// webhookDialTimeout bounds the TCP connect to a webhook receiver.
	webhookDialTimeout = 5 * time.Second
	// webhookRequestTimeout bounds one whole delivery attempt.
	webhookRequestTimeout = 10 * time.Second
)

// webhookService implements WebhookService.
type webhookService struct {
	repo     repository.WebhookRepository
	client   *http.Client
	slackSvc SlackService
}

// WebhookServiceOption configures optional dependencies for webhookService.
type WebhookServiceOption func(*webhookService)

// WithSlackService injects a SlackService into the webhookService.
// When set, Dispatch will also send Slack notifications for task lifecycle events.
func WithSlackService(ss SlackService) WebhookServiceOption {
	return func(s *webhookService) {
		s.slackSvc = ss
	}
}

// NewWebhookService returns a new WebhookService backed by the given repository.
func NewWebhookService(repo repository.WebhookRepository, opts ...WebhookServiceOption) WebhookService {
	s := &webhookService{
		repo:   repo,
		client: newWebhookHTTPClient(isPubliclyRoutable),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// newWebhookHTTPClient builds the client used for outbound webhook delivery.
// The URL check at write time (validateWebhookURL) can only see what the name
// resolves to today, so it is not what keeps the server out of its own
// network: this client is. It dials only addresses allow accepts, checked on
// the exact IP right before connect (closes DNS rebinding between the write and
// any later delivery or retry), and never follows a redirect (a 30x is
// returned as-is and recorded as a failed delivery, not retried).
//
// allow is isPubliclyRoutable in production; tests pass a predicate that
// admits their loopback httptest server.
//
// Proxy is deliberately left nil: an HTTP(S)_PROXY from the environment would
// make the dial check see the proxy's address, not the receiver's.
func newWebhookHTTPClient(allow func(net.IP) bool) *http.Client {
	return &http.Client{
		Timeout: webhookRequestTimeout,
		// Hand the 30x back as the response instead of following it: the
		// Location is chosen by whoever runs the receiver, and the delivery log
		// then shows the status the receiver actually answered.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext:         guardedDialContext(webhookDialTimeout, allow),
			TLSHandshakeTimeout: webhookDialTimeout,
			// Receivers are chosen by tenants and deliveries are sporadic:
			// pooling idle connections per host would only leak descriptors.
			DisableKeepAlives: true,
		},
	}
}

// isPermanentWebhookError reports whether a delivery error is our own guard
// refusing the address, or a name that does not exist.
// Retrying cannot change the outcome, and the retry schedule sleeps 1s+5s+25s
// per event. Temporary resolver failures and ordinary network errors still
// retry.
func isPermanentWebhookError(err error) bool {
	if errors.Is(err, errDialAddressRefused) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// Create generates a random secret and persists the webhook configuration.
func (s *webhookService) Create(ctx context.Context, input domain.CreateWebhookInput) (*domain.WebhookConfig, error) {
	if strings.TrimSpace(input.Name) == "" {
		return nil, apierror.ValidationError(map[string]string{
			"name": "name is required",
		})
	}
	if err := validateWebhookURL(input.URL); err != nil {
		return nil, err
	}
	if len(input.Events) == 0 {
		return nil, apierror.ValidationError(map[string]string{
			"events": "at least one event type is required",
		})
	}

	secret, err := generateSecret()
	if err != nil {
		return nil, fmt.Errorf("generate webhook secret: %w", err)
	}

	now := time.Now()
	wh := &domain.WebhookConfig{
		ID:          uuid.New(),
		WorkspaceID: input.WorkspaceID,
		Name:        input.Name,
		URL:         input.URL,
		Secret:      secret,
		Events:      input.Events,
		IsActive:    true,
		CreatedBy:   input.CreatedBy,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.repo.Create(ctx, wh); err != nil {
		return nil, err
	}
	return wh, nil
}

// GetByID retrieves a webhook configuration by its ID.
func (s *webhookService) GetByID(ctx context.Context, id uuid.UUID) (*domain.WebhookConfig, error) {
	wh, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if wh == nil {
		return nil, apierror.NotFound("Webhook")
	}
	return wh, nil
}

// Update applies a partial update to a webhook configuration.
func (s *webhookService) Update(ctx context.Context, id uuid.UUID, input domain.UpdateWebhookInput) (*domain.WebhookConfig, error) {
	if input.URL != nil {
		if err := validateWebhookURL(*input.URL); err != nil {
			return nil, err
		}
	}
	wh, err := s.repo.Update(ctx, id, input)
	if err != nil {
		return nil, err
	}
	return wh, nil
}

// Delete removes a webhook configuration.
func (s *webhookService) Delete(ctx context.Context, id uuid.UUID) error {
	return s.repo.Delete(ctx, id)
}

// ListByWorkspace returns all webhook configurations for a workspace.
func (s *webhookService) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.WebhookConfig, error) {
	return s.repo.ListByWorkspace(ctx, workspaceID)
}

// ListDeliveries returns recent delivery records for a webhook.
func (s *webhookService) ListDeliveries(ctx context.Context, webhookID uuid.UUID, limit int) ([]domain.WebhookDelivery, error) {
	if limit <= 0 {
		limit = webhookDefaultDeliveryLimit
	}
	return s.repo.ListDeliveries(ctx, webhookID, limit)
}

// Dispatch finds active webhooks subscribed to eventType and fires HTTP POSTs asynchronously.
// It also forwards the event to the Slack integration if one is configured.
// This method never blocks the caller.
func (s *webhookService) Dispatch(ctx context.Context, workspaceID uuid.UUID, eventType string, payload any) {
	webhooks, err := s.repo.ListActiveByEvent(ctx, workspaceID, eventType)
	if err != nil {
		log.Printf("[webhook] failed to list active webhooks for event %s: %v", eventType, err)
		// Continue so Slack dispatch is still attempted.
	}

	if len(webhooks) > 0 {
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			log.Printf("[webhook] failed to marshal payload for event %s: %v", eventType, err)
		} else {
			for i := range webhooks {
				wh := webhooks[i]
				go s.dispatchOne(wh, eventType, payloadBytes)
			}
		}
	}

	// Forward to Slack integration (NotifyTaskEvent is itself fire-and-forget).
	if s.slackSvc != nil {
		event := slackEventFromPayload(eventType, payload)
		s.slackSvc.NotifyTaskEvent(ctx, workspaceID, event)
	}
}

// TestDelivery fetches the webhook by ID and fires a single test HTTP POST directly
// to its URL, bypassing event subscription filtering. The call is asynchronous.
func (s *webhookService) TestDelivery(ctx context.Context, webhookID uuid.UUID) {
	wh, err := s.repo.GetByID(ctx, webhookID)
	if err != nil || wh == nil {
		log.Printf("[webhook] TestDelivery: webhook %s not found: %v", webhookID, err)
		return
	}

	payload := map[string]any{
		"event":      "webhook.test",
		"webhook_id": wh.ID.String(),
		"message":    "This is a test delivery from evc-mesh",
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[webhook] TestDelivery: marshal payload: %v", err)
		return
	}

	go s.dispatchOne(*wh, "webhook.test", payloadBytes)
}

// dispatchOne sends a single webhook delivery with up to webhookMaxAttempts retries.
func (s *webhookService) dispatchOne(wh domain.WebhookConfig, eventType string, payloadBytes []byte) {
	deliveryID := uuid.New()
	backoffs := []time.Duration{1 * time.Second, 5 * time.Second, 25 * time.Second}

	var (
		lastStatus   *int
		lastBody     *string
		lastDuration *int
		success      bool
		lastAttempt  int
	)

	for attempt := 1; attempt <= webhookMaxAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(backoffs[attempt-2])
		}

		lastAttempt = attempt
		status, body, duration, err := s.sendHTTP(wh, eventType, deliveryID, payloadBytes, attempt)
		lastDuration = &duration
		if err == nil {
			lastStatus = &status
			bodyStr := body
			lastBody = &bodyStr
			if status >= 200 && status < 300 {
				success = true
				break
			}
			if status >= 300 && status < 400 {
				// Not followed, and a retry will not change the answer.
				log.Printf("[webhook] webhook %s event %s: receiver answered %d, redirects are not followed", wh.ID, eventType, status)
				break
			}
		} else {
			log.Printf("[webhook] attempt %d/%d for webhook %s event %s: %v", attempt, webhookMaxAttempts, wh.ID, eventType, err)
			if isPermanentWebhookError(err) {
				break
			}
		}
	}

	// Record delivery.
	delivery := &domain.WebhookDelivery{
		ID:             deliveryID,
		WebhookID:      wh.ID,
		EventType:      eventType,
		Payload:        payloadBytes,
		ResponseStatus: lastStatus,
		ResponseBody:   lastBody,
		DurationMs:     lastDuration,
		Success:        success,
		Attempt:        lastAttempt,
		CreatedAt:      time.Now(),
	}

	// Use a background context so that recording isn't cancelled when the original request ends.
	bgCtx := context.Background()
	if err := s.repo.CreateDelivery(bgCtx, delivery); err != nil {
		log.Printf("[webhook] failed to record delivery for webhook %s: %v", wh.ID, err)
	}

	pkgmetrics.RecordWebhookDispatch(eventType, success)

	if success {
		if err := s.repo.ResetFailure(bgCtx, wh.ID); err != nil {
			log.Printf("[webhook] failed to reset failure count for webhook %s: %v", wh.ID, err)
		}
	} else {
		if err := s.repo.IncrementFailure(bgCtx, wh.ID); err != nil {
			log.Printf("[webhook] failed to increment failure count for webhook %s: %v", wh.ID, err)
		}
		// Auto-deactivate after too many consecutive failures.
		updated, err := s.repo.GetByID(bgCtx, wh.ID)
		if err == nil && updated != nil && updated.FailureCount >= webhookMaxFailures {
			if err := s.repo.Deactivate(bgCtx, wh.ID); err != nil {
				log.Printf("[webhook] failed to deactivate webhook %s: %v", wh.ID, err)
			} else {
				log.Printf("[webhook] auto-deactivated webhook %s after %d consecutive failures", wh.ID, updated.FailureCount)
			}
		}
	}
}

// sendHTTP performs a single HTTP POST to the webhook URL with HMAC-SHA256 signature headers.
// Returns (status, body, duration_ms, error).
func (s *webhookService) sendHTTP(wh domain.WebhookConfig, eventType string, deliveryID uuid.UUID, payloadBytes []byte, _ int) (statusCode int, respBody string, durationMS int, err error) {
	start := time.Now()

	// Compute HMAC-SHA256 signature.
	mac := hmac.New(sha256.New, []byte(wh.Secret))
	mac.Write(payloadBytes)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequest(http.MethodPost, wh.URL, bytes.NewReader(payloadBytes))
	if err != nil {
		return 0, "", 0, fmt.Errorf("create request: %w", err)
	}

	timestamp := time.Now().UTC().Format(time.RFC3339)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mesh-Signature", sig)
	req.Header.Set("X-Mesh-Event", eventType)
	req.Header.Set("X-Mesh-Delivery", deliveryID.String())
	req.Header.Set("X-Mesh-Timestamp", timestamp)
	req.Header.Set("User-Agent", "evc-mesh-webhook/1.0")

	resp, err := s.client.Do(req)
	duration := int(time.Since(start).Milliseconds())
	if err != nil {
		return 0, "", duration, fmt.Errorf("http post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(bodyBytes), duration, nil
}

// slackEventFromPayload converts a raw webhook payload into a TaskEvent for Slack.
// Best-effort: unknown or missing fields are left zero-valued.
func slackEventFromPayload(eventType string, payload any) TaskEvent {
	event := TaskEvent{EventType: eventType}

	m, ok := toStringMap(payload)
	if !ok {
		return event
	}

	if v, ok := uuidFromMap(m, "task_id"); ok {
		event.TaskID = v
	}
	if v, ok := uuidFromMap(m, "project_id"); ok {
		event.ProjectID = v
	}
	if v, ok := stringFromMap(m, "title"); ok {
		event.TaskTitle = v
	}
	if v, ok := stringFromMap(m, "priority"); ok {
		event.Priority = v
	}

	return event
}

// toStringMap attempts to convert payload to map[string]interface{}.
func toStringMap(payload any) (map[string]interface{}, bool) {
	if m, ok := payload.(map[string]interface{}); ok {
		return m, true
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false
	}
	return m, true
}

// uuidFromMap extracts a uuid.UUID from a map value that may be a string or uuid.UUID.
func uuidFromMap(m map[string]interface{}, key string) (uuid.UUID, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return uuid.Nil, false
	}
	switch val := v.(type) {
	case string:
		id, err := uuid.Parse(val)
		if err != nil {
			return uuid.Nil, false
		}
		return id, true
	case uuid.UUID:
		return val, true
	}
	return uuid.Nil, false
}

// stringFromMap extracts a string from a map value.
func stringFromMap(m map[string]interface{}, key string) (string, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// validateWebhookURL refuses a webhook URL that is visibly wrong at write
// time: wrong scheme, a literal non-public IP, localhost, a numeric host that a
// libc resolver would read as an IP, or a name that resolves to a non-public
// address. It uses the same predicate as the delivery dial guard
// (isPubliclyRoutable).
//
// This is a courtesy to the person configuring the webhook (fast, readable
// error), not the security boundary: the answer for a name can change after
// this check. The boundary is newWebhookHTTPClient.
func validateWebhookURL(rawURL string) error {
	fail := func(msg string) error {
		return apierror.ValidationError(map[string]string{"url": msg})
	}
	if strings.TrimSpace(rawURL) == "" {
		return fail("url is required")
	}
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return fail("url is not a valid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fail("url must use http or https scheme")
	}

	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "" {
		return fail("url must have a host")
	}
	const privateMsg = "url must not point to a private or internal address"
	if ip := net.ParseIP(hostname); ip != nil {
		if !isPubliclyRoutable(ip) {
			return fail(privateMsg)
		}
		return nil
	}
	// 2130706433, 0x7f000001, 127.1: not canonical IPs, so ParseIP says no, yet
	// a libc resolver reads each as 127.0.0.1. No real DNS name ends in a
	// numeric label, so refuse the whole shape.
	if isNumericHostLabel(hostname[strings.LastIndex(hostname, ".")+1:]) {
		return fail("url host must be a DNS name or a canonical IP address")
	}
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return fail(privateMsg)
	}

	addrs, err := net.LookupHost(hostname)
	if err != nil {
		return fail("url hostname could not be resolved")
	}
	for _, addr := range addrs {
		if ip := net.ParseIP(addr); ip != nil && !isPubliclyRoutable(ip) {
			return fail(privateMsg)
		}
	}
	return nil
}

// generateSecret generates a random 32-byte hex-encoded secret.
func generateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Ensure webhookService satisfies the WebhookService interface.
var _ WebhookService = (*webhookService)(nil)

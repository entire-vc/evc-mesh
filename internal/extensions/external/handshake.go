package external

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
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Connections finds the workspace connection (integration_configs row) of an
// extension.
type Connections interface {
	GetByProvider(ctx context.Context, ws uuid.UUID, p domain.IntegrationProvider) (*domain.IntegrationConfig, error)
}

// SecretWriter stores the per-connection signing secret.
type SecretWriter interface {
	Create(ctx context.Context, in domain.CreateSecretInput) (domain.Secret, error)
}

// SecretReader reads current workspace secret values (the same materializer
// the delivery side signs with).
type SecretReader interface {
	ResolveCurrentValues(ctx context.Context, ws uuid.UUID, project, agent *uuid.UUID) ([]domain.MaterializedSecret, error)
}

// Subscriptions is the subscription table.
type Subscriptions interface {
	Create(ctx context.Context, sub *domain.ExtensionEventSubscription) (*domain.ExtensionEventSubscription, error)
	ListByConnection(ctx context.Context, connectionID uuid.UUID) ([]domain.ExtensionEventSubscription, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

// installRequest is the body of POST {base}{install_path}. The service keeps
// signing_secret and verifies every later event with it. install_code is the
// one-time code the service issued for binding; it is sent only when the
// workspace holds it, so without one the body is unchanged.
type installRequest struct {
	ExtensionID   string    `json:"extension_id"`
	Version       string    `json:"version"`
	WorkspaceID   uuid.UUID `json:"workspace_id"`
	SigningSecret string    `json:"signing_secret"`
	Events        []string  `json:"events"`
	EventsPath    string    `json:"events_path"`
	InstallCode   string    `json:"install_code,omitempty"`
}

// Handshaker performs the install handshake for one workspace connection and
// makes the subscriptions match the manifest. It is idempotent: the secret is
// generated once and reused, a repeated handshake sends the same secret.
type Handshaker struct {
	Client  *http.Client
	Conns   Connections
	Secrets SecretWriter
	Reader  SecretReader
	Subs    Subscriptions

	mu   sync.Mutex
	done map[string]string // ws/ext -> fingerprint of the last successful handshake
}

// SecretName is the workspace secret that signs events for an extension.
func SecretName(extensionID string) string {
	return secretPrefix(extensionID) + "_SIGNING_SECRET"
}

// InstallCodeName is the workspace secret holding the binding code the service
// issued. An admin or agent sets it through the workspace secrets API; it never
// lives in the connection settings.
func InstallCodeName(extensionID string) string {
	return secretPrefix(extensionID) + "_INSTALL_CODE"
}

func secretPrefix(extensionID string) string {
	return "EXT_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(extensionID))
}

// Sign is the documented request signature: hex HMAC-SHA256 of
// "{timestamp}.{body}" (docs/webhooks.md).
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *Handshaker) key(ws uuid.UUID, ext string) string { return ws.String() + "/" + ext }

// fingerprint covers what the service was told, including the full SHA-256
// digests of the signing secret and the install code, so a rotated secret or a
// new code forces a new handshake.
func fingerprint(m extensions.Manifest, base, secret, code string) string {
	ev := slices.Clone(m.Capabilities.Events)
	slices.Sort(ev)
	sum := sha256.Sum256([]byte(secret))
	fp := m.Version + "|" + base + "|" + strings.Join(ev, ",") + "|" + hex.EncodeToString(sum[:])
	if code != "" {
		cs := sha256.Sum256([]byte(code))
		fp += "|" + hex.EncodeToString(cs[:])
	}
	return fp
}

// Forget drops the memo so the next Ensure or Run talks to the service again.
func (h *Handshaker) Forget(ws uuid.UUID, ext string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.done, h.key(ws, ext))
}

// Ensure runs the handshake unless it already succeeded for this version,
// address, event list and signing secret.
func (h *Handshaker) Ensure(ctx context.Context, p *Provider, ws uuid.UUID, settings map[string]any) error {
	base, err := p.baseURL(settings)
	if err != nil {
		return err
	}
	secret, ok, err := h.readSecret(ctx, ws, SecretName(p.Man.ID), "signing secret")
	if err != nil || !ok {
		return h.Run(ctx, p, ws, settings)
	}
	code, err := h.installCode(ctx, p, ws)
	if err != nil {
		return h.Run(ctx, p, ws, settings)
	}
	fp := fingerprint(p.Man, base, secret, code)
	h.mu.Lock()
	same := h.done[h.key(ws, p.Man.ID)] == fp
	h.mu.Unlock()
	if same {
		return nil
	}
	return h.Run(ctx, p, ws, settings)
}

// Run does the handshake now: ensure the signing secret, tell the service
// (install_path), then align subscriptions. Errors carry no secret and no
// response body.
func (h *Handshaker) Run(ctx context.Context, p *Provider, ws uuid.UUID, settings map[string]any) error {
	m := p.Man
	h.Forget(ws, m.ID)
	base, err := p.baseURL(settings)
	if err != nil {
		return err
	}
	conn, err := h.Conns.GetByProvider(ctx, ws, domain.IntegrationProvider(m.ID))
	if err != nil {
		return fmt.Errorf("read connection: %w", err)
	}
	if conn == nil || !conn.IsActive {
		return errors.New("the extension is not connected in this workspace")
	}
	name := SecretName(m.ID)
	secret, err := h.signingSecret(ctx, ws, name)
	if err != nil {
		return err
	}
	code, err := h.installCode(ctx, p, ws)
	if err != nil {
		return err
	}
	events := publicDeclared(m)
	if p.ext.InstallPath != "" {
		if err := h.install(ctx, p, base, installRequest{
			ExtensionID: m.ID, Version: m.Version, WorkspaceID: ws, SigningSecret: secret,
			Events: events, EventsPath: p.ext.EventsPath, InstallCode: code,
		}, secret); err != nil {
			return err
		}
	}
	if err := h.alignSubscriptions(ctx, conn.ID, base, p.ext.EventsPath, name, events); err != nil {
		return err
	}
	h.mu.Lock()
	if h.done == nil {
		h.done = map[string]string{}
	}
	h.done[h.key(ws, m.ID)] = fingerprint(m, base, secret, code)
	h.mu.Unlock()
	return nil
}

func publicDeclared(m extensions.Manifest) []string {
	var out []string
	for _, e := range m.Capabilities.Events {
		if extensions.IsPublicEvent(e) && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// installCode reads the optional binding code; "" when none is set or the
// extension has no install endpoint to send it to.
func (h *Handshaker) installCode(ctx context.Context, p *Provider, ws uuid.UUID) (string, error) {
	if p.ext.InstallPath == "" {
		return "", nil
	}
	v, _, err := h.readSecret(ctx, ws, InstallCodeName(p.Man.ID), "install code")
	return v, err
}

func (h *Handshaker) signingSecret(ctx context.Context, ws uuid.UUID, name string) (string, error) {
	if v, ok, err := h.readSecret(ctx, ws, name, "signing secret"); err != nil || ok {
		return v, err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate signing secret: %w", err)
	}
	_, err := h.Secrets.Create(ctx, domain.CreateSecretInput{
		WorkspaceID: ws, Scope: domain.SecretScopeWorkspace, Name: name, Value: hex.EncodeToString(buf),
		CreatedBy: uuid.Nil, CreatedByType: domain.ActorTypeSystem,
	})
	if err != nil {
		var ae *apierror.Error
		if !errors.As(err, &ae) || ae.Code != http.StatusConflict {
			return "", errors.New("store signing secret failed")
		}
	}
	// Read back what is stored: after a lost create race the other writer's value wins.
	v, ok, err := h.readSecret(ctx, ws, name, "signing secret")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("signing secret is not readable after it was stored")
	}
	return v, nil
}

// readSecret finds a current workspace secret by name. Errors name what was
// looked up (what) and never carry the value.
func (h *Handshaker) readSecret(ctx context.Context, ws uuid.UUID, name, what string) (value string, found bool, err error) {
	all, err := h.Reader.ResolveCurrentValues(ctx, ws, nil, nil)
	if err != nil {
		return "", false, errors.New(what + " lookup failed")
	}
	for i := range all {
		if all[i].Name == name {
			if all[i].Expired || all[i].Value == "" {
				return "", false, errors.New(what + " is expired")
			}
			return all[i].Value, true, nil
		}
	}
	return "", false, nil
}

func (h *Handshaker) install(ctx context.Context, p *Provider, base string, req installRequest, secret string) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, base+p.ext.InstallPath, bytes.NewReader(body))
	if err != nil {
		return errors.New("bad install URL")
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Mesh-Timestamp", ts)
	hr.Header.Set("X-Mesh-Signature", Sign(secret, ts, body))
	hr.Header.Set("User-Agent", "evc-mesh-extension/1.0")
	resp, err := h.Client.Do(hr)
	if err != nil {
		return errors.New("service unreachable")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("install endpoint answered %d", resp.StatusCode)
	}
	return nil
}

// alignSubscriptions makes the connection's webhook subscriptions exactly the
// manifest events pointing at base+events_path, signed with secretRef.
func (h *Handshaker) alignSubscriptions(ctx context.Context, connID uuid.UUID, base, eventsPath, secretRef string, events []string) error {
	if eventsPath == "" {
		events = nil
	}
	target := base + eventsPath
	existing, err := h.Subs.ListByConnection(ctx, connID)
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}
	have := map[string]bool{}
	for i := range existing {
		s := existing[i]
		if s.Delivery != domain.ExtensionDeliveryWebhook || s.SecretRef != secretRef {
			continue
		}
		if s.Target == target && slices.Contains(events, s.Event) {
			have[s.Event] = true
			continue
		}
		// Ours (signed with the extension secret) but stale: other target or an event the manifest dropped.
		if err := h.Subs.Delete(ctx, s.ID); err != nil {
			return fmt.Errorf("remove stale subscription: %w", err)
		}
	}
	for _, ev := range events {
		if have[ev] {
			continue
		}
		if _, err := h.Subs.Create(ctx, &domain.ExtensionEventSubscription{
			ID: uuid.New(), ConnectionID: connID, Event: ev,
			Delivery: domain.ExtensionDeliveryWebhook, Target: target, SecretRef: secretRef,
		}); err != nil {
			return fmt.Errorf("subscribe %s: %w", ev, err)
		}
	}
	return nil
}

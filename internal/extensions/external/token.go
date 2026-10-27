package external

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
)

// TokenStore is the extension actor side of token delivery
// (service.ExtensionActorService satisfies it).
type TokenStore interface {
	RotateForDelivery(ctx context.Context, workspaceID, connectionID uuid.UUID) (token string, restore func(context.Context) error, err error)
	TokenDeliveredAt(ctx context.Context, connectionID uuid.UUID) (*time.Time, error)
	MarkTokenDelivered(ctx context.Context, connectionID uuid.UUID, token string, at time.Time) error
}

// tokenRequest is the body of POST {base}{token_path}.
type tokenRequest struct {
	ExtensionID    string    `json:"extension_id"`
	WorkspaceID    uuid.UUID `json:"workspace_id"`
	ExtensionToken string    `json:"extension_token"`
}

// DeliveryError is a token delivery the service did not confirm. Its text
// carries only the status and the host, never the token or a response body.
type DeliveryError struct{ msg string }

func (e *DeliveryError) Error() string { return e.msg }

// Delivered is the outcome of a successful rotate-and-deliver.
type Delivered struct {
	// Rotated is true when a previous token exists and keeps working for the
	// rotation grace; false when this was the connection's first token.
	Rotated bool
}

// tokenDelivery serialises deliveries per connection: the signature
// timestamp of one must be strictly later than the previous one, and a
// rollback must never race a newer rotation of the same process.
type tokenDelivery struct {
	mu    sync.Mutex
	locks map[uuid.UUID]*sync.Mutex
}

func (d *tokenDelivery) lock(connID uuid.UUID) func() {
	d.mu.Lock()
	if d.locks == nil {
		d.locks = map[uuid.UUID]*sync.Mutex{}
	}
	l, ok := d.locks[connID]
	if !ok {
		l = &sync.Mutex{}
		d.locks[connID] = l
	}
	d.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// HasTokenPath reports whether the extension takes its token from Mesh.
func (p *Provider) HasTokenPath() bool { return p.ext.TokenPath != "" }

// RotateAndDeliver issues a new actor token for the connection and posts it to
// the service's token_path. When the service does not confirm, the rotation is
// undone and the previous token keeps working exactly as before the call.
func (h *Handshaker) RotateAndDeliver(ctx context.Context, p *Provider, ws, connID uuid.UUID, settings map[string]any) (Delivered, error) {
	if !p.HasTokenPath() {
		return Delivered{}, errors.New("the extension has no token_path")
	}
	if h.Tokens == nil {
		return Delivered{}, errors.New("token delivery is not configured")
	}
	base, err := p.baseURL(settings)
	if err != nil {
		return Delivered{}, err
	}
	secret, ok, err := h.readSecret(ctx, ws, SecretName(p.Man.ID), "signing secret")
	if err != nil {
		return Delivered{}, err
	}
	if !ok {
		return Delivered{}, errors.New("signing secret is missing: the install handshake has not run")
	}
	return h.rotateAndDeliver(ctx, p, ws, connID, base, secret, false)
}

// rotateAndDeliver rotates and posts under the per-connection lock. With
// onlyUndelivered it does nothing when the current token is already delivered;
// the marker is read under the lock so concurrent installs deliver once.
func (h *Handshaker) rotateAndDeliver(ctx context.Context, p *Provider, ws, connID uuid.UUID, base, secret string, onlyUndelivered bool) (Delivered, error) {
	defer h.delivery.lock(connID)()
	last, err := h.Tokens.TokenDeliveredAt(ctx, connID)
	if err != nil {
		return Delivered{}, fmt.Errorf("read token delivery state: %w", err)
	}
	if onlyUndelivered && last != nil {
		return Delivered{}, nil
	}
	ts := time.Now().Unix()
	if last != nil && last.Unix() >= ts {
		ts = last.Unix() + 1
	}
	token, restore, err := h.Tokens.RotateForDelivery(ctx, ws, connID)
	if err != nil {
		return Delivered{}, err
	}
	if derr := h.postToken(ctx, p, base, secret, ts, tokenRequest{ExtensionID: p.Man.ID, WorkspaceID: ws, ExtensionToken: token}); derr != nil {
		if restore != nil {
			// The caller may be gone; the old token must come back regardless.
			if rerr := restore(context.WithoutCancel(ctx)); rerr != nil {
				slog.Error("extension token delivery: restoring the previous token failed",
					"extension", p.Man.ID, "connection_id", connID, "error", rerr)
			}
		}
		return Delivered{}, derr
	}
	if merr := h.Tokens.MarkTokenDelivered(ctx, connID, token, time.Unix(ts, 0)); merr != nil {
		// The service holds the token; only the marker is missing, so a later
		// install handshake delivers once more.
		slog.Warn("extension token delivered but not recorded",
			"extension", p.Man.ID, "connection_id", connID, "error", merr)
	}
	return Delivered{Rotated: restore != nil}, nil
}

// postToken sends the token. Only 200 with {"ok":true} counts as delivered.
func (h *Handshaker) postToken(ctx context.Context, p *Provider, base, secret string, ts int64, req tokenRequest) error {
	host := ""
	if u, err := url.Parse(base); err == nil {
		host = u.Hostname()
	}
	fail := func(what string) error { return &DeliveryError{msg: "token delivery to " + host + " failed: " + what} }
	body, err := json.Marshal(req)
	if err != nil {
		return fail("encode")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, base+p.ext.TokenPath, bytes.NewReader(body))
	if err != nil {
		return fail("bad token URL")
	}
	tss := strconv.FormatInt(ts, 10)
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Mesh-Timestamp", tss)
	hr.Header.Set("X-Mesh-Signature", Sign(secret, tss, body))
	hr.Header.Set("User-Agent", "evc-mesh-extension/1.0")
	resp, err := h.Client.Do(hr)
	if err != nil {
		return fail("service unreachable")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fail("answered " + strconv.Itoa(resp.StatusCode))
	}
	var ack struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(raw, &ack) != nil || !ack.OK {
		return fail(`answered 200 without {"ok":true}`)
	}
	return nil
}

// deliverOnInstall hands the first token to a service that has not got one.
// A repeated handshake finds it delivered and does nothing.
func (h *Handshaker) deliverOnInstall(ctx context.Context, p *Provider, ws, connID uuid.UUID, base, secret string) error {
	if !p.HasTokenPath() || h.Tokens == nil {
		return nil
	}
	_, err := h.rotateAndDeliver(ctx, p, ws, connID, base, secret, true)
	return err
}

// TokenRotator serves POST /integrations/:id/rotate-token for extensions that
// take their token from Mesh.
type TokenRotator struct {
	Reg   *extensions.Registry
	Conns interface {
		GetByID(ctx context.Context, id uuid.UUID) (*domain.IntegrationConfig, error)
	}
	HS *Handshaker
}

// RotateAndDeliver rotates and delivers when the connection belongs to an
// external extension with token_path. handled is false for every other
// connection (missing, foreign, builtin, no token_path): the caller rotates
// the usual way and answers as before.
func (t *TokenRotator) RotateAndDeliver(ctx context.Context, ws, connID uuid.UUID) (res Delivered, handled bool, err error) {
	conn, err := t.Conns.GetByID(ctx, connID)
	if err != nil {
		return Delivered{}, false, err
	}
	if conn == nil || conn.WorkspaceID != ws {
		return Delivered{}, false, nil
	}
	pr, ok := t.Reg.Get(string(conn.Provider))
	if !ok {
		return Delivered{}, false, nil
	}
	p, ok := pr.(*Provider)
	if !ok || !p.HasTokenPath() {
		return Delivered{}, false, nil
	}
	var settings map[string]any
	if json.Unmarshal(conn.Config, &settings) != nil {
		return Delivered{}, true, errors.New("connection settings are unreadable")
	}
	res, err = t.HS.RotateAndDeliver(ctx, p, ws, connID, settings)
	return res, true, err
}
